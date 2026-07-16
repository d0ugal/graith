package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLimitsAccessorsFallBackToDefault confirms every [limits] accessor returns
// its default constant when the field is unset (zero) or negative, and honours a
// positive value. This is the runtime fail-safe that mirrors the other int
// accessors (BatchSizeOrDefault, SampleHistoryOrDefault) added for issue #1252.
func TestLimitsAccessorsFallBackToDefault(t *testing.T) {
	cases := []struct {
		name string
		get  func(LimitsConfig) int
		set  func(*LimitsConfig, int)
		def  int
	}{
		{"log_lines", LimitsConfig.LogLinesOrDefault, func(l *LimitsConfig, v int) { l.LogLines = v }, LimitsLogLinesDefault},
		{"wait_scan_lines", LimitsConfig.WaitScanLinesOrDefault, func(l *LimitsConfig, v int) { l.WaitScanLines = v }, LimitsWaitScanLinesDefault},
		{"wait_buffer_bytes", LimitsConfig.WaitBufferBytesOrDefault, func(l *LimitsConfig, v int) { l.WaitBufferBytes = v }, LimitsWaitBufferBytesDefault},
		{"mcp_log_read_bytes", LimitsConfig.MCPLogReadBytesOrDefault, func(l *LimitsConfig, v int) { l.MCPLogReadBytes = v }, LimitsMCPLogReadBytesDefault},
		{"approval_display_bytes", LimitsConfig.ApprovalDisplayBytesOrDefault, func(l *LimitsConfig, v int) { l.ApprovalDisplayBytes = v }, LimitsApprovalDisplayBytesDefault},
		{"last_message_runes", LimitsConfig.LastMessageRunesOrDefault, func(l *LimitsConfig, v int) { l.LastMessageRunes = v }, LimitsLastMessageRunesDefault},
		{"inbox_preview_bytes", LimitsConfig.InboxPreviewBytesOrDefault, func(l *LimitsConfig, v int) { l.InboxPreviewBytes = v }, LimitsInboxPreviewBytesDefault},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Unset (zero) falls back to the default.
			var zero LimitsConfig
			if got := c.get(zero); got != c.def {
				t.Errorf("unset %s = %d, want default %d", c.name, got, c.def)
			}

			// Negative falls back to the default.
			var neg LimitsConfig
			c.set(&neg, -7)

			if got := c.get(neg); got != c.def {
				t.Errorf("negative %s = %d, want default %d", c.name, got, c.def)
			}

			// A positive value is honoured verbatim.
			var set LimitsConfig
			c.set(&set, 42)

			if got := c.get(set); got != 42 {
				t.Errorf("explicit %s = %d, want 42", c.name, got)
			}
		})
	}
}

// TestEmbeddedLimitsCarryPolicyValues is the issue #1252 drift guard: every
// [limits] default must live in the embedded default_config.toml (asserting the
// RAW parsed field), not only as a Go fallback literal, so `gr config
// show/diff/reset` describe the full effective configuration.
func TestEmbeddedLimitsCarryPolicyValues(t *testing.T) {
	l := Default().Limits

	checks := []struct {
		name string
		got  int
		want int
	}{
		{"log_lines", l.LogLines, LimitsLogLinesDefault},
		{"wait_scan_lines", l.WaitScanLines, LimitsWaitScanLinesDefault},
		{"wait_buffer_bytes", l.WaitBufferBytes, LimitsWaitBufferBytesDefault},
		{"mcp_log_read_bytes", l.MCPLogReadBytes, LimitsMCPLogReadBytesDefault},
		{"approval_display_bytes", l.ApprovalDisplayBytes, LimitsApprovalDisplayBytesDefault},
		{"last_message_runes", l.LastMessageRunes, LimitsLastMessageRunesDefault},
		{"inbox_preview_bytes", l.InboxPreviewBytes, LimitsInboxPreviewBytesDefault},
	}

	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("Default().Limits.%s = %d, want %d (missing from default_config.toml?)", c.name, c.got, c.want)
		}
	}
}

// TestLoadPreservesPartialLimitsMerge confirms a user config that sets only one
// limit keeps every other embedded default through the real Load() merge path
// (issue #1252 acceptance: partial configs stay compatible).
func TestLoadPreservesPartialLimitsMerge(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")

	toml := `
[limits]
log_lines = 42
`
	if err := os.WriteFile(cfgPath, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	if got := cfg.Limits.LogLinesOrDefault(); got != 42 {
		t.Errorf("log_lines override = %d, want 42", got)
	}

	// Every unmentioned limit keeps its embedded default after the merge.
	if got := cfg.Limits.WaitScanLinesOrDefault(); got != LimitsWaitScanLinesDefault {
		t.Errorf("wait_scan_lines = %d, want default %d (preserved)", got, LimitsWaitScanLinesDefault)
	}

	if got := cfg.Limits.ApprovalDisplayBytesOrDefault(); got != LimitsApprovalDisplayBytesDefault {
		t.Errorf("approval_display_bytes = %d, want default %d (preserved)", got, LimitsApprovalDisplayBytesDefault)
	}

	if got := cfg.Limits.InboxPreviewBytesOrDefault(); got != LimitsInboxPreviewBytesDefault {
		t.Errorf("inbox_preview_bytes = %d, want default %d (preserved)", got, LimitsInboxPreviewBytesDefault)
	}
}
