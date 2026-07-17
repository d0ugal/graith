package config

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestOrchestratorRestartRejectsNonPositiveDurations is the #1303 validation
// regression: a zero or negative initial/max/stable-reset duration must fail on
// load rather than silently falling back to the accessor default. A non-positive
// backoff would let a crash-looping orchestrator restart with no delay (a restart
// storm) and a non-positive stable_reset would reset the backoff after every run,
// pinning it at the shortest retry forever.
func TestOrchestratorRestartRejectsNonPositiveDurations(t *testing.T) {
	fields := []struct {
		name string
		body string
		want string
	}{
		{"zero initial_backoff", "initial_backoff = \"0s\"", "orchestrator.restart.initial_backoff"},
		{"negative initial_backoff", "initial_backoff = \"-2s\"", "orchestrator.restart.initial_backoff"},
		{"zero max_backoff", "max_backoff = \"0s\"", "orchestrator.restart.max_backoff"},
		{"negative max_backoff", "max_backoff = \"-1s\"", "orchestrator.restart.max_backoff"},
		{"zero stable_reset", "stable_reset = \"0s\"", "orchestrator.restart.stable_reset"},
		{"negative stable_reset", "stable_reset = \"-5s\"", "orchestrator.restart.stable_reset"},
	}

	for _, f := range fields {
		t.Run(f.name, func(t *testing.T) {
			dir := t.TempDir()
			cfgPath := filepath.Join(dir, "config.toml")
			body := "[orchestrator.restart]\n" + f.body + "\n"

			if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}

			// Load is the shared load/reload path (ReloadConfig → LoadOrDefault →
			// Load → Validate), so this covers the failed-reload requirement too.
			_, err := Load(cfgPath)
			if err == nil {
				t.Fatalf("Load accepted %s", f.name)
			}

			if !strings.Contains(err.Error(), f.want) {
				t.Fatalf("error %q does not mention %q", err, f.want)
			}
		})
	}
}

// TestOrchestratorRestartScheduleValidation covers the positivity and ordering
// rules for explicit schedule entries: each must be greater than zero, and the
// series must be nondecreasing so a later crash never restarts sooner than an
// earlier one.
func TestOrchestratorRestartScheduleValidation(t *testing.T) {
	cases := []struct {
		name     string
		schedule []string
		wantErr  string // empty means "must pass"
	}{
		{"positive nondecreasing passes", []string{"2s", "4s", "4s", "8s"}, ""},
		{"single entry passes", []string{"5s"}, ""},
		{"zero entry rejected", []string{"2s", "0s"}, "orchestrator.restart.schedule[1]"},
		{"negative entry rejected", []string{"-2s"}, "orchestrator.restart.schedule[0]"},
		{"decreasing entry rejected", []string{"8s", "4s"}, "orchestrator.restart.schedule[1]"},
		{"unparseable entry rejected", []string{"2s", "wut"}, "orchestrator.restart.schedule[1]"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{Orchestrator: OrchestratorConfig{Restart: OrchestratorRestartConfig{
				Schedule: tc.schedule,
			}}}

			err := c.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected validation error: %v", err)
				}

				return
			}

			if err == nil {
				t.Fatalf("Validate accepted schedule %v", tc.schedule)
			}

			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not mention %q", err, tc.wantErr)
			}
		})
	}
}

// TestOrchestratorRestartInitialExceedsMax rejects a geometric policy whose first
// delay already exceeds the cap — backoff would start above the maximum and every
// attempt would clamp to it, defeating the escalation the cap is meant to bound.
// The comparison uses effective defaults so a single explicit key that
// contradicts the other's default is caught rather than silently clamped at
// runtime. An explicit schedule makes the geometric knobs inert, so a
// contradiction there must not error.
func TestOrchestratorRestartInitialExceedsMax(t *testing.T) {
	contradictory := []struct {
		name    string
		restart OrchestratorRestartConfig
	}{
		{"both explicit", OrchestratorRestartConfig{InitialBackoff: "300s", MaxBackoff: "60s"}},
		{"only initial explicit vs default max", OrchestratorRestartConfig{InitialBackoff: "10m"}},
		{"only max explicit vs default initial", OrchestratorRestartConfig{MaxBackoff: "1s"}},
	}
	for _, tc := range contradictory {
		t.Run(tc.name+" rejected", func(t *testing.T) {
			c := &Config{Orchestrator: OrchestratorConfig{Restart: tc.restart}}

			err := c.Validate()
			if err == nil {
				t.Fatal("Validate accepted a contradictory initial/max pairing")
			}

			if !strings.Contains(err.Error(), "must not exceed max_backoff") {
				t.Fatalf("error %q does not mention the ordering rule", err)
			}
		})
	}

	coherent := []struct {
		name    string
		restart OrchestratorRestartConfig
	}{
		{"initial equal to max", OrchestratorRestartConfig{InitialBackoff: "60s", MaxBackoff: "60s"}},
		{"initial below max", OrchestratorRestartConfig{InitialBackoff: "2s", MaxBackoff: "300s"}},
		{"only initial within default max", OrchestratorRestartConfig{InitialBackoff: "30s"}},
		{"only max above default initial", OrchestratorRestartConfig{MaxBackoff: "600s"}},
		// An explicit schedule makes initial/max inert: the contradiction that
		// would fail in geometric mode is harmless and must be accepted.
		{"contradiction inert under explicit schedule", OrchestratorRestartConfig{
			Schedule: []string{"2s", "4s"}, InitialBackoff: "300s", MaxBackoff: "60s",
		}},
	}
	for _, tc := range coherent {
		t.Run(tc.name+" passes", func(t *testing.T) {
			c := &Config{Orchestrator: OrchestratorConfig{Restart: tc.restart}}
			if err := c.Validate(); err != nil {
				t.Fatalf("Validate rejected a coherent policy: %v", err)
			}
		})
	}
}

// TestOrchestratorMultiplierRejectsNonFinite is the #1303 multiplier regression:
// a NaN or ±Inf geometric multiplier must be rejected at load. NaN slips past
// every comparison in DelayForLevel, so the delay never caps and the retry
// collapses to the supervisor safety floor rather than the configured backoff.
func TestOrchestratorMultiplierRejectsNonFinite(t *testing.T) {
	cases := []struct {
		name string
		mult float64
	}{
		{"nan", math.NaN()},
		{"positive infinity", math.Inf(1)},
		{"negative infinity", math.Inf(-1)},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := &Config{Orchestrator: OrchestratorConfig{Restart: OrchestratorRestartConfig{
				InitialBackoff: "2s",
				MaxBackoff:     "300s",
				StableReset:    "60s",
				Multiplier:     c.mult,
			}}}

			err := cfg.Validate()
			if err == nil {
				t.Fatalf("Validate accepted a non-finite multiplier %v", c.mult)
			}

			if !strings.Contains(err.Error(), "orchestrator.restart.multiplier") {
				t.Fatalf("error %q does not mention the offending key", err)
			}
		})
	}

	// A finite value <= 1 stays legal (documented fall-back to the default).
	cfg := &Config{Orchestrator: OrchestratorConfig{Restart: OrchestratorRestartConfig{
		InitialBackoff: "2s", MaxBackoff: "300s", StableReset: "60s", Multiplier: 0.5,
	}}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate rejected a finite <=1 multiplier: %v", err)
	}
}

// TestLoadRejectsNonFiniteMultiplier drives the whole load path (which reload
// reuses via LoadOrDefault→Load→Validate) with TOML nan/inf/-inf literals.
func TestLoadRejectsNonFiniteMultiplier(t *testing.T) {
	for _, lit := range []string{"nan", "inf", "+inf", "-inf"} {
		t.Run(lit, func(t *testing.T) {
			dir := t.TempDir()
			cfgPath := filepath.Join(dir, "config.toml")
			body := "[orchestrator.restart]\nmultiplier = " + lit + "\n"

			if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}

			if _, err := Load(cfgPath); err == nil {
				t.Fatalf("Load accepted multiplier = %s", lit)
			} else if !strings.Contains(err.Error(), "multiplier") {
				t.Fatalf("error %q does not mention multiplier", err)
			}
		})
	}
}

// TestDelayForLevelStaysPositiveAndCapped asserts DelayForLevel is robust even
// for a directly-constructed config carrying a non-finite or extreme multiplier
// (Validate would have rejected it, but the accessor must not depend on that):
// every level yields a positive, monotonic, capped delay rather than the
// NaN→collapse the supervisor floor had to paper over (#1303).
func TestDelayForLevelStaysPositiveAndCapped(t *testing.T) {
	const maxDelay = 60 * time.Second

	mults := map[string]float64{
		"nan":            math.NaN(),
		"positive inf":   math.Inf(1),
		"negative inf":   math.Inf(-1),
		"finite extreme": 1e300,
	}

	for name, mult := range mults {
		t.Run(name, func(t *testing.T) {
			r := OrchestratorRestartConfig{
				InitialBackoff: "2s",
				MaxBackoff:     "60s",
				Multiplier:     mult,
			}

			var prev time.Duration

			for level := 0; level <= 12; level++ {
				d := r.DelayForLevel(level)

				if d <= 0 {
					t.Fatalf("level %d: delay %v must be positive", level, d)
				}

				if d > maxDelay {
					t.Fatalf("level %d: delay %v exceeds cap %v", level, d, maxDelay)
				}

				if d < prev {
					t.Fatalf("level %d: delay %v decreased from %v", level, d, prev)
				}

				prev = d
			}

			// The tail must saturate exactly at the cap.
			if got := r.DelayForLevel(50); got != maxDelay {
				t.Fatalf("far level delay = %v, want cap %v", got, maxDelay)
			}
		})
	}
}

// TestDelayForLevelNonPositiveConfigFallsBackPositive asserts that even a
// directly-constructed config with non-positive backoff strings (which Validate
// rejects) never yields a non-positive delay: the accessors fall back to their
// positive defaults so a restart is always scheduled with a real gap.
func TestDelayForLevelNonPositiveConfigFallsBackPositive(t *testing.T) {
	r := OrchestratorRestartConfig{
		InitialBackoff: "0s",
		MaxBackoff:     "-1s",
		Multiplier:     2,
	}

	for level := 0; level <= 5; level++ {
		if got := r.DelayForLevel(level); got <= 0 {
			t.Fatalf("level %d: delay %v must be positive despite non-positive config", level, got)
		}
	}
}
