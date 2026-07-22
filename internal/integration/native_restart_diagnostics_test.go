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
	labels                   []string
	drainFailed, replacement bool
}

var nativeLifecycleLabels = map[string]string{
	"daemon started": "daemon-started", "preparing upgrade": "preparing-upgrade",
	"exec-ing new binary": "exec-started", "daemon upgraded": "adoption-started",
	"upgrade attempt failed; old daemon remains active":                 "old-daemon-rollback",
	"upgrade descriptor rollback could not be made safe; shutting down": "unsafe-rollback",
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
			Message string `json:"msg"`
			Error   string `json:"err"`
		}
		if json.Unmarshal([]byte(line), &event) != nil {
			continue
		}

		label, allowed := nativeLifecycleLabels[event.Message]
		if !allowed {
			continue
		}

		evidence.replacement = evidence.replacement || label == "exec-started" || label == "adoption-started" || label == "unsafe-rollback"
		errorClass := nativeRestartErrorClass(event.Error)
		evidence.drainFailed = evidence.drainFailed || strings.HasSuffix(errorClass, "-drain")

		evidence.replacement = evidence.replacement || errorClass == "exec"
		if errorClass != "none" {
			label += ":" + errorClass
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
	case evidence.drainFailed:
		return "post-ack-drain-rollback"
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

	classes := [][2]string{
		{nativeRestartTimeoutClass(nativeRestartTimeoutObservation{sameGenerationHandshakes: 1}, nativeRestartLogEvidence{}), "old-generation-still-serving"},
		{nativeRestartTimeoutClass(nativeRestartTimeoutObservation{failedHandshakes: 1}, nativeRestartLogEvidence{}), "daemon-unreachable-or-handshake-failed"},
		{nativeRestartTimeoutClass(nativeRestartTimeoutObservation{daemonDone: true}, nativeRestartLogEvidence{}), "old-daemon-exited"},
		{nativeRestartTimeoutClass(nativeRestartTimeoutObservation{}, nativeRestartLogEvidence{drainFailed: true}), "post-ack-drain-rollback"},
		{nativeRestartTimeoutClass(nativeRestartTimeoutObservation{}, nativeRestartLogEvidence{replacement: true}), "exec-replacement-adoption-or-startup-failure"},
	}
	for _, class := range classes {
		if class[0] != class[1] {
			t.Errorf("classification = %q, want %q", class[0], class[1])
		}
	}

	summary := nativeRestartTimeoutSummary(nativeRestartTimeoutObservation{sameGenerationHandshakes: 7, failedHandshakes: 3, lastHandshakeErrorClass: nativeHandshakeErrorClass(errors.New("handshake read failed")), daemonCurrent: "current", daemonCompletionClass: "running"}, evidence, errors.New("dreich"), nil)
	for _, want := range []string{"same_generation_handshakes=7", "failed_handshakes=3", "last_handshake_error=handshake-read-failed", "cleanup=failed", "lifecycle=old-daemon-rollback:background-drain"} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary %q does not contain %q", summary, want)
		}
	}
}
