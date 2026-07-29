package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
)

func TestClassifyGraithUpdate(t *testing.T) {
	tests := map[string]struct {
		previous GraithBuildState
		current  GraithBuildState
		want     string
	}{
		"upgrade": {
			previous: GraithBuildState{Version: "v0.2.1", CommitSHA: "braw"},
			current:  GraithBuildState{Version: "v0.3.0", CommitSHA: "canny"},
			want:     graithUpdateKindUpgrade,
		},
		"downgrade": {
			previous: GraithBuildState{Version: "v0.3.0", CommitSHA: "canny"},
			current:  GraithBuildState{Version: "v0.2.1", CommitSHA: "braw"},
			want:     graithUpdateKindDowngrade,
		},
		"same version replacement": {
			previous: GraithBuildState{Version: "v0.3.0", CommitSHA: "canny"},
			current:  GraithBuildState{Version: "0.3.0", CommitSHA: "dreich"},
			want:     graithUpdateKindRestartSameVersion,
		},
		"unavailable metadata": {
			previous: GraithBuildState{Version: "dev", CommitSHA: "unknown"},
			current:  GraithBuildState{Version: "v0.3.0", CommitSHA: "dreich"},
			want:     graithUpdateKindMetadataUnavailable,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if got := classifyGraithUpdate(test.previous, test.current); got != test.want {
				t.Errorf("classifyGraithUpdate() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestRecordGraithBuildObservationQueuesOnceWhenOrchestratorEnabled(t *testing.T) {
	h := newTestHarnessWithConfig(t, config.Default())
	h.sm.cfg.Orchestrator.Enabled = true
	detectedAt := time.Date(2026, time.July, 29, 18, 30, 0, 0, time.UTC)

	h.sm.state.GraithBuild = &GraithBuildState{
		Version:    "v0.2.1",
		CommitSHA:  "braw",
		ObservedAt: detectedAt.Add(-time.Hour),
	}

	current := GraithBuildState{Version: "v0.3.0", CommitSHA: "canny"}
	if err := h.sm.recordGraithBuildObservation(current, detectedAt); err != nil {
		t.Fatalf("recordGraithBuildObservation: %v", err)
	}

	if err := h.sm.recordGraithBuildObservation(current, detectedAt.Add(time.Minute)); err != nil {
		t.Fatalf("second recordGraithBuildObservation: %v", err)
	}

	pending := h.sm.state.PendingGraithUpdateNotifications
	if len(pending) != 1 {
		t.Fatalf("pending notifications = %d, want 1", len(pending))
	}

	if pending[0].Kind != graithUpdateKindUpgrade {
		t.Errorf("Kind = %q, want %q", pending[0].Kind, graithUpdateKindUpgrade)
	}

	if h.sm.state.GraithBuild.Version != "v0.3.0" || h.sm.state.GraithBuild.CommitSHA != "canny" {
		t.Errorf("GraithBuild = %+v, want current version/commit", h.sm.state.GraithBuild)
	}
}

func TestRecordGraithBuildObservationKeepsRepeatedVersionPairTransitionsDistinct(t *testing.T) {
	h := newTestHarnessWithConfig(t, config.Default())
	h.sm.cfg.Orchestrator.Enabled = true

	observedAt := time.Date(2026, time.July, 29, 18, 0, 0, 0, time.UTC)
	h.sm.state.GraithBuild = &GraithBuildState{
		Version:    "v0.2.1",
		CommitSHA:  "braw",
		ObservedAt: observedAt,
	}

	observations := []struct {
		build      GraithBuildState
		detectedAt time.Time
	}{
		{build: GraithBuildState{Version: "v0.3.0", CommitSHA: "canny"}, detectedAt: observedAt.Add(time.Minute)},
		{build: GraithBuildState{Version: "v0.2.1", CommitSHA: "braw"}, detectedAt: observedAt.Add(2 * time.Minute)},
		{build: GraithBuildState{Version: "v0.3.0", CommitSHA: "canny"}, detectedAt: observedAt.Add(3 * time.Minute)},
	}

	for _, observation := range observations {
		if err := h.sm.recordGraithBuildObservation(observation.build, observation.detectedAt); err != nil {
			t.Fatalf("recordGraithBuildObservation(%+v): %v", observation.build, err)
		}
	}

	pending := h.sm.state.PendingGraithUpdateNotifications
	if len(pending) != 3 {
		t.Fatalf("pending notifications = %d, want every transition retained", len(pending))
	}

	if pending[0].ID == pending[2].ID {
		t.Fatalf("repeated A-to-B transition reused event ID %q", pending[0].ID)
	}

	if pending[0].Previous.Version != "v0.2.1" || pending[0].Current.Version != "v0.3.0" {
		t.Fatalf("first transition = %+v -> %+v, want v0.2.1 -> v0.3.0", pending[0].Previous, pending[0].Current)
	}

	if pending[2].Previous.Version != "v0.2.1" || pending[2].Current.Version != "v0.3.0" {
		t.Fatalf("third transition = %+v -> %+v, want v0.2.1 -> v0.3.0", pending[2].Previous, pending[2].Current)
	}
}

func TestRecordGraithBuildObservationFirstStartupRecordsBaselineOnly(t *testing.T) {
	h := newTestHarnessWithConfig(t, config.Default())
	h.sm.cfg.Orchestrator.Enabled = true
	detectedAt := time.Date(2026, time.July, 29, 18, 30, 0, 0, time.UTC)

	if err := h.sm.recordGraithBuildObservation(GraithBuildState{Version: "v0.3.0", CommitSHA: "canny"}, detectedAt); err != nil {
		t.Fatalf("recordGraithBuildObservation: %v", err)
	}

	if h.sm.state.GraithBuild == nil || h.sm.state.GraithBuild.Version != "v0.3.0" {
		t.Fatalf("GraithBuild = %+v, want first-start baseline", h.sm.state.GraithBuild)
	}

	if len(h.sm.state.PendingGraithUpdateNotifications) != 0 {
		t.Fatalf("first startup queued notifications: %+v", h.sm.state.PendingGraithUpdateNotifications)
	}
}

func TestRecordGraithBuildObservationSkipsNotificationWhenOrchestratorDisabled(t *testing.T) {
	h := newTestHarnessWithConfig(t, config.Default())
	h.sm.cfg.Orchestrator.Enabled = false
	detectedAt := time.Date(2026, time.July, 29, 18, 30, 0, 0, time.UTC)
	h.sm.state.GraithBuild = &GraithBuildState{Version: "v0.2.1", CommitSHA: "braw"}

	if err := h.sm.recordGraithBuildObservation(GraithBuildState{Version: "v0.3.0", CommitSHA: "canny"}, detectedAt); err != nil {
		t.Fatalf("recordGraithBuildObservation: %v", err)
	}

	if len(h.sm.state.PendingGraithUpdateNotifications) != 0 {
		t.Fatalf("disabled orchestrator queued notifications: %+v", h.sm.state.PendingGraithUpdateNotifications)
	}

	if h.sm.state.GraithBuild.Version != "v0.3.0" {
		t.Errorf("GraithBuild.Version = %q, want current version despite disabled orchestrator", h.sm.state.GraithBuild.Version)
	}
}

func TestDeliverPendingGraithUpdateNotificationPublishesSystemMessageAndDedupes(t *testing.T) {
	h := newTestHarnessWithConfig(t, config.Default())
	h.sm.cfg.Orchestrator.Enabled = true
	h.sm.state.Sessions["orch"] = &SessionState{
		ID:         "orch",
		Name:       "orchestrator",
		SystemKind: SystemKindOrchestrator,
		Status:     StatusRunning,
	}

	detectedAt := time.Date(2026, time.July, 29, 18, 30, 0, 0, time.UTC)
	notice := graithUpdateNotificationFor(
		GraithBuildState{Version: "v0.2.1", CommitSHA: "braw"},
		GraithBuildState{Version: "v0.3.0", CommitSHA: "canny"},
		detectedAt,
	)
	h.sm.state.PendingGraithUpdateNotifications = []GraithUpdateNotificationState{notice}

	h.sm.deliverPendingGraithUpdateNotifications(context.Background())

	if len(h.sm.state.PendingGraithUpdateNotifications) != 0 {
		t.Fatalf("pending notifications after delivery = %+v, want empty", h.sm.state.PendingGraithUpdateNotifications)
	}

	msgs, err := h.sm.messages.Read("inbox:orch", "", false, "")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if len(msgs) != 1 {
		t.Fatalf("messages = %d, want 1", len(msgs))
	}

	msg := msgs[0]
	if !msg.System {
		t.Error("System = false, want true")
	}

	if msg.ThreadID != notice.ID {
		t.Errorf("ThreadID = %q, want %q", msg.ThreadID, notice.ID)
	}

	loaded, err := LoadState(h.sm.paths.StateFile)
	if err != nil {
		t.Fatalf("LoadState after delivery: %v", err)
	}

	if len(loaded.PendingGraithUpdateNotifications) != 0 {
		t.Fatalf("persisted pending notifications after delivery = %+v, want empty", loaded.PendingGraithUpdateNotifications)
	}

	for _, want := range []string{
		"System event: Graith update detected",
		"Previous Graith version: v0.2.1",
		"New Graith version: v0.3.0",
		"Detected at: 2026-07-29T18:30:00Z",
		"Transition: upgrade",
		"Proactively suggest applicable",
	} {
		if !strings.Contains(msg.Body, want) {
			t.Errorf("message body missing %q:\n%s", want, msg.Body)
		}
	}

	h.sm.state.PendingGraithUpdateNotifications = []GraithUpdateNotificationState{notice}
	h.sm.deliverPendingGraithUpdateNotifications(context.Background())

	msgs, err = h.sm.messages.Read("inbox:orch", "", false, "")
	if err != nil {
		t.Fatalf("Read after duplicate delivery: %v", err)
	}

	if len(msgs) != 1 {
		t.Fatalf("messages after duplicate delivery = %d, want 1", len(msgs))
	}
}

func TestDeliverPendingGraithUpdateNotificationPublishesRepeatedVersionPair(t *testing.T) {
	h := newTestHarnessWithConfig(t, config.Default())
	h.sm.cfg.Orchestrator.Enabled = true
	h.sm.state.Sessions["orch"] = &SessionState{
		ID:         "orch",
		Name:       "orchestrator",
		SystemKind: SystemKindOrchestrator,
		Status:     StatusRunning,
	}

	observedAt := time.Date(2026, time.July, 29, 18, 0, 0, 0, time.UTC)
	first := graithUpdateNotificationFor(
		GraithBuildState{Version: "v0.2.1", CommitSHA: "braw", ObservedAt: observedAt},
		GraithBuildState{Version: "v0.3.0", CommitSHA: "canny"},
		observedAt.Add(time.Minute),
	)
	h.sm.state.PendingGraithUpdateNotifications = []GraithUpdateNotificationState{first}
	h.sm.deliverPendingGraithUpdateNotifications(context.Background())

	repeated := graithUpdateNotificationFor(
		GraithBuildState{Version: "v0.2.1", CommitSHA: "braw", ObservedAt: observedAt.Add(2 * time.Minute)},
		GraithBuildState{Version: "v0.3.0", CommitSHA: "canny"},
		observedAt.Add(3*time.Minute),
	)
	if repeated.ID == first.ID {
		t.Fatalf("repeated transition ID = %q, want distinct from first", repeated.ID)
	}

	h.sm.state.PendingGraithUpdateNotifications = []GraithUpdateNotificationState{repeated}
	h.sm.deliverPendingGraithUpdateNotifications(context.Background())

	msgs, err := h.sm.messages.Read("inbox:orch", "", false, "")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if len(msgs) != 2 {
		t.Fatalf("messages = %d, want both repeated transitions delivered", len(msgs))
	}

	if msgs[0].ThreadID == msgs[1].ThreadID {
		t.Fatalf("message thread IDs are both %q, want distinct transition events", msgs[0].ThreadID)
	}
}

func TestDeliverPendingGraithUpdateNotificationRetainsPendingWithoutMessageStore(t *testing.T) {
	h := newTestHarnessWithConfig(t, config.Default())
	h.sm.cfg.Orchestrator.Enabled = true
	h.sm.messages = nil
	h.sm.state.Sessions["orch"] = &SessionState{
		ID:         "orch",
		Name:       "orchestrator",
		SystemKind: SystemKindOrchestrator,
		Status:     StatusRunning,
	}

	notice := graithUpdateNotificationFor(
		GraithBuildState{Version: "v0.2.1", CommitSHA: "braw"},
		GraithBuildState{Version: "v0.3.0", CommitSHA: "canny"},
		time.Date(2026, time.July, 29, 18, 30, 0, 0, time.UTC),
	)
	h.sm.state.PendingGraithUpdateNotifications = []GraithUpdateNotificationState{notice}

	h.sm.deliverPendingGraithUpdateNotifications(context.Background())

	if len(h.sm.state.PendingGraithUpdateNotifications) != 1 {
		t.Fatalf("pending notifications = %d, want retained after delivery failure", len(h.sm.state.PendingGraithUpdateNotifications))
	}
}

func TestFormatGraithUpdateNotificationUnavailableMetadata(t *testing.T) {
	detectedAt := time.Date(2026, time.July, 29, 18, 30, 0, 0, time.UTC)
	notice := graithUpdateNotificationFor(
		GraithBuildState{Version: "dev", CommitSHA: "unknown"},
		GraithBuildState{Version: "", CommitSHA: "dreich"},
		detectedAt,
	)

	body := formatGraithUpdateNotification(notice)
	for _, want := range []string{
		"Previous Graith version: unavailable",
		"Previous Graith commit: unavailable",
		"New Graith version: unavailable",
		"New Graith commit: dreich",
		"Transition: version metadata unavailable",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("message body missing %q:\n%s", want, body)
		}
	}
}

func TestDeliverPendingGraithUpdateNotificationWaitsForOrchestrator(t *testing.T) {
	h := newTestHarnessWithConfig(t, config.Default())
	h.sm.cfg.Orchestrator.Enabled = true
	notice := graithUpdateNotificationFor(
		GraithBuildState{Version: "v0.2.1", CommitSHA: "braw"},
		GraithBuildState{Version: "v0.3.0", CommitSHA: "canny"},
		time.Date(2026, time.July, 29, 18, 30, 0, 0, time.UTC),
	)
	h.sm.state.PendingGraithUpdateNotifications = []GraithUpdateNotificationState{notice}

	h.sm.deliverPendingGraithUpdateNotifications(context.Background())

	if len(h.sm.state.PendingGraithUpdateNotifications) != 1 {
		t.Fatalf("pending notifications = %d, want retained until orchestrator exists", len(h.sm.state.PendingGraithUpdateNotifications))
	}

	msgs, err := h.sm.messages.Read("inbox:orch", "", false, "")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if len(msgs) != 0 {
		t.Fatalf("messages for missing orchestrator = %d, want 0", len(msgs))
	}
}
