package daemon

import (
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
)

func quietTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func baseNotifications() config.Notifications {
	return config.Notifications{Enabled: true}
}

func TestEvaluatePushGate_Disabled(t *testing.T) {
	res := evaluatePushGate(pushGateInput{
		cfg:      config.Notifications{Enabled: false},
		priority: config.NotifyPriorityNormal,
		now:      time.Now(),
	})
	if res.deliver {
		t.Fatal("disabled notifications should not deliver")
	}
}

func TestEvaluatePushGate_Coalesces(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)

	in := pushGateInput{
		cfg:            baseNotifications(),
		priority:       config.NotifyPriorityNormal,
		key:            "normal|graith|bide",
		now:            now,
		lastKey:        "normal|graith|bide",
		lastAt:         now.Add(-5 * time.Second),
		coalesceWindow: pushCoalesceWindow,
	}
	if res := evaluatePushGate(in); res.deliver {
		t.Fatal("identical notification within window should be coalesced")
	}

	// Outside the window it delivers.
	in.lastAt = now.Add(-2 * pushCoalesceWindow)
	if res := evaluatePushGate(in); !res.deliver {
		t.Fatal("identical notification outside window should deliver")
	}
}

func TestEvaluatePushGate_QuietHours(t *testing.T) {
	cfg := baseNotifications()
	cfg.QuietHoursStart = "22:00"
	cfg.QuietHoursEnd = "07:00"

	nightNormal := evaluatePushGate(pushGateInput{
		cfg: cfg, priority: config.NotifyPriorityNormal,
		now: time.Date(2026, 7, 11, 23, 0, 0, 0, time.UTC), coalesceWindow: pushCoalesceWindow,
	})
	if nightNormal.deliver {
		t.Fatal("normal notification in quiet hours should be suppressed")
	}

	// High priority bypasses quiet hours.
	nightHigh := evaluatePushGate(pushGateInput{
		cfg: cfg, priority: config.NotifyPriorityHigh,
		now: time.Date(2026, 7, 11, 23, 0, 0, 0, time.UTC), coalesceWindow: pushCoalesceWindow,
	})
	if !nightHigh.deliver {
		t.Fatal("high priority should bypass quiet hours")
	}
}

func TestEvaluatePushGate_RateLimit(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	cfg := baseNotifications()
	cfg.MaxPerHour = 2

	// Two recent deliveries already; a normal one is rate limited.
	log := []time.Time{now.Add(-10 * time.Minute), now.Add(-5 * time.Minute)}

	res := evaluatePushGate(pushGateInput{
		cfg: cfg, priority: config.NotifyPriorityNormal, now: now, log: log, coalesceWindow: pushCoalesceWindow,
	})
	if res.deliver {
		t.Fatal("normal notification over the hourly cap should be rate limited")
	}

	// High priority bypasses the cap and records its send.
	resHigh := evaluatePushGate(pushGateInput{
		cfg: cfg, priority: config.NotifyPriorityHigh, now: now, log: log, coalesceWindow: pushCoalesceWindow,
	})
	if !resHigh.deliver {
		t.Fatal("high priority should bypass the rate limit")
	}

	if len(resHigh.log) != 3 {
		t.Fatalf("high-priority send should be recorded in the window, got len %d", len(resHigh.log))
	}
}

func TestEvaluatePushGate_PrunesOldTimestamps(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	cfg := baseNotifications()
	cfg.MaxPerHour = 2

	// Two old (>1h) timestamps should be pruned, so a fresh normal send delivers.
	log := []time.Time{now.Add(-2 * time.Hour), now.Add(-90 * time.Minute)}

	res := evaluatePushGate(pushGateInput{
		cfg: cfg, priority: config.NotifyPriorityNormal, now: now, log: log, coalesceWindow: pushCoalesceWindow,
	})
	if !res.deliver {
		t.Fatal("stale timestamps should be pruned, letting the send through")
	}

	if len(res.log) != 1 {
		t.Fatalf("pruned window should contain only the new send, got len %d", len(res.log))
	}
}

// fakeDispatch captures dispatch calls for SendPushNotification tests.
type fakeDispatch struct {
	mu    sync.Mutex
	calls []pushNotification
	err   error
}

func (f *fakeDispatch) fn(backend, title, message, priority string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls = append(f.calls, pushNotification{Title: title, Message: message, Priority: priority})

	return f.err
}

func newPushSM(n config.Notifications, d func(string, string, string, string) error) *SessionManager {
	return &SessionManager{
		cfg:          &config.Config{Notifications: n},
		log:          quietTestLogger(),
		pushDispatch: d,
	}
}

func TestSendPushNotification_Delivers(t *testing.T) {
	fd := &fakeDispatch{}
	sm := newPushSM(baseNotifications(), fd.fn)

	ok, reason := sm.SendPushNotification(pushNotification{Message: "briefing ready"})
	if !ok {
		t.Fatalf("expected delivery, got suppressed: %s", reason)
	}

	if len(fd.calls) != 1 {
		t.Fatalf("expected 1 dispatch, got %d", len(fd.calls))
	}

	if fd.calls[0].Title != "graith" || fd.calls[0].Priority != config.NotifyPriorityNormal {
		t.Errorf("unexpected dispatch: %+v", fd.calls[0])
	}

	// Rolling window should now hold one entry.
	if len(sm.pushLog) != 1 {
		t.Errorf("expected pushLog len 1, got %d", len(sm.pushLog))
	}
}

func TestSendPushNotification_InvalidPriority(t *testing.T) {
	fd := &fakeDispatch{}
	sm := newPushSM(baseNotifications(), fd.fn)

	if ok, _ := sm.SendPushNotification(pushNotification{Message: "x", Priority: "screaming"}); ok {
		t.Fatal("invalid priority should not deliver")
	}

	if len(fd.calls) != 0 {
		t.Fatal("invalid priority should not dispatch")
	}
}

func TestSendPushNotification_EmptyMessage(t *testing.T) {
	fd := &fakeDispatch{}
	sm := newPushSM(baseNotifications(), fd.fn)

	if ok, _ := sm.SendPushNotification(pushNotification{Message: "   "}); ok {
		t.Fatal("empty message should not deliver")
	}
}

func TestSendPushNotification_DispatchError(t *testing.T) {
	fd := &fakeDispatch{err: fmt.Errorf("boom")}
	sm := newPushSM(baseNotifications(), fd.fn)

	ok, reason := sm.SendPushNotification(pushNotification{Message: "x"})
	if ok {
		t.Fatal("dispatch error should report not delivered")
	}

	if reason == "" {
		t.Fatal("expected a failure reason")
	}

	// A failed dispatch must not consume rate-limit budget or open a coalescing
	// window — the notification never reached the user, so a retry must be allowed.
	if len(sm.pushLog) != 0 {
		t.Errorf("failed dispatch should not record a rate-limit entry, got len %d", len(sm.pushLog))
	}

	if sm.lastPushKey != "" {
		t.Errorf("failed dispatch should not set a coalescing key, got %q", sm.lastPushKey)
	}

	// The retry (dispatch now succeeds) should go through.
	fd.err = nil
	if ok, reason := sm.SendPushNotification(pushNotification{Message: "x"}); !ok {
		t.Fatalf("retry after a failed dispatch should deliver, got suppressed: %s", reason)
	}
}

func TestSendPushNotification_Coalesced(t *testing.T) {
	fd := &fakeDispatch{}
	sm := newPushSM(baseNotifications(), fd.fn)

	if ok, _ := sm.SendPushNotification(pushNotification{Message: "same"}); !ok {
		t.Fatal("first send should deliver")
	}

	if ok, reason := sm.SendPushNotification(pushNotification{Message: "same"}); ok {
		t.Fatal("identical immediate resend should be coalesced")
	} else if reason == "" {
		t.Fatal("expected a coalescing reason")
	}

	if len(fd.calls) != 1 {
		t.Fatalf("expected exactly 1 dispatch after coalescing, got %d", len(fd.calls))
	}
}

func TestOsaQuote(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"hello", `"hello"`},
		{`say "hi"`, `"say \"hi\""`},
		{`back\slash`, `"back\\slash"`},
	}
	for _, c := range cases {
		if got := osaQuote(c.in); got != c.want {
			t.Errorf("osaQuote(%q) = %s, want %s", c.in, got, c.want)
		}
	}
}
