package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestParseDurationWithDaysTrimsWhitespace pins the single-normalization-point
// contract for issue #1321: the shared parser trims leading/trailing whitespace
// so a padded value parses identically wherever it is read, closing the
// validation/accessor split (validation pre-trims; accessors previously did
// not).
func TestParseDurationWithDaysTrimsWhitespace(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"12h ", 12 * time.Hour},
		{" 12h", 12 * time.Hour},
		{"  12h  ", 12 * time.Hour},
		{"7d ", 7 * 24 * time.Hour},
		{" 7d", 7 * 24 * time.Hour},
		{"\t30m\n", 30 * time.Minute},
		{" 7d12h ", 7*24*time.Hour + 12*time.Hour},
	}

	for _, c := range cases {
		got, err := ParseDurationWithDays(c.in)
		if err != nil {
			t.Errorf("ParseDurationWithDays(%q) unexpected error: %v", c.in, err)
			continue
		}

		if got != c.want {
			t.Errorf("ParseDurationWithDays(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestDurationAccessorsWhitespaceConsistency asserts that every duration accessor
// backed by ParseDurationWithDays returns the intended value for a
// whitespace-padded input rather than silently collapsing to its zero/default.
// Before #1321 the accessors parsed the untrimmed string and fell back; the most
// dangerous was messages.max_age, whose fallback (0) means "retain forever".
func TestDurationAccessorsWhitespaceConsistency(t *testing.T) {
	if got := (Messages{MaxAge: "12h "}).MaxAgeDuration(); got != 12*time.Hour {
		t.Errorf("MaxAgeDuration(%q) = %v, want 12h (retain-forever fallback would be 0)", "12h ", got)
	}

	if got := (GCConfig{OrphanMinAge: " 6h"}).OrphanMinAgeDuration(); got != 6*time.Hour {
		t.Errorf("OrphanMinAgeDuration(%q) = %v, want 6h", " 6h", got)
	}

	if got := (Delete{Retention: "3d "}).RetentionDuration(); got != 3*24*time.Hour {
		t.Errorf("RetentionDuration(%q) = %v, want 72h", "3d ", got)
	}

	if got := (Approvals{CommandTimeout: " 9s "}).CommandTimeoutDuration(); got != 9*time.Second {
		t.Errorf("CommandTimeoutDuration(%q) = %v, want 9s", " 9s ", got)
	}

	if got := (StatusConfig{TTL: "2m "}).TTLDuration(); got != 2*time.Minute {
		t.Errorf("TTLDuration(%q) = %v, want 2m", "2m ", got)
	}
}

// TestLoadReloadWhitespaceMaxAge is the load/reload regression for #1321: a
// whitespace-padded max_age must validate at load, and the accessor must then
// return the finite window rather than 0 (retain forever). Reload reruns the
// same Load→Validate path, so a value that loads cleanly reloads identically.
func TestLoadReloadWhitespaceMaxAge(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")

	if err := os.WriteFile(cfgPath, []byte("[messages]\nmax_age = \"12h \"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load rejected whitespace-padded max_age: %v", err)
	}

	if got := cfg.Messages.MaxAgeDuration(); got != 12*time.Hour {
		t.Fatalf("MaxAgeDuration() = %v, want 12h (a padded value must not collapse to retain-forever)", got)
	}

	// Reload path: re-loading the same file must succeed and yield the same
	// finite window (the shared parser guarantees load and accessor agree).
	reloaded, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("reload rejected whitespace-padded max_age: %v", err)
	}

	if got := reloaded.Messages.MaxAgeDuration(); got != 12*time.Hour {
		t.Fatalf("reloaded MaxAgeDuration() = %v, want 12h", got)
	}
}
