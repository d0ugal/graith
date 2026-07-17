package config

import (
	"errors"
	"fmt"
	"time"
)

const (
	ScenarioCleanupOff       = "off"
	ScenarioCleanupOnSuccess = "on_success"
	ScenarioCleanupAlways    = "always"
)

// ScenarioLifecycleConfig controls optional cleanup after a completion epoch.
// The zero value is deliberately disabled for backward compatibility.
type ScenarioLifecycleConfig struct {
	Cleanup string `json:"cleanup,omitempty" toml:"cleanup"`
	Delay   string `json:"delay,omitempty"   toml:"delay"`
}

func (c ScenarioLifecycleConfig) CleanupMode() string {
	if c.Cleanup == "" {
		return ScenarioCleanupOff
	}

	return c.Cleanup
}

func (c ScenarioLifecycleConfig) DelayDuration() time.Duration {
	if c.Delay == "" {
		return 0
	}

	d, _ := ParseDurationWithDays(c.Delay)

	return d
}

func ValidateScenarioLifecycle(c ScenarioLifecycleConfig) error {
	switch c.CleanupMode() {
	case ScenarioCleanupOff, ScenarioCleanupOnSuccess, ScenarioCleanupAlways:
	default:
		return fmt.Errorf("scenario.lifecycle.cleanup %q is invalid (want off, on_success, or always)", c.Cleanup)
	}

	if c.Delay != "" {
		d, err := ParseDurationWithDays(c.Delay)
		if err != nil {
			return fmt.Errorf("scenario.lifecycle.delay %q: %w", c.Delay, err)
		}

		if d < 0 {
			return errors.New("scenario.lifecycle.delay must not be negative")
		}

		if c.CleanupMode() == ScenarioCleanupOff {
			return errors.New("scenario.lifecycle.delay requires cleanup to be enabled")
		}
	}

	return nil
}
