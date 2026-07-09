package cli

import (
	"strings"
	"testing"
	"time"
)

// withStatusClearCov2 sets the package-global statusSummaryClear flag and
// restores it afterwards so these tests don't leak state into siblings.
func withStatusClearCov2(t *testing.T, doClear bool, fn func()) {
	t.Helper()

	prev := statusSummaryClear
	statusSummaryClear = doClear

	defer func() { statusSummaryClear = prev }()

	fn()
}

func TestParseTTLCov2Valid(t *testing.T) {
	d, err := parseTTL("10m")
	if err != nil {
		t.Fatalf("parseTTL: %v", err)
	}

	if d != 10*time.Minute {
		t.Errorf("got %v, want 10m", d)
	}
}

func TestParseTTLCov2Unparseable(t *testing.T) {
	_, err := parseTTL("dreich")
	if err == nil {
		t.Fatal("expected error for unparseable TTL")
	}

	if !strings.Contains(err.Error(), "invalid TTL") {
		t.Errorf("error %q should mention 'invalid TTL'", err)
	}
}

func TestParseTTLCov2NonPositive(t *testing.T) {
	for _, in := range []string{"0s", "-5m"} {
		if _, err := parseTTL(in); err == nil {
			t.Errorf("expected error for non-positive TTL %q", in)
		}
	}
}

// resolveStatusArgs only touches the client when it must resolve a session
// name; every branch exercised here returns before that, so a nil client is
// safe.
func TestResolveStatusArgsCov2ClearWithSession(t *testing.T) {
	t.Setenv("GRAITH_SESSION_ID", "braw-session")

	withStatusClearCov2(t, true, func() {
		id, text, err := resolveStatusArgs(nil, nil)
		if err != nil {
			t.Fatalf("clear inside session: %v", err)
		}

		if id != "braw-session" || text != "" {
			t.Errorf("got (%q, %q), want (braw-session, \"\")", id, text)
		}
	})
}

func TestResolveStatusArgsCov2ClearOutsideSessionNoArgs(t *testing.T) {
	t.Setenv("GRAITH_SESSION_ID", "")

	withStatusClearCov2(t, true, func() {
		if _, _, err := resolveStatusArgs(nil, nil); err == nil {
			t.Fatal("expected error clearing status with no session and no arg")
		}
	})
}

func TestResolveStatusArgsCov2MessageRequired(t *testing.T) {
	t.Setenv("GRAITH_SESSION_ID", "braw-session")

	withStatusClearCov2(t, false, func() {
		_, _, err := resolveStatusArgs(nil, nil)
		if err == nil || !strings.Contains(err.Error(), "message required") {
			t.Fatalf("expected 'message required' error, got %v", err)
		}
	})
}

func TestResolveStatusArgsCov2InsideSessionJoinsWords(t *testing.T) {
	t.Setenv("GRAITH_SESSION_ID", "braw-session")

	withStatusClearCov2(t, false, func() {
		id, text, err := resolveStatusArgs(nil, []string{"reviewing", "the", "wynd"})
		if err != nil {
			t.Fatalf("set status inside session: %v", err)
		}

		if id != "braw-session" {
			t.Errorf("session id = %q, want braw-session", id)
		}

		if text != "reviewing the wynd" {
			t.Errorf("text = %q, want joined words", text)
		}
	})
}

func TestResolveStatusArgsCov2OutsideSessionNeedsNameAndMessage(t *testing.T) {
	t.Setenv("GRAITH_SESSION_ID", "")

	withStatusClearCov2(t, false, func() {
		// One arg outside a session is ambiguous: it must ask for both a
		// session name and a message rather than treating the lone arg as text.
		_, _, err := resolveStatusArgs(nil, []string{"canny"})
		if err == nil || !strings.Contains(err.Error(), "session name and message required") {
			t.Fatalf("expected 'session name and message required', got %v", err)
		}
	})
}
