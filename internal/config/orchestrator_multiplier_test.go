package config

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestOrchestratorMultiplierRejectsNonFinite is the #1303 validation regression:
// a NaN or ±Inf geometric multiplier must be rejected at load and reload. NaN
// slips past every comparison in DelayForLevel, so the delay never caps and the
// retry collapses to the supervisor safety floor rather than the configured
// backoff.
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

// TestDelayForLevelFiniteWithNonFiniteMultiplier asserts DelayForLevel is robust
// even for a directly-constructed config carrying a non-finite or extreme
// multiplier: every level yields a positive, monotonic, capped delay rather than
// the NaN→0 collapse the supervisor floor had to paper over (#1303).
func TestDelayForLevelFiniteWithNonFiniteMultiplier(t *testing.T) {
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
