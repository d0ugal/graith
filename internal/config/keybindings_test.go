package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDefaultKeybindingsIncludePrefixCommands verifies the embedded default
// config wires the previously-hardcoded m/r prefix commands (issue #1233).
func TestDefaultKeybindingsIncludePrefixCommands(t *testing.T) {
	cfg := Default()

	cases := map[string]string{
		"messages":        cfg.Keybindings.Messages,
		"restart_session": cfg.Keybindings.RestartSession,
	}
	want := map[string]string{
		"messages":        "m",
		"restart_session": "r",
	}

	for name, got := range cases {
		if got != want[name] {
			t.Errorf("Keybindings.%s = %q, want %q", name, got, want[name])
		}
	}
}

// TestDefaultOverlayKeybindings verifies the embedded default config populates
// the [keybindings.overlay] table.
func TestDefaultOverlayKeybindings(t *testing.T) {
	ov := Default().Keybindings.Overlay

	cases := map[string]string{
		"up":                ov.Up,
		"down":              ov.Down,
		"message_pin":       ov.MessagePin,
		"message_next_conv": ov.MessageNextConv,
	}
	for name, got := range cases {
		if got == "" {
			t.Errorf("Keybindings.Overlay.%s is empty; expected a default", name)
		}
	}

	if !strings.Contains(ov.Cancel, "ctrl+c") {
		t.Errorf("Keybindings.Overlay.cancel = %q, want ctrl+c clean-exit binding", ov.Cancel)
	}
}

// TestOverlayKeybindingPartialOverride confirms that naming only some overlay
// keys in a config file keeps the built-in defaults for the rest.
func TestOverlayKeybindingPartialOverride(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	toml := `
[keybindings.overlay]
message_pin = "space"
`

	if err := os.WriteFile(cfgPath, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Keybindings.Overlay.MessagePin != "space" {
		t.Errorf("message_pin = %q, want overridden %q", cfg.Keybindings.Overlay.MessagePin, "space")
	}

	// An unspecified key keeps its default from the embedded config.
	if cfg.Keybindings.Overlay.MessageExpandAll != "O" {
		t.Errorf("message_expand_all = %q, want default %q (partial table must not zero other keys)", cfg.Keybindings.Overlay.MessageExpandAll, "O")
	}
}

func TestKeybindingsConflicts(t *testing.T) {
	t.Run("no conflicts in defaults", func(t *testing.T) {
		if got := Default().Keybindings.Conflicts(); len(got) != 0 {
			t.Errorf("default keybindings report conflicts: %v", got)
		}
	})

	t.Run("duplicate prefix commands detected", func(t *testing.T) {
		k := Keybindings{
			Detach:   "d",
			Messages: "d", // collides with detach
		}

		got := k.Conflicts()
		if len(got) != 1 {
			t.Fatalf("Conflicts() = %v, want exactly one collision", got)
		}

		if !strings.Contains(got[0], "detach") || !strings.Contains(got[0], "messages") {
			t.Errorf("collision message %q should name both detach and messages", got[0])
		}
	})

	t.Run("empty bindings are not conflicts", func(t *testing.T) {
		k := Keybindings{Messages: ""}
		if got := k.Conflicts(); len(got) != 0 {
			t.Errorf("empty bindings reported as conflicting: %v", got)
		}
	})
}

// TestLoadPopulatesKeybindingConflictWarnings verifies a conflicting config
// loads successfully (warn, don't fail) but records a warning (issue #1233).
func TestLoadPopulatesKeybindingConflictWarnings(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	toml := `
[keybindings]
messages = "d"
detach = "d"
`

	if err := os.WriteFile(cfgPath, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load must not fail on a keybinding conflict: %v", err)
	}

	if len(cfg.Warnings) == 0 {
		t.Fatal("expected a keybinding-conflict warning, got none")
	}

	found := false

	for _, w := range cfg.Warnings {
		if strings.Contains(w, "detach") && strings.Contains(w, "messages") {
			found = true
		}
	}

	if !found {
		t.Errorf("warnings %v should include the detach/messages collision", cfg.Warnings)
	}
}
