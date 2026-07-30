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

// TestDefaultTUIKeybindings verifies the embedded default config populates the
// [keybindings.tui] table.
func TestDefaultTUIKeybindings(t *testing.T) {
	tui := Default().Keybindings.TUI

	cases := map[string]string{
		"up":                tui.Up,
		"down":              tui.Down,
		"message_pin":       tui.MessagePin,
		"message_next_conv": tui.MessageNextConv,
		"message_topics":    tui.MessageTopics,
		"message_direct":    tui.MessageDirect,
	}
	for name, got := range cases {
		if got == "" {
			t.Errorf("Keybindings.TUI.%s is empty; expected a default", name)
		}
	}

	if !strings.Contains(tui.Cancel, "ctrl+c") {
		t.Errorf("Keybindings.TUI.cancel = %q, want ctrl+c clean-exit binding", tui.Cancel)
	}
}

// TestTUIKeybindingPartialOverride confirms that naming only some TUI
// keys in a config file keeps the built-in defaults for the rest.
func TestTUIKeybindingPartialOverride(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	toml := `
[keybindings.tui]
message_pin = "space"
`

	if err := os.WriteFile(cfgPath, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Keybindings.TUI.MessagePin != "space" {
		t.Errorf("message_pin = %q, want overridden %q", cfg.Keybindings.TUI.MessagePin, "space")
	}

	// An unspecified key keeps its default from the embedded config.
	if cfg.Keybindings.TUI.MessageExpandAll != "O" {
		t.Errorf("message_expand_all = %q, want default %q (partial table must not zero other keys)", cfg.Keybindings.TUI.MessageExpandAll, "O")
	}
}

func TestLegacyNavigatorConfigNamesAreIgnored(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	toml := `
[overlay]
shortcut_keys = "abc"

[keybindings]
session_list = "z"

[keybindings.overlay]
cancel = "x"
`

	if err := os.WriteFile(cfgPath, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.SessionNavigator.ShortcutKeys != "1234567890" {
		t.Errorf("legacy [overlay] changed SessionNavigator.ShortcutKeys to %q", cfg.SessionNavigator.ShortcutKeys)
	}

	if cfg.Keybindings.SessionNavigator != "w" {
		t.Errorf("legacy session_list changed SessionNavigator key to %q", cfg.Keybindings.SessionNavigator)
	}

	if cfg.Keybindings.TUI.Cancel != "q esc ctrl+c" {
		t.Errorf("legacy [keybindings.overlay] changed TUI cancel to %q", cfg.Keybindings.TUI.Cancel)
	}
}

func TestSessionNavigatorConfigNamesLoad(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	toml := `
[session_navigator]
shortcut_keys = "canny"

[keybindings]
session_navigator = "z"

[keybindings.tui]
cancel = "esc"
`

	if err := os.WriteFile(cfgPath, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.SessionNavigator.ShortcutKeys != "canny" {
		t.Errorf("SessionNavigator.ShortcutKeys = %q, want %q", cfg.SessionNavigator.ShortcutKeys, "canny")
	}

	if cfg.Keybindings.SessionNavigator != "z" {
		t.Errorf("Keybindings.SessionNavigator = %q, want %q", cfg.Keybindings.SessionNavigator, "z")
	}

	if cfg.Keybindings.TUI.Cancel != "esc" {
		t.Errorf("Keybindings.TUI.Cancel = %q, want %q", cfg.Keybindings.TUI.Cancel, "esc")
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

	t.Run("runtime order names the actual winner", func(t *testing.T) {
		k := Default().Keybindings
		k.Messages = "s"
		k.Shell = "s"

		got := k.Conflicts()
		if len(got) != 1 {
			t.Fatalf("Conflicts() = %v, want exactly one collision", got)
		}

		if !strings.Contains(got[0], "messages wins") {
			t.Errorf("collision message %q should name messages as the runtime winner", got[0])
		}
	})

	t.Run("prefix collision reports unreachable action", func(t *testing.T) {
		k := Default().Keybindings
		k.Prefix = "d"
		k.Detach = "d"

		got := k.Conflicts()
		if len(got) == 0 {
			t.Fatal("expected a prefix/action collision")
		}

		if !strings.Contains(got[0], "prefix") || !strings.Contains(got[0], "detach") || !strings.Contains(got[0], "unreachable") {
			t.Errorf("prefix collision message %q should explain detach is unreachable", got[0])
		}
	})

	t.Run("navigator cancel shadows navigator action", func(t *testing.T) {
		k := Default().Keybindings
		k.TUI.Cancel = "q esc ctrl+c x"

		got := k.Conflicts()
		if len(got) != 1 {
			t.Fatalf("Conflicts() = %v, want exactly one Navigator collision", got)
		}

		if !strings.Contains(got[0], "tui.cancel") || !strings.Contains(got[0], "delete_session") {
			t.Errorf("Navigator collision message %q should name cancel and delete_session", got[0])
		}
	})

	t.Run("navigator configured action shadows fixed action", func(t *testing.T) {
		k := Default().Keybindings
		k.Search = "s"

		got := k.Conflicts()
		if len(got) != 1 {
			t.Fatalf("Conflicts() = %v, want exactly one Navigator collision", got)
		}

		if !strings.Contains(got[0], "search wins") || !strings.Contains(got[0], "navigator star") {
			t.Errorf("Navigator collision message %q should name search as the winner over star", got[0])
		}
	})
}

func TestKeybindingValidationRejectsInvalidPassthroughActions(t *testing.T) {
	tests := map[string]string{
		"empty":       `messages = ""`,
		"multi_char":  `messages = "dd"`,
		"multibyte":   `messages = "é"`,
		"nul":         `messages = "\u0000"`,
		"bad_prefix":  `prefix = "ctrl+space"`,
		"trimmed_key": `messages = " m "`,
	}

	for name, line := range tests {
		t.Run(name, func(t *testing.T) {
			assertLoadRejectsKeybinding(t, "[keybindings]", line, "keybindings.")
		})
	}
}

func TestKeybindingValidationAcceptsNavigatorTUIKeyNames(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	toml := `
[keybindings]
delete_session = "ctrl+d"
resume_session = "f5"
search = " "
`

	if err := os.WriteFile(cfgPath, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(cfgPath); err != nil {
		t.Fatalf("Load rejected Navigator TUI key names: %v", err)
	}
}

func TestTUIKeybindingValidationRejectsInvalidNames(t *testing.T) {
	tests := map[string]string{
		"unknown_cancel": `cancel = "escape"`,
		"bad_ctrl":       `cancel = "ctrl-c"`,
		"multibyte":      `message_pin = "é"`,
	}

	for name, line := range tests {
		t.Run(name, func(t *testing.T) {
			assertLoadRejectsKeybinding(t, "[keybindings.tui]", line, "keybindings.tui.")
		})
	}
}

func assertLoadRejectsKeybinding(t *testing.T, header, line, wantError string) {
	t.Helper()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	toml := header + "\n" + line + "\n"

	if err := os.WriteFile(cfgPath, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Load(cfgPath)
	if err == nil {
		t.Fatal("Load() = nil, want validation error")
	}

	if !strings.Contains(err.Error(), wantError) {
		t.Errorf("error %q should contain %q", err, wantError)
	}
}

func TestParseKeybindingPrefixPreservesPrintableLiteralBytes(t *testing.T) {
	tests := map[string]struct {
		input string
		want  byte
	}{
		"uppercase": {input: "A", want: 'A'},
		"space":     {input: " ", want: ' '},
		"backtick":  {input: "`", want: '`'},
		"ctrl+b":    {input: "ctrl+b", want: 0x02},
		"trimmed":   {input: " CTRL+A ", want: 0x01},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := ParseKeybindingPrefixByte(test.input)
			if err != nil {
				t.Fatalf("ParseKeybindingPrefixByte(%q): %v", test.input, err)
			}

			if got != test.want {
				t.Errorf("ParseKeybindingPrefixByte(%q) = %#x, want %#x", test.input, got, test.want)
			}
		})
	}
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
