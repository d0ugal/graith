//go:build integration

package integration

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
)

type nativeRestartTimeoutObservation struct {
	sameGenerationHandshakes, failedHandshakes                    int
	lastHandshakeErrorClass, daemonCurrent, daemonCompletionClass string
	daemonDone                                                    bool
}

type nativeRestartLogEvidence struct {
	labels                                []string
	preAckDrainFailed, postAckDrainFailed bool
	replacement                           bool
}

var nativeLifecycleLabels = map[string]string{
	"daemon started": "daemon-started", "preparing upgrade": "preparing-upgrade",
	"exec-ing new binary": "exec-started", "daemon upgraded": "daemon-upgraded",
	"upgrade adoption bootstrap started":                                "adoption-bootstrap",
	"upgrade adoption loaded state snapshot":                            "adoption-state-loaded",
	"upgrade adoption reaped inherited terminal helpers":                "adoption-helpers-reaped",
	"upgrade adoption adopted listener":                                 "adoption-listener",
	"upgrade adoption adopted sessions":                                 "adoption-sessions",
	"upgrade attempt failed; old daemon remains active":                 "old-daemon-rollback",
	"upgrade descriptor rollback could not be made safe; shutting down": "unsafe-rollback",
	"startup recovery started":                                          "startup-recovery-started",
	"startup recovery completed":                                        "startup-recovery-completed",
}

func readBoundedNativeTail(path string, limit int) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil {
		return nil, err
	}

	if _, err := file.Seek(max(info.Size()-int64(limit), 0), io.SeekStart); err != nil {
		return nil, err
	}

	return io.ReadAll(io.LimitReader(file, int64(limit)))
}

func classifyNativeRestartLog(value []byte) nativeRestartLogEvidence {
	var evidence nativeRestartLogEvidence

	for _, line := range strings.Split(string(value), "\n") {
		var event struct {
			Message  string `json:"msg"`
			Error    string `json:"err"`
			Recovery string `json:"recovery"`
			Stage    string `json:"stage"`
		}
		if json.Unmarshal([]byte(line), &event) != nil {
			continue
		}

		label, allowed := nativeLifecycleLabels[event.Message]
		if !allowed {
			switch event.Message {
			case "upgrade stage started":
				label = "upgrade-stage-started"
			case "upgrade stage completed":
				label = "upgrade-stage-completed"
			case "upgrade stage failed":
				label = "upgrade-stage-failed"
			default:
				continue
			}
		}

		evidence.replacement = evidence.replacement || label == "exec-started" ||
			strings.HasPrefix(label, "adoption-") || label == "daemon-upgraded" || label == "unsafe-rollback"
		errorClass := nativeRestartErrorClass(event.Error)
		preAckDrainStage := event.Stage == "drain-admitted-work" || event.Stage == "drain-background-work"
		postAckDrainStage := event.Stage == "quiesce-session-io"
		evidence.preAckDrainFailed = evidence.preAckDrainFailed ||
			errorClass == "background-drain" ||
			(label == "upgrade-stage-failed" && preAckDrainStage)
		evidence.postAckDrainFailed = evidence.postAckDrainFailed ||
			errorClass == "session-io-drain" ||
			(label == "upgrade-stage-failed" && postAckDrainStage)

		evidence.replacement = evidence.replacement || errorClass == "exec"
		if errorClass != "none" {
			label += ":" + errorClass
		}

		if strings.HasPrefix(label, "startup-recovery-") && event.Recovery != "" {
			label += ":" + event.Recovery
		}

		if strings.HasPrefix(label, "upgrade-stage-") && event.Stage != "" {
			label += ":" + event.Stage
		}

		evidence.labels = append(evidence.labels, label)
	}

	return evidence
}

func nativeRestartErrorClass(value string) string {
	switch {
	case strings.Contains(value, "upgrade background drain failed"):
		return "background-drain"
	case strings.Contains(value, "upgrade session I/O drain failed"):
		return "session-io-drain"
	case strings.Contains(value, "upgrade exec failed"):
		return "exec"
	case value != "":
		return "other"
	default:
		return "none"
	}
}

func nativeRestartTimeoutClass(observation nativeRestartTimeoutObservation, evidence nativeRestartLogEvidence) string {
	switch {
	case evidence.postAckDrainFailed:
		return "post-ack-drain-rollback"
	case evidence.preAckDrainFailed:
		return "pre-ack-drain-refusal"
	case evidence.replacement:
		return "exec-replacement-adoption-or-startup-failure"
	case observation.daemonDone || observation.daemonCurrent == "exited":
		return "old-daemon-exited"
	case observation.sameGenerationHandshakes > 0:
		return "old-generation-still-serving"
	default:
		return "daemon-unreachable-or-handshake-failed"
	}
}

func nativeRestartTimeoutSummary(observation nativeRestartTimeoutObservation, evidence nativeRestartLogEvidence, cleanupErr, logErr error) string {
	lastHandshake, lifecycle := observation.lastHandshakeErrorClass, strings.Join(evidence.labels, ",")
	if lastHandshake == "" {
		lastHandshake = "none"
	}

	if lifecycle == "" {
		lifecycle = "none"
	}

	cleanup, logRead := "clean", "ok"
	if cleanupErr != nil {
		cleanup = "failed"
	}

	if logErr != nil {
		logRead = "failed"
	}

	return fmt.Sprintf("classification=%s same_generation_handshakes=%d failed_handshakes=%d last_handshake_error=%s daemon_done_at_timeout=%t daemon_current_at_timeout=%s daemon_completion=%s cleanup=%s log_read=%s lifecycle=%s",
		nativeRestartTimeoutClass(observation, evidence), observation.sameGenerationHandshakes,
		observation.failedHandshakes, lastHandshake, observation.daemonDone, observation.daemonCurrent,
		observation.daemonCompletionClass, cleanup, logRead, lifecycle)
}

func nativeHandshakeErrorClass(err error) string {
	class := strings.ReplaceAll(err.Error(), " ", "-")
	switch class {
	case "token-unavailable", "dial-failed", "handshake-write-failed", "handshake-read-failed", "handshake-rejected", "handshake-decode-failed":
		return class
	default:
		return "unclassified"
	}
}

func TestNativeRestartDiagnostics(t *testing.T) {
	path := t.TempDir() + "/daemon.log"
	if err := os.WriteFile(path, []byte("braw-canny-dreich"), 0o600); err != nil {
		t.Fatal(err)
	}

	if got, err := readBoundedNativeTail(path, 6); err != nil || string(got) != "dreich" {
		t.Fatalf("bounded tail = %q, %v", got, err)
	}

	logTail := []byte(`{"msg":"upgrade attempt failed; old daemon remains active","err":"upgrade background drain failed: deadline","pid":42,"session":"deadbeef","output":"private terminal output"}`)
	evidence := classifyNativeRestartLog(logTail)

	if got := strings.Join(evidence.labels, ","); got != "old-daemon-rollback:background-drain" || strings.Contains(got, "deadbeef") || strings.Contains(got, "terminal") {
		t.Fatalf("safe lifecycle evidence = %q", got)
	}

	if !evidence.preAckDrainFailed || evidence.postAckDrainFailed {
		t.Fatalf("background drain evidence = %+v, want pre-ack drain only", evidence)
	}

	phaseTail := []byte(strings.Join([]string{
		`{"msg":"exec-ing new binary"}`,
		`{"msg":"upgrade adoption bootstrap started","sessions":9}`,
		`{"msg":"upgrade adoption adopted sessions","resolved_sessions":9}`,
		`{"msg":"daemon upgraded"}`,
		`{"msg":"startup recovery started","recovery":"orphaned-processes"}`,
		`{"msg":"startup recovery completed","recovery":"orphaned-processes"}`,
	}, "\n"))

	phaseEvidence := classifyNativeRestartLog(phaseTail)
	if got, want := strings.Join(phaseEvidence.labels, ","), "exec-started,adoption-bootstrap,adoption-sessions,daemon-upgraded,startup-recovery-started:orphaned-processes,startup-recovery-completed:orphaned-processes"; got != want {
		t.Fatalf("phase lifecycle evidence = %q, want %q", got, want)
	}

	stageTail := []byte(strings.Join([]string{
		`{"msg":"upgrade stage started","stage":"drain-background-work"}`,
		`{"msg":"upgrade stage completed","stage":"prepare-exec-boundary","duration_ms":17}`,
		`{"msg":"upgrade stage failed","stage":"drain-admitted-work","err":"context deadline exceeded","session":"hidden"}`,
		`{"msg":"upgrade stage failed","stage":"quiesce-session-io","err":"context deadline exceeded","session":"hidden"}`,
	}, "\n"))

	stageEvidence := classifyNativeRestartLog(stageTail)
	if got, want := strings.Join(stageEvidence.labels, ","), "upgrade-stage-started:drain-background-work,upgrade-stage-completed:prepare-exec-boundary,upgrade-stage-failed:other:drain-admitted-work,upgrade-stage-failed:other:quiesce-session-io"; got != want {
		t.Fatalf("stage lifecycle evidence = %q, want %q", got, want)
	}

	if !stageEvidence.preAckDrainFailed || !stageEvidence.postAckDrainFailed || strings.Contains(strings.Join(stageEvidence.labels, ","), "hidden") {
		t.Fatalf("stage evidence should classify drain failure without private session data: %+v", stageEvidence)
	}

	classes := [][2]string{
		{nativeRestartTimeoutClass(nativeRestartTimeoutObservation{sameGenerationHandshakes: 1}, nativeRestartLogEvidence{}), "old-generation-still-serving"},
		{nativeRestartTimeoutClass(nativeRestartTimeoutObservation{failedHandshakes: 1}, nativeRestartLogEvidence{}), "daemon-unreachable-or-handshake-failed"},
		{nativeRestartTimeoutClass(nativeRestartTimeoutObservation{daemonDone: true}, nativeRestartLogEvidence{}), "old-daemon-exited"},
		{nativeRestartTimeoutClass(nativeRestartTimeoutObservation{}, nativeRestartLogEvidence{preAckDrainFailed: true}), "pre-ack-drain-refusal"},
		{nativeRestartTimeoutClass(nativeRestartTimeoutObservation{}, nativeRestartLogEvidence{postAckDrainFailed: true}), "post-ack-drain-rollback"},
		{nativeRestartTimeoutClass(nativeRestartTimeoutObservation{}, nativeRestartLogEvidence{replacement: true}), "exec-replacement-adoption-or-startup-failure"},
	}
	for _, class := range classes {
		if class[0] != class[1] {
			t.Errorf("classification = %q, want %q", class[0], class[1])
		}
	}

	summary := nativeRestartTimeoutSummary(nativeRestartTimeoutObservation{sameGenerationHandshakes: 7, failedHandshakes: 3, lastHandshakeErrorClass: nativeHandshakeErrorClass(errors.New("handshake read failed")), daemonCurrent: "current", daemonCompletionClass: "running"}, evidence, errors.New("dreich"), nil)
	for _, want := range []string{"classification=pre-ack-drain-refusal", "same_generation_handshakes=7", "failed_handshakes=3", "last_handshake_error=handshake-read-failed", "cleanup=failed", "lifecycle=old-daemon-rollback:background-drain"} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary %q does not contain %q", summary, want)
		}
	}
}
