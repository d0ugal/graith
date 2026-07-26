package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/protocol"
)

func TestTriggerStateLabel(t *testing.T) {
	cases := []struct {
		rec        protocol.TriggerRecord
		withConfig bool
		want       string
	}{
		{protocol.TriggerRecord{Enabled: true}, false, "enabled"},
		{protocol.TriggerRecord{Enabled: true, Paused: true}, false, "paused"},
		{protocol.TriggerRecord{Enabled: false}, false, "disabled"},
		{protocol.TriggerRecord{Enabled: false}, true, "disabled (config)"},
	}
	for _, tc := range cases {
		if got := triggerStateLabel(tc.rec, tc.withConfig); got != tc.want {
			t.Errorf("triggerStateLabel(%+v, %v) = %q, want %q", tc.rec, tc.withConfig, got, tc.want)
		}
	}
}

func TestRenderTriggerList(t *testing.T) {
	var buf bytes.Buffer
	renderTriggerList(&buf, nil)

	if !strings.Contains(buf.String(), "No triggers configured") {
		t.Errorf("empty list: %q", buf.String())
	}

	buf.Reset()
	renderTriggerList(&buf, []protocol.TriggerRecord{
		{Name: "braw", Source: "schedule", Action: "message", Schedule: "0 9 * * *", Enabled: true, RunCount: 3},
		{Name: "canny", Source: "watch", Action: "session", WatchScope: "role:implementer", Enabled: true, Paused: true},
		{Name: "dreich", Source: "gcx", Action: "session", GCXScope: "oncall_alert_group every 1m", Enabled: true},
	})
	out := buf.String()

	for _, want := range []string{"NAME", "braw", "schedule", "0 9 * * *", "canny", "watch", "role:implementer", "paused", "dreich", "gcx", "oncall_alert_group"} {
		if !strings.Contains(out, want) {
			t.Errorf("list output missing %q:\n%s", want, out)
		}
	}
}

func TestRenderTriggerStatus(t *testing.T) {
	var buf bytes.Buffer
	renderTriggerStatus(&buf, protocol.TriggerRecord{
		Name: "braw", Source: "schedule", Action: "message", Schedule: "@daily",
		Enabled: true, NextFire: "2026-07-12T09:00:00Z", RunCount: 5,
		LastRun: "2026-07-11T09:00:00Z", LastResult: "published",
	})
	out := buf.String()

	for _, want := range []string{"Trigger: braw", "Schedule: @daily", "Next fire:", "State: enabled", "Runs: 5", "published"} {
		if !strings.Contains(out, want) {
			t.Errorf("status output missing %q:\n%s", want, out)
		}
	}

	buf.Reset()
	renderTriggerStatus(&buf, protocol.TriggerRecord{
		Name: "canny", Source: "watch", Action: "command", WatchScope: "repo:/croft",
		Enabled: true, Bindings: 2, Degraded: "watcher.Add failed",
		DegradedRetryCount: 2, DegradedRetryAt: "2026-07-15T10:00:00Z", LastError: "boom",
		BindingsDetail: []protocol.TriggerBindingDetail{
			{
				SessionID:      "braw",
				SessionName:    "braw-runner",
				WorktreePath:   "/work/croft-a",
				State:          "debouncing",
				PendingChanges: 3,
				DebounceUntil:  "2026-07-15T09:59:00Z",
			},
			{
				SessionID:          "dreich",
				SessionName:        "dreich-runner",
				WorktreePath:       "/work/croft-a",
				State:              "degraded",
				LastResult:         "skipped",
				LastError:          "action already\nin flight",
				Degraded:           "watcher.Add failed",
				DegradedRetryCount: 2,
				DegradedRetryAt:    "2026-07-15T10:00:00Z",
			},
		},
	})
	out = buf.String()

	for _, want := range []string{
		"Watch: repo:/croft", "2 live binding", "SESSION", "WORKTREE",
		"braw-runner (braw)", "/work/croft-a", "debouncing", "3", "2026-07-15T09:59:00Z",
		"dreich-runner (dreich)", "skipped: action already in flight", "degraded: watcher.Add failed", "2026-07-15T10:00:00Z (2)",
		"Degraded:", "Next retry: 2026-07-15T10:00:00Z", "2 attempt", "Last error: boom",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("watch status output missing %q:\n%s", want, out)
		}
	}

	buf.Reset()
	renderTriggerStatus(&buf, protocol.TriggerRecord{
		Name: "dreich", Source: "gcx", Action: "session", GCXScope: "oncall_alert_group every 1m",
		Enabled: true, NextPoll: "2026-07-17T12:00:00Z",
	})

	out = buf.String()
	for _, want := range []string{"GCX: oncall_alert_group", "Next poll: 2026-07-17T12:00:00Z"} {
		if !strings.Contains(out, want) {
			t.Errorf("gcx status output missing %q:\n%s", want, out)
		}
	}
}
