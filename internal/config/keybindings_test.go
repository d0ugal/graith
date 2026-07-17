package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDefaultKeybindingsIncludePrefixCommands verifies the embedded default
// config wires the previously-hardcoded m/a/r prefix commands (issue #1233).
func TestDefaultKeybindingsIncludePrefixCommands(t *testing.T) {
	cfg := Default()

	cases := map[string]string{
		"messages":        cfg.Keybindings.Messages,
		"approvals":       cfg.Keybindings.Approvals,
		"restart_session": cfg.Keybindings.RestartSession,
	}
	want := map[string]string{
		"messages":        "m",
		"approvals":       "a",
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
		"confirm":           ov.Confirm,
		"cancel":            ov.Cancel,
		"dashboard_attach":  ov.DashboardAttach,
		"approval_allow":    ov.ApprovalAllow,
		"message_pin":       ov.MessagePin,
		"message_next_conv": ov.MessageNextConv,
	}

	if ov.Confirm != "y Y" {
		t.Errorf("Keybindings.Overlay.confirm = %q, want y/Y without Enter so [y/N] stays safe", ov.Confirm)
	}

	for name, got := range cases {
		if got == "" {
			t.Errorf("Keybindings.Overlay.%s is empty; expected a default", name)
		}
	}
}

// TestOverlayKeybindingPartialOverride confirms that naming only some overlay
// keys in a config file keeps the built-in defaults for the rest.
func TestOverlayKeybindingPartialOverride(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	toml := `
[keybindings.overlay]
dashboard_attach = "enter"
`

	if err := os.WriteFile(cfgPath, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Keybindings.Overlay.DashboardAttach != "enter" {
		t.Errorf("dashboard_attach = %q, want overridden %q", cfg.Keybindings.Overlay.DashboardAttach, "enter")
	}

	// An unspecified key keeps its default from the embedded config.
	if cfg.Keybindings.Overlay.DashboardStop != "s" {
		t.Errorf("dashboard_stop = %q, want default %q (partial table must not zero other keys)", cfg.Keybindings.Overlay.DashboardStop, "s")
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
		k := Keybindings{Messages: "", Approvals: ""}
		if got := k.Conflicts(); len(got) != 0 {
			t.Errorf("empty bindings reported as conflicting: %v", got)
		}
	})

	t.Run("action colliding with prefix is detected", func(t *testing.T) {
		k := Keybindings{Prefix: "d", Detach: "d"}

		got := k.Conflicts()
		if len(got) != 1 || !strings.Contains(got[0], "prefix") || !strings.Contains(got[0], "detach") {
			t.Fatalf("Conflicts() = %v, want prefix/detach precedence collision", got)
		}
	})
}

// TestParsePrefixBytePreservesPrintableLiterals covers the round-3 regression
// (issue #1233): parsePrefixByte must normalize only the ctrl+letter control
// syntax and keep an exactly-one-byte printable literal byte-for-byte. Applying
// TrimSpace/ToLower to the whole value silently rewrote "A" to "a" and collapsed
// the valid literal " " (0x20) to empty, which restored the ctrl+b default.
func TestParsePrefixBytePreservesPrintableLiterals(t *testing.T) {
	cases := []struct {
		name   string
		raw    string
		want   byte
		wantOK bool
	}{
		{"empty keeps ctrl+b default", "", 0x02, true},
		{"lowercase literal", "a", 'a', true},
		{"uppercase literal preserved", "A", 'A', true},
		{"space literal preserved", " ", 0x20, true},
		{"ctrl+letter lowercase", "ctrl+b", 0x02, true},
		{"ctrl+letter uppercase normalized", "CTRL+B", 0x02, true},
		{"ctrl+a", "ctrl+a", 0x01, true},
		{"digit literal", "5", '5', true},
		{"multi-char rejected", "ab", 0, false},
		{"control byte rejected", "\x1b", 0, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := parsePrefixByte(c.raw)
			if ok != c.wantOK {
				t.Fatalf("parsePrefixByte(%q) ok = %v, want %v", c.raw, ok, c.wantOK)
			}

			if ok && got != c.want {
				t.Errorf("parsePrefixByte(%q) = %#x, want %#x", c.raw, got, c.want)
			}
		})
	}
}

// TestConflictReportsRuntimeWinner asserts the collision warning names the action
// that actually executes. Approvals precedes Messages in the runtime switch
// (client/passthrough.go), so a shared byte must report approvals — not messages,
// which the pre-round-3 passthroughActions() order wrongly named (issue #1233).
func TestConflictReportsRuntimeWinner(t *testing.T) {
	k := Keybindings{Approvals: "x", Messages: "x"}

	got := k.Conflicts()
	if len(got) != 1 {
		t.Fatalf("Conflicts() = %v, want exactly one collision", got)
	}

	if !strings.Contains(got[0], "approvals takes precedence") {
		t.Errorf("collision %q should report approvals as the runtime winner", got[0])
	}

	// The prefix always wins over any action sharing its byte.
	kp := Keybindings{Prefix: "x", Approvals: "x", Messages: "x"}

	gotP := kp.Conflicts()
	if len(gotP) != 1 || !strings.Contains(gotP[0], "prefix takes precedence") {
		t.Fatalf("Conflicts() = %v, want prefix as winner", gotP)
	}
}

func TestPassthroughKeybindingShapeValidation(t *testing.T) {
	valid := []Keybindings{
		{Prefix: "ctrl+b", Messages: "m"},
		{Prefix: "x", Messages: ""}, // empty explicitly disables the action
	}
	for _, bindings := range valid {
		if err := bindings.Validate(); err != nil {
			t.Errorf("valid bindings %+v rejected: %v", bindings, err)
		}
	}

	tests := []struct {
		name string
		keys Keybindings
		want string
	}{
		{"multi-character", Keybindings{Messages: "dd"}, "messages"},
		{"multibyte", Keybindings{Messages: "é"}, "messages"},
		{"NUL", Keybindings{Messages: "\x00"}, "messages"},
		{"control byte", Keybindings{Messages: "\x1b"}, "messages"},
		{"invalid prefix", Keybindings{Prefix: "ctrl+1"}, "prefix"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.keys.Validate()
			if err == nil {
				t.Fatalf("Validate() accepted unsupported bindings %+v", tt.keys)
			}

			if !strings.Contains(err.Error(), tt.want) || !strings.Contains(err.Error(), "printable ASCII") {
				t.Errorf("Validate() error = %q, want actionable %q printable-ASCII error", err, tt.want)
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
