package config

import (
	"testing"
	"time"
)

func TestNormalizeNotifyPriority(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"", NotifyPriorityNormal, true},
		{"normal", NotifyPriorityNormal, true},
		{"LOW", NotifyPriorityLow, true},
		{"  High ", NotifyPriorityHigh, true},
		{"urgent", "", false},
		{"medium", "", false},
	}

	for _, c := range cases {
		got, ok := NormalizeNotifyPriority(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("NormalizeNotifyPriority(%q) = (%q, %v), want (%q, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestNotifyBackendAndMaxDefaults(t *testing.T) {
	var n Notifications
	if got := n.NotifyBackendName(); got != "macos" {
		t.Errorf("default backend = %q, want macos", got)
	}

	if got := n.MaxPerHourValue(); got != DefaultNotifyMaxPerHour {
		t.Errorf("default max_per_hour = %d, want %d", got, DefaultNotifyMaxPerHour)
	}

	n = Notifications{Backend: " command ", MaxPerHour: 3}
	if got := n.NotifyBackendName(); got != "command" {
		t.Errorf("backend = %q, want command (trimmed)", got)
	}

	if got := n.MaxPerHourValue(); got != 3 {
		t.Errorf("max_per_hour = %d, want 3", got)
	}
}

func clock(t *testing.T, hhmm string) time.Time {
	t.Helper()

	parsed, err := time.Parse("15:04", hhmm)
	if err != nil {
		t.Fatalf("parse %q: %v", hhmm, err)
	}
	// Anchor to an arbitrary date; only hour/minute matter to InQuietHours.
	return time.Date(2026, 7, 11, parsed.Hour(), parsed.Minute(), 0, 0, time.UTC)
}

func TestInQuietHours(t *testing.T) {
	cases := []struct {
		name       string
		start, end string
		at         string
		want       bool
	}{
		{"unset", "", "", "23:00", false},
		{"wrap inside late", "22:00", "07:00", "23:30", true},
		{"wrap inside early", "22:00", "07:00", "06:59", true},
		{"wrap outside", "22:00", "07:00", "12:00", false},
		{"wrap start boundary", "22:00", "07:00", "22:00", true},
		{"wrap end boundary exclusive", "22:00", "07:00", "07:00", false},
		{"same-day inside", "09:00", "17:00", "13:00", true},
		{"same-day before", "09:00", "17:00", "08:59", false},
		{"same-day end exclusive", "09:00", "17:00", "17:00", false},
		{"zero-length never", "10:00", "10:00", "10:00", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n := Notifications{QuietHoursStart: c.start, QuietHoursEnd: c.end}
			if got := n.InQuietHours(clock(t, c.at)); got != c.want {
				t.Errorf("InQuietHours(%s) with %s-%s = %v, want %v", c.at, c.start, c.end, got, c.want)
			}
		})
	}
}

func TestInQuietHoursMalformedIsFailOpen(t *testing.T) {
	// A malformed window (which Validate would reject at load) mutes nothing.
	n := Notifications{QuietHoursStart: "nope", QuietHoursEnd: "07:00"}
	if n.InQuietHours(clock(t, "23:00")) {
		t.Error("malformed quiet hours should not suppress (fail-open)")
	}
}

func TestNotificationsValidate(t *testing.T) {
	cases := []struct {
		name    string
		n       Notifications
		wantErr bool
	}{
		{"empty ok", Notifications{}, false},
		{"macos ok", Notifications{Backend: "macos"}, false},
		{"command with cmd ok", Notifications{Backend: "command", Command: "notify-send"}, false},
		{"command without cmd", Notifications{Backend: "command"}, true},
		{"unknown backend", Notifications{Backend: "ntfy"}, true},
		{"quiet hours ok", Notifications{QuietHoursStart: "22:00", QuietHoursEnd: "07:00"}, false},
		{"quiet hours half set", Notifications{QuietHoursStart: "22:00"}, true},
		{"quiet hours bad start", Notifications{QuietHoursStart: "25:00", QuietHoursEnd: "07:00"}, true},
		{"quiet hours bad end", Notifications{QuietHoursStart: "22:00", QuietHoursEnd: "7pm"}, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.n.Validate()
			if (err != nil) != c.wantErr {
				t.Errorf("Validate() err = %v, wantErr %v", err, c.wantErr)
			}
		})
	}
}

func TestParseClock(t *testing.T) {
	cases := []struct {
		in   string
		want int
		ok   bool
	}{
		{"00:00", 0, true},
		{"07:30", 450, true},
		{"23:59", 1439, true},
		{"9:00", 540, true}, // non-zero-padded hour is allowed
		{"24:00", 0, false},
		{"12:60", 0, false},
		{"noon", 0, false},
		{"-1:00", 0, false},
		{"22:00:59", 0, false}, // trailing field rejected
		{"09:00abc", 0, false}, // trailing junk rejected
		{"22", 0, false},       // no minute field
	}

	for _, c := range cases {
		got, ok := parseClock(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("parseClock(%q) = (%d, %v), want (%d, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestConfigValidateRejectsBadNotifications(t *testing.T) {
	cfg := Default()
	cfg.Notifications.Backend = "pushover"

	if err := cfg.Validate(); err == nil {
		t.Fatal("expected Validate to reject an unsupported notifications backend")
	}
}

func TestValidateTriggers_NotifyPriority(t *testing.T) {
	valid := schedTrigger("braw-notify", ScheduleConfig{Cron: "0 9 * * *"},
		ActionConfig{
			Type: ActionMessage, Body: "hi", Deliver: DeliverConfig{Inbox: "orchestrator"},
			NotifyOnComplete: true, NotifyMessage: "done", NotifyPriority: "high",
		})
	if errs := validateOne(valid, false); len(errs) != 0 {
		t.Fatalf("expected valid notify_priority, got %v", errs)
	}

	bad := schedTrigger("dreich-notify", ScheduleConfig{Cron: "0 9 * * *"},
		ActionConfig{
			Type: ActionMessage, Body: "hi", Deliver: DeliverConfig{Inbox: "orchestrator"},
			NotifyOnComplete: true, NotifyPriority: "screaming",
		})
	if errs := validateOne(bad, false); len(errs) == 0 {
		t.Fatal("expected invalid notify_priority to be rejected")
	}
}
