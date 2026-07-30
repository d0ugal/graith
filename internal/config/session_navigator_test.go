package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSessionNavigatorSelectedDetailEmbeddedDefaults(t *testing.T) {
	detail := Default().SessionNavigator.SelectedDetail

	if !detail.Enabled {
		t.Fatal("session_navigator.selected_detail.enabled = false, want true")
	}

	if got := detail.Layout; got != SessionNavigatorSelectedDetailLayoutSidePanel {
		t.Errorf("session_navigator.selected_detail.layout = %q, want %q", got, SessionNavigatorSelectedDetailLayoutSidePanel)
	}

	if got := detail.MinTerminalWidth; got != SessionNavigatorSelectedDetailMinTerminalWidthDefault {
		t.Errorf("session_navigator.selected_detail.min_terminal_width = %d, want %d", got, SessionNavigatorSelectedDetailMinTerminalWidthDefault)
	}

	if got := detail.MinTerminalHeight; got != SessionNavigatorSelectedDetailMinTerminalHeightDefault {
		t.Errorf("session_navigator.selected_detail.min_terminal_height = %d, want %d", got, SessionNavigatorSelectedDetailMinTerminalHeightDefault)
	}

	if got := detail.MaxWidth; got != SessionNavigatorSelectedDetailMaxWidthDefault {
		t.Errorf("session_navigator.selected_detail.max_width = %d, want %d", got, SessionNavigatorSelectedDetailMaxWidthDefault)
	}

	if got, want := detail.Fields, DefaultSessionNavigatorSelectedDetailFields(); !reflect.DeepEqual(got, want) {
		t.Errorf("session_navigator.selected_detail.fields = %v, want %v", got, want)
	}
}

func TestSessionNavigatorSelectedDetailLoadOverride(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	toml := `
[session_navigator.selected_detail]
enabled = false
min_terminal_width = 180
fields = ["branch", "worktree", "pr"]
`

	if err := os.WriteFile(cfgPath, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	detail := cfg.SessionNavigator.SelectedDetail
	if detail.Enabled {
		t.Fatal("session_navigator.selected_detail.enabled = true, want overridden false")
	}

	if got := detail.MinTerminalWidthOrDefault(); got != 180 {
		t.Errorf("session_navigator.selected_detail.min_terminal_width = %d, want 180", got)
	}

	if got := detail.MaxWidthOrDefault(); got != SessionNavigatorSelectedDetailMaxWidthDefault {
		t.Errorf("session_navigator.selected_detail.max_width = %d, want default %d", got, SessionNavigatorSelectedDetailMaxWidthDefault)
	}

	if got, want := detail.FieldsOrDefault(), []string{"branch", "worktree", "pr"}; !reflect.DeepEqual(got, want) {
		t.Errorf("session_navigator.selected_detail.fields = %v, want %v", got, want)
	}
}

func TestSessionNavigatorSelectedDetailIgnoresOldOverlayNamespace(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	toml := `
[overlay.selected_detail]
enabled = false
min_terminal_width = 180
fields = ["branch"]
`

	if err := os.WriteFile(cfgPath, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	detail := cfg.SessionNavigator.SelectedDetail
	if !detail.Enabled {
		t.Fatal("session_navigator.selected_detail.enabled = false, want default true")
	}

	if got := detail.MinTerminalWidthOrDefault(); got != SessionNavigatorSelectedDetailMinTerminalWidthDefault {
		t.Errorf("session_navigator.selected_detail.min_terminal_width = %d, want default %d", got, SessionNavigatorSelectedDetailMinTerminalWidthDefault)
	}

	if got, want := detail.FieldsOrDefault(), DefaultSessionNavigatorSelectedDetailFields(); !reflect.DeepEqual(got, want) {
		t.Errorf("session_navigator.selected_detail.fields = %v, want default %v", got, want)
	}
}

func TestSessionNavigatorSelectedDetailFieldsNormalizeExplicitValues(t *testing.T) {
	tests := map[string]struct {
		toml string
		want []string
	}{
		"empty list stays empty": {
			toml: `[session_navigator.selected_detail]
fields = []
`,
			want: []string{},
		},
		"field whitespace trimmed": {
			toml: `[session_navigator.selected_detail]
fields = [" branch ", " labels "]
`,
			want: []string{"branch", "labels"},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			cfgPath := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(cfgPath, []byte(test.toml), 0o600); err != nil {
				t.Fatal(err)
			}

			cfg, err := Load(cfgPath)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}

			if got := cfg.SessionNavigator.SelectedDetail.FieldsOrDefault(); !reflect.DeepEqual(got, test.want) {
				t.Errorf("session_navigator.selected_detail.fields = %v, want %v", got, test.want)
			}
		})
	}
}

func TestSessionNavigatorSelectedDetailValidateRejectsInvalidNames(t *testing.T) {
	tests := map[string]string{
		"layout": `[session_navigator.selected_detail]
layout = "dashboard"
`,
		"field": `[session_navigator.selected_detail]
fields = ["branch", "needs_attention"]
`,
		"empty field": `[session_navigator.selected_detail]
fields = ["branch", ""]
`,
		"max width": `[session_navigator.selected_detail]
max_width = 10
`,
	}

	for name, toml := range tests {
		t.Run(name, func(t *testing.T) {
			cfgPath := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(cfgPath, []byte(toml), 0o600); err != nil {
				t.Fatal(err)
			}

			if _, err := Load(cfgPath); err == nil {
				t.Fatal("Load() = nil, want validation error")
			}
		})
	}
}
