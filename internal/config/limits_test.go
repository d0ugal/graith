package config

import (
	"testing"
	"time"
)

// TestHeadlessLimitAccessors exercises the empty/invalid/valid paths of the
// [headless] processing-limit accessors (issue #1250), mirroring the fail-safe
// pattern of the other accessors: a zero/non-positive/unparseable value keeps
// the default.
func TestHeadlessLimitAccessors(t *testing.T) {
	t.Run("max_line_bytes", func(t *testing.T) {
		cases := []struct {
			in   int
			want int
		}{
			{0, HeadlessMaxLineBytesDefault},
			{-1, HeadlessMaxLineBytesDefault},
			{4096, 4096},
		}
		for _, c := range cases {
			if got := (HeadlessConfig{MaxLineBytes: c.in}).MaxLineBytesOrDefault(); got != c.want {
				t.Errorf("MaxLineBytesOrDefault(%d) = %d, want %d", c.in, got, c.want)
			}
		}
	})

	t.Run("preview_bytes", func(t *testing.T) {
		if got := (HeadlessConfig{}).PreviewBytesOrDefault(); got != HeadlessPreviewBytesDefault {
			t.Errorf("PreviewBytesOrDefault() = %d, want %d", got, HeadlessPreviewBytesDefault)
		}

		if got := (HeadlessConfig{PreviewBytes: 999}).PreviewBytesOrDefault(); got != 999 {
			t.Errorf("PreviewBytesOrDefault(999) = %d, want 999", got)
		}
	})

	t.Run("control_timeout", func(t *testing.T) {
		cases := []struct {
			in   string
			want time.Duration
		}{
			{"", HeadlessControlTimeoutDefault},
			{"dreich", HeadlessControlTimeoutDefault},
			{"0", HeadlessControlTimeoutDefault},
			{"-5s", HeadlessControlTimeoutDefault},
			{"45s", 45 * time.Second},
		}
		for _, c := range cases {
			if got := (HeadlessConfig{ControlTimeout: c.in}).ControlTimeoutDuration(); got != c.want {
				t.Errorf("ControlTimeoutDuration(%q) = %v, want %v", c.in, got, c.want)
			}
		}
	})

	t.Run("interrupt_timeout", func(t *testing.T) {
		if got := (HeadlessConfig{}).InterruptTimeoutDuration(); got != HeadlessInterruptTimeoutDefault {
			t.Errorf("InterruptTimeoutDuration() = %v, want %v", got, HeadlessInterruptTimeoutDefault)
		}

		if got := (HeadlessConfig{InterruptTimeout: "2s"}).InterruptTimeoutDuration(); got != 2*time.Second {
			t.Errorf("InterruptTimeoutDuration(2s) = %v, want 2s", got)
		}
	})
}

// TestMigrationHealthWindowDuration exercises the [migration] health_window
// accessor's fail-safe paths (issue #1250).
func TestMigrationHealthWindowDuration(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"", MigrationHealthWindowDefault},
		{"blether", MigrationHealthWindowDefault},
		{"0", MigrationHealthWindowDefault},
		{"-1s", MigrationHealthWindowDefault},
		{"3s", 3 * time.Second},
	}
	for _, c := range cases {
		if got := (MigrationConfig{HealthWindow: c.in}).HealthWindowDuration(); got != c.want {
			t.Errorf("HealthWindowDuration(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestTranscriptLimitAccessors exercises the [transcript] byte-size accessors'
// zero/negative/positive paths (issue #1250).
func TestTranscriptLimitAccessors(t *testing.T) {
	cases := []struct {
		name string
		zero int
		set  int
	}{
		{"max_context_bytes", TranscriptMaxContextBytesDefault, 1000},
		{"max_tool_output_bytes", TranscriptMaxToolOutputBytesDefault, 2000},
		{"max_line_bytes", TranscriptMaxLineBytesDefault, 3000},
		{"max_metadata_line_bytes", TranscriptMaxMetadataBytesDefault, 4000},
	}

	get := func(name string, tc TranscriptConfig) int {
		switch name {
		case "max_context_bytes":
			return tc.MaxContextBytesOrDefault()
		case "max_tool_output_bytes":
			return tc.MaxToolOutputBytesOrDefault()
		case "max_line_bytes":
			return tc.MaxLineBytesOrDefault()
		default:
			return tc.MaxMetadataLineBytesOrDefault()
		}
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := get(c.name, TranscriptConfig{}); got != c.zero {
				t.Errorf("%s zero-value = %d, want default %d", c.name, got, c.zero)
			}

			var tc TranscriptConfig

			switch c.name {
			case "max_context_bytes":
				tc.MaxContextBytes = c.set
			case "max_tool_output_bytes":
				tc.MaxToolOutputBytes = c.set
			case "max_line_bytes":
				tc.MaxLineBytes = c.set
			default:
				tc.MaxMetadataLineBytes = c.set
			}

			if got := get(c.name, tc); got != c.set {
				t.Errorf("%s set = %d, want %d", c.name, got, c.set)
			}

			// A negative value keeps the default (Validate rejects it at load; this
			// is the runtime fail-safe).
			var neg TranscriptConfig

			switch c.name {
			case "max_context_bytes":
				neg.MaxContextBytes = -1
			case "max_tool_output_bytes":
				neg.MaxToolOutputBytes = -1
			case "max_line_bytes":
				neg.MaxLineBytes = -1
			default:
				neg.MaxMetadataLineBytes = -1
			}

			if got := get(c.name, neg); got != c.zero {
				t.Errorf("%s negative = %d, want default %d", c.name, got, c.zero)
			}
		})
	}
}

// TestEmbeddedDefaultsCarryLimitValues is the drift guard for issue #1250: the
// headless/migration/transcript processing-limit defaults must live in the
// embedded default_config.toml, asserted on the RAW parsed fields so a default
// silently dropped from the TOML (leaving only the Go fallback) fails here.
func TestEmbeddedDefaultsCarryLimitValues(t *testing.T) {
	d := Default()

	t.Run("headless", func(t *testing.T) {
		if d.Headless.MaxLineBytes != HeadlessMaxLineBytesDefault {
			t.Errorf("Default().Headless.MaxLineBytes = %d, want %d", d.Headless.MaxLineBytes, HeadlessMaxLineBytesDefault)
		}

		if d.Headless.ControlTimeout != "30s" {
			t.Errorf("Default().Headless.ControlTimeout = %q, want %q", d.Headless.ControlTimeout, "30s")
		}

		if d.Headless.InterruptTimeout != "5s" {
			t.Errorf("Default().Headless.InterruptTimeout = %q, want %q", d.Headless.InterruptTimeout, "5s")
		}

		if d.Headless.PreviewBytes != HeadlessPreviewBytesDefault {
			t.Errorf("Default().Headless.PreviewBytes = %d, want %d", d.Headless.PreviewBytes, HeadlessPreviewBytesDefault)
		}
	})

	t.Run("migration", func(t *testing.T) {
		if d.Migration.HealthWindow != "1.5s" {
			t.Errorf("Default().Migration.HealthWindow = %q, want %q", d.Migration.HealthWindow, "1.5s")
		}

		if got := d.Migration.HealthWindowDuration(); got != MigrationHealthWindowDefault {
			t.Errorf("Default().Migration.HealthWindowDuration() = %v, want %v", got, MigrationHealthWindowDefault)
		}
	})

	t.Run("transcript", func(t *testing.T) {
		if d.Transcript.MaxContextBytes != TranscriptMaxContextBytesDefault {
			t.Errorf("Default().Transcript.MaxContextBytes = %d, want %d", d.Transcript.MaxContextBytes, TranscriptMaxContextBytesDefault)
		}

		if d.Transcript.MaxToolOutputBytes != TranscriptMaxToolOutputBytesDefault {
			t.Errorf("Default().Transcript.MaxToolOutputBytes = %d, want %d", d.Transcript.MaxToolOutputBytes, TranscriptMaxToolOutputBytesDefault)
		}

		if d.Transcript.MaxLineBytes != TranscriptMaxLineBytesDefault {
			t.Errorf("Default().Transcript.MaxLineBytes = %d, want %d", d.Transcript.MaxLineBytes, TranscriptMaxLineBytesDefault)
		}

		if d.Transcript.MaxMetadataLineBytes != TranscriptMaxMetadataBytesDefault {
			t.Errorf("Default().Transcript.MaxMetadataLineBytes = %d, want %d", d.Transcript.MaxMetadataLineBytes, TranscriptMaxMetadataBytesDefault)
		}
	})
}

// TestValidateRejectsBadLimitDurations confirms a set-but-unparseable or
// non-positive [headless]/[migration] duration fails at load rather than
// silently falling back to the accessor default (issue #1250).
func TestValidateRejectsBadLimitDurations(t *testing.T) {
	fields := []struct {
		name string
		set  func(*Config, string)
	}{
		{"headless.control_timeout", func(c *Config, v string) { c.Headless.ControlTimeout = v }},
		{"headless.interrupt_timeout", func(c *Config, v string) { c.Headless.InterruptTimeout = v }},
		{"migration.health_window", func(c *Config, v string) { c.Migration.HealthWindow = v }},
	}

	for _, f := range fields {
		for _, bad := range []string{"thrawn", "0", "-1s"} {
			cfg := Default()
			f.set(cfg, bad)

			if err := cfg.Validate(); err == nil {
				t.Errorf("expected Validate() to reject %s = %q", f.name, bad)
			}
		}
	}
}

// TestValidateRejectsNegativeLimitBytes confirms a negative byte-size limit is a
// load error, guarding against a typo silently disabling a safety cap.
func TestValidateRejectsNegativeLimitBytes(t *testing.T) {
	fields := []struct {
		name string
		set  func(*Config)
	}{
		{"headless.max_line_bytes", func(c *Config) { c.Headless.MaxLineBytes = -1 }},
		{"headless.preview_bytes", func(c *Config) { c.Headless.PreviewBytes = -1 }},
		{"transcript.max_context_bytes", func(c *Config) { c.Transcript.MaxContextBytes = -1 }},
		{"transcript.max_tool_output_bytes", func(c *Config) { c.Transcript.MaxToolOutputBytes = -1 }},
		{"transcript.max_line_bytes", func(c *Config) { c.Transcript.MaxLineBytes = -1 }},
		{"transcript.max_metadata_line_bytes", func(c *Config) { c.Transcript.MaxMetadataLineBytes = -1 }},
	}

	for _, f := range fields {
		cfg := Default()
		f.set(cfg)

		if err := cfg.Validate(); err == nil {
			t.Errorf("expected Validate() to reject negative %s", f.name)
		}
	}
}
