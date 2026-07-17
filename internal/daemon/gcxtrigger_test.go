package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/tools"
)

type fakeGCX struct {
	schedules   [][]byte
	alerts      [][]byte
	scheduleErr []error
	alertErr    []error
	calls       [][]string
}

func (f *fakeGCX) run(_ context.Context, contextName string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{contextName}, args...))

	if slices.Contains(args, "schedules") {
		if len(f.scheduleErr) > 0 {
			err := f.scheduleErr[0]
			f.scheduleErr = f.scheduleErr[1:]

			if err != nil {
				return nil, err
			}
		}

		if len(f.schedules) == 0 {
			return nil, errors.New("unexpected schedules call")
		}

		out := f.schedules[0]
		f.schedules = f.schedules[1:]

		return out, nil
	}

	if slices.Contains(args, "alert-groups") {
		if len(f.alertErr) > 0 {
			err := f.alertErr[0]
			f.alertErr = f.alertErr[1:]

			if err != nil {
				return nil, err
			}
		}

		if len(f.alerts) == 0 {
			return nil, errors.New("unexpected alert-groups call")
		}

		out := f.alerts[0]
		f.alerts = f.alerts[1:]

		return out, nil
	}

	return nil, fmt.Errorf("unexpected gcx args: %v", args)
}

func gcxTestTrigger(gate bool) config.TriggerConfig {
	gcx := &config.GCXConfig{Context: "croft", Every: "1m", MaxAge: "24h", Limit: 20}
	if gate {
		gcx.OnCallUserID = "U-BRAW"
		gcx.ScheduleIDs = []string{"S-BRAW"}
	}

	return config.TriggerConfig{
		Name: "canny-oncall",
		GCX:  gcx,
		Action: config.ActionConfig{
			Type: config.ActionMessage,
			Body: "alert {gcx_event_id} {gcx_event_state}",
			Deliver: config.DeliverConfig{
				Topic: "blether",
			},
		},
		Policy: config.TriggerPolicy{RateLimit: "20/1h"},
	}
}

func gcxSchedulesJSON(t *testing.T, scheduleID string, users ...string) []byte {
	t.Helper()

	onCall := make([]map[string]string, 0, len(users))
	for _, user := range users {
		onCall = append(onCall, map[string]string{"pk": user})
	}

	out, err := json.Marshal([]map[string]any{{
		"metadata.name":    scheduleID,
		"spec.on_call_now": onCall,
	}})
	if err != nil {
		t.Fatal(err)
	}

	return out
}

func gcxAlertsJSON(t *testing.T, ids ...string) []byte {
	t.Helper()

	items := make([]map[string]string, 0, len(ids))
	for i, id := range ids {
		items = append(items, map[string]string{
			"metadata.name":             id,
			"spec.integration.id":       "I-BRAW",
			"spec.permalinks.web":       "https://grafana.example.invalid/a/" + id,
			"spec.team.id":              "T-BRAW",
			"status.state":              "firing",
			"status.timestamps.started": fmt.Sprintf("2026-07-17T12:%02d:00Z", i),
		})
	}

	out, err := json.Marshal(map[string]any{"items": items})
	if err != nil {
		t.Fatal(err)
	}

	return out
}

func TestPollGCXOnCall(t *testing.T) {
	cfg := &config.GCXConfig{Context: "croft", OnCallUserID: "U-BRAW", ScheduleIDs: []string{"S-BRAW"}}

	t.Run("on call", func(t *testing.T) {
		fake := &fakeGCX{schedules: [][]byte{gcxSchedulesJSON(t, "S-BRAW", "U-BRAW")}}

		onCall, err := pollGCXOnCall(t.Context(), cfg, fake.run)
		if err != nil || !onCall {
			t.Fatalf("onCall=%v err=%v", onCall, err)
		}
	})

	t.Run("somebody else", func(t *testing.T) {
		fake := &fakeGCX{schedules: [][]byte{gcxSchedulesJSON(t, "S-BRAW", "U-THRAWN")}}

		onCall, err := pollGCXOnCall(t.Context(), cfg, fake.run)
		if err != nil || onCall {
			t.Fatalf("onCall=%v err=%v", onCall, err)
		}
	})

	t.Run("missing schedule fails closed", func(t *testing.T) {
		fake := &fakeGCX{schedules: [][]byte{gcxSchedulesJSON(t, "S-OTHER", "U-BRAW")}}
		if _, err := pollGCXOnCall(t.Context(), cfg, fake.run); err == nil || !strings.Contains(err.Error(), "S-BRAW") {
			t.Fatalf("expected missing-schedule error, got %v", err)
		}
	})

	t.Run("no gate avoids gcx call", func(t *testing.T) {
		fake := &fakeGCX{}

		onCall, err := pollGCXOnCall(t.Context(), &config.GCXConfig{Context: "croft"}, fake.run)
		if err != nil || !onCall || len(fake.calls) != 0 {
			t.Fatalf("onCall=%v err=%v calls=%v", onCall, err, fake.calls)
		}
	})
}

func TestPollGCXAlertGroups(t *testing.T) {
	cfg := &config.GCXConfig{
		Context: "croft", States: []string{"firing", "acknowledged"}, TeamIDs: []string{"T-BRAW"},
		IntegrationIDs: []string{"I-BRAW"}, MaxAge: "12h", Limit: 25,
	}
	fake := &fakeGCX{alerts: [][]byte{gcxAlertsJSON(t, "AG-BRAW")}}

	events, err := pollGCXAlertGroups(t.Context(), cfg, fake.run)
	if err != nil {
		t.Fatal(err)
	}

	if len(events) != 1 || events[0].ID != "AG-BRAW" || events[0].TeamID != "T-BRAW" || events[0].Kind != config.GCXEventOnCallAlertGroup {
		t.Fatalf("events = %+v", events)
	}

	joined := strings.Join(fake.calls[0], " ")
	for _, want := range []string{"croft", "--state firing", "--state acknowledged", "--team T-BRAW", "--integration I-BRAW", "--max-age 12h", "--limit 25"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q: %s", want, joined)
		}
	}
}

func TestGCXTriggerPrimeThenFireNewEvent(t *testing.T) {
	trig := gcxTestTrigger(false)
	sm := newTriggerTestSM(t, trig)
	ms := withMsgStore(t, sm)
	sm.reconcileGCXBindings(sm.allTriggers(), time.Now())

	prime := &fakeGCX{alerts: [][]byte{gcxAlertsJSON(t, "AG-BRAW")}}
	sm.pollGCXTrigger(t.Context(), trig.Name, prime.run)

	if messages, _ := ms.Read("blether", "reader", false, ""); len(messages) != 0 {
		t.Fatalf("prime dispatched %d message(s)", len(messages))
	}

	rt := sm.getTriggerRuntime(trig.Name)
	if rt.LastGCXPollAt == nil || len(rt.GCXSeen) != 1 {
		t.Fatalf("prime cursor = %+v", rt)
	}

	next := &fakeGCX{alerts: [][]byte{gcxAlertsJSON(t, "AG-BRAW", "AG-CANNY")}}
	sm.pollGCXTrigger(t.Context(), trig.Name, next.run)

	messages, err := ms.Read("blether", "reader", false, "")
	if err != nil {
		t.Fatal(err)
	}

	if len(messages) != 1 || !strings.Contains(messages[0].Body, "AG-CANNY") {
		t.Fatalf("messages = %+v", messages)
	}

	if rt := sm.getTriggerRuntime(trig.Name); rt.RunCount != 1 || len(rt.GCXSeen) != 2 {
		t.Fatalf("runtime after event = %+v", rt)
	}
}

func TestGCXTriggerOffCallReprimesAtHandoff(t *testing.T) {
	trig := gcxTestTrigger(true)
	sm := newTriggerTestSM(t, trig)
	ms := withMsgStore(t, sm)
	sm.reconcileGCXBindings(sm.allTriggers(), time.Now())

	prime := &fakeGCX{
		schedules: [][]byte{gcxSchedulesJSON(t, "S-BRAW", "U-BRAW")},
		alerts:    [][]byte{gcxAlertsJSON(t, "AG-BRAW")},
	}
	sm.pollGCXTrigger(t.Context(), trig.Name, prime.run)

	offCall := &fakeGCX{schedules: [][]byte{gcxSchedulesJSON(t, "S-BRAW", "U-THRAWN")}}
	sm.pollGCXTrigger(t.Context(), trig.Name, offCall.run)

	if len(offCall.calls) != 1 || slices.Contains(offCall.calls[0], "alert-groups") {
		t.Fatalf("off-call poll queried alerts: %v", offCall.calls)
	}

	handoff := &fakeGCX{
		schedules: [][]byte{gcxSchedulesJSON(t, "S-BRAW", "U-BRAW")},
		alerts:    [][]byte{gcxAlertsJSON(t, "AG-BRAW", "AG-CANNY")},
	}
	sm.pollGCXTrigger(t.Context(), trig.Name, handoff.run)

	if messages, _ := ms.Read("blether", "reader", false, ""); len(messages) != 0 {
		t.Fatalf("handoff baseline dispatched %d message(s)", len(messages))
	}

	newAlert := &fakeGCX{
		schedules: [][]byte{
			gcxSchedulesJSON(t, "S-BRAW", "U-BRAW"),
			gcxSchedulesJSON(t, "S-BRAW", "U-BRAW"),
		},
		alerts: [][]byte{gcxAlertsJSON(t, "AG-BRAW", "AG-CANNY", "AG-DREICH")},
	}
	sm.pollGCXTrigger(t.Context(), trig.Name, newAlert.run)

	messages, _ := ms.Read("blether", "reader", false, "")
	if len(messages) != 1 || !strings.Contains(messages[0].Body, "AG-DREICH") {
		t.Fatalf("post-handoff messages = %+v", messages)
	}
}

func TestGCXTriggerRechecksGateBeforeDispatch(t *testing.T) {
	trig := gcxTestTrigger(true)
	sm := newTriggerTestSM(t, trig)
	ms := withMsgStore(t, sm)
	sm.reconcileGCXBindings(sm.allTriggers(), time.Now())

	prime := &fakeGCX{
		schedules: [][]byte{gcxSchedulesJSON(t, "S-BRAW", "U-BRAW")},
		alerts:    [][]byte{gcxAlertsJSON(t, "AG-BRAW")},
	}
	sm.pollGCXTrigger(t.Context(), trig.Name, prime.run)

	handoffDuringRead := &fakeGCX{
		schedules: [][]byte{
			gcxSchedulesJSON(t, "S-BRAW", "U-BRAW"),
			gcxSchedulesJSON(t, "S-BRAW", "U-THRAWN"),
		},
		alerts: [][]byte{gcxAlertsJSON(t, "AG-BRAW", "AG-CANNY")},
	}
	sm.pollGCXTrigger(t.Context(), trig.Name, handoffDuringRead.run)

	if messages, _ := ms.Read("blether", "reader", false, ""); len(messages) != 0 {
		t.Fatalf("handoff race dispatched: %+v", messages)
	}

	if rt := sm.getTriggerRuntime(trig.Name); len(rt.GCXSeen) != 1 {
		t.Fatalf("handoff race advanced cursor: %+v", rt.GCXSeen)
	}
}

func TestGCXTriggerRestartCatchUpPolicy(t *testing.T) {
	for _, tc := range []struct {
		name      string
		catchUp   bool
		wantFires int
	}{
		{name: "default primes after restart", catchUp: false, wantFires: 0},
		{name: "catch up resumes cursor", catchUp: true, wantFires: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			trig := gcxTestTrigger(false)
			trig.Policy.CatchUp = tc.catchUp
			sm := newTriggerTestSM(t, trig)
			ms := withMsgStore(t, sm)
			sm.reconcileGCXBindings(sm.allTriggers(), time.Now())

			fp := triggerFingerprint(&trig)
			lastPoll := time.Now().Add(-time.Minute)
			sm.putTriggerRuntime(trig.Name, &TriggerRuntimeState{
				Name: trig.Name, Fingerprint: fp, LastGCXPollAt: &lastPoll,
				GCXSeen: map[string]time.Time{"AG-BRAW": lastPoll},
			})

			// A daemon restart loses only process-local bindings; durable state stays.
			sm.triggers = newTriggerState()
			sm.reconcileGCXBindings(sm.allTriggers(), time.Now())

			fake := &fakeGCX{alerts: [][]byte{gcxAlertsJSON(t, "AG-BRAW", "AG-CANNY")}}
			sm.pollGCXTrigger(t.Context(), trig.Name, fake.run)

			messages, _ := ms.Read("blether", "reader", false, "")
			if len(messages) != tc.wantFires {
				t.Fatalf("fires=%d want=%d messages=%+v", len(messages), tc.wantFires, messages)
			}
		})
	}
}

func TestGCXTriggerReaddedDefinitionPrimes(t *testing.T) {
	trig := gcxTestTrigger(false)
	trig.Policy.CatchUp = true
	sm := newTriggerTestSM(t, trig)
	sm.reconcileGCXBindings(sm.allTriggers(), time.Now())

	lastPoll := time.Now().Add(-time.Minute)
	sm.putTriggerRuntime(trig.Name, &TriggerRuntimeState{
		Name: trig.Name, Fingerprint: triggerFingerprint(&trig), LastGCXPollAt: &lastPoll,
		GCXSeen: map[string]time.Time{"AG-BRAW": lastPoll},
	})

	sm.cfg.Triggers = nil
	sm.reconcileGCXBindings(nil, time.Now())

	if rt := sm.getTriggerRuntime(trig.Name); rt.LastGCXPollAt != nil || len(rt.GCXSeen) != 0 {
		t.Fatalf("removed source retained cursor: %+v", rt)
	}

	sm.cfg.Triggers = []config.TriggerConfig{trig}
	sm.reconcileGCXBindings(sm.allTriggers(), time.Now())
	sm.triggers.mu.Lock()
	prime := sm.triggers.gcxBindings[trig.Name].prime
	sm.triggers.mu.Unlock()

	if !prime {
		t.Fatal("re-added source should prime even with catch_up=true")
	}
}

func TestGCXGateErrorForcesNextPrime(t *testing.T) {
	trig := gcxTestTrigger(true)
	sm := newTriggerTestSM(t, trig)
	ms := withMsgStore(t, sm)
	sm.reconcileGCXBindings(sm.allTriggers(), time.Now())

	prime := &fakeGCX{
		schedules: [][]byte{gcxSchedulesJSON(t, "S-BRAW", "U-BRAW")},
		alerts:    [][]byte{gcxAlertsJSON(t, "AG-BRAW")},
	}
	sm.pollGCXTrigger(t.Context(), trig.Name, prime.run)

	failedGate := &fakeGCX{scheduleErr: []error{errors.New("temporary failure")}}
	sm.pollGCXTrigger(t.Context(), trig.Name, failedGate.run)

	recovered := &fakeGCX{
		schedules: [][]byte{gcxSchedulesJSON(t, "S-BRAW", "U-BRAW")},
		alerts:    [][]byte{gcxAlertsJSON(t, "AG-BRAW", "AG-CANNY")},
	}
	sm.pollGCXTrigger(t.Context(), trig.Name, recovered.run)

	if messages, _ := ms.Read("blether", "reader", false, ""); len(messages) != 0 {
		t.Fatalf("gate recovery replayed uncertain backlog: %+v", messages)
	}
}

func TestGCXTriggerTruncationAndSaveFailureSuppressDispatch(t *testing.T) {
	t.Run("truncation", func(t *testing.T) {
		trig := gcxTestTrigger(false)
		trig.GCX.Limit = 2
		sm := newTriggerTestSM(t, trig)
		ms := withMsgStore(t, sm)
		sm.reconcileGCXBindings(sm.allTriggers(), time.Now())

		fake := &fakeGCX{alerts: [][]byte{gcxAlertsJSON(t, "AG-BRAW", "AG-CANNY")}}
		sm.pollGCXTrigger(t.Context(), trig.Name, fake.run)

		if messages, _ := ms.Read("blether", "reader", false, ""); len(messages) != 0 {
			t.Fatalf("truncated poll dispatched: %+v", messages)
		}

		if rt := sm.getTriggerRuntime(trig.Name); rt.LastError == "" || len(rt.GCXSeen) != 0 {
			t.Fatalf("truncation runtime = %+v", rt)
		}
	})

	t.Run("cursor save", func(t *testing.T) {
		trig := gcxTestTrigger(false)
		trig.Policy.CatchUp = true
		sm := newTriggerTestSM(t, trig)
		ms := withMsgStore(t, sm)
		sm.reconcileGCXBindings(sm.allTriggers(), time.Now())

		lastPoll := time.Now().Add(-time.Minute)
		sm.putTriggerRuntime(trig.Name, &TriggerRuntimeState{
			Name: trig.Name, Fingerprint: triggerFingerprint(&trig), LastGCXPollAt: &lastPoll,
			GCXSeen: map[string]time.Time{"AG-BRAW": lastPoll},
		})
		sm.triggers = newTriggerState()
		sm.reconcileGCXBindings(sm.allTriggers(), time.Now())
		// A directory cannot be atomically replaced with the state file.
		sm.paths.StateFile = filepath.Dir(sm.paths.StateFile)

		fake := &fakeGCX{alerts: [][]byte{gcxAlertsJSON(t, "AG-BRAW", "AG-CANNY")}}
		sm.pollGCXTrigger(t.Context(), trig.Name, fake.run)

		if messages, _ := ms.Read("blether", "reader", false, ""); len(messages) != 0 {
			t.Fatalf("failed cursor save dispatched: %+v", messages)
		}
	})
}

func TestPlanGCXSnapshotPrunesOldCursor(t *testing.T) {
	trig := gcxTestTrigger(false)
	sm := newTriggerTestSM(t, trig)
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	fp := triggerFingerprint(&trig)
	sm.putTriggerRuntime(trig.Name, &TriggerRuntimeState{
		Name: trig.Name, Fingerprint: fp,
		GCXSeen: map[string]time.Time{
			"AG-AULD": now.Add(-2 * time.Hour),
			"AG-BRAW": now.Add(-time.Minute),
		},
	})

	newEvents, seen, ok := sm.planGCXSnapshot(trig.Name, fp, []gcxEvent{{ID: "AG-CANNY"}}, now, time.Hour, false)
	if !ok || len(newEvents) != 1 || len(seen) != 2 {
		t.Fatalf("new=%+v seen=%+v ok=%v", newEvents, seen, ok)
	}

	if _, exists := seen["AG-AULD"]; exists {
		t.Fatal("old cursor entry was not pruned")
	}
}

func TestRunGCXWithTimeout(t *testing.T) {
	cfg := &config.GCXConfig{Context: "croft", Timeout: "1ms"}
	runner := func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}

	if _, err := runGCXWithTimeout(t.Context(), cfg, runner, "irm"); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout, got %v", err)
	}
}

func TestRunGCXCommandUsesConfiguredTool(t *testing.T) {
	t.Cleanup(tools.Reset)
	dir := t.TempDir()

	bin := filepath.Join(dir, "braw-gcx")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\"\n"), 0o755); err != nil { //nolint:gosec // executable test stub
		t.Fatal(err)
	}

	tools.Configure(tools.Config{GCX: bin})

	out, err := runGCXCommand(t.Context(), "croft", "irm", "oncall", "schedules", "list")
	if err != nil {
		t.Fatal(err)
	}

	got := strings.TrimSpace(string(out))
	if got != "--context croft --no-color irm oncall schedules list" {
		t.Fatalf("args = %q", got)
	}
}
