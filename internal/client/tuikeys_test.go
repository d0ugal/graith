package client

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
)

func TestSplitKeys(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"j", []string{"j"}},
		{"j down ctrl+n", []string{"j", "down", "ctrl+n"}},
		{"  j   down  ", []string{"j", "down"}},
	}
	for _, tc := range cases {
		got := SplitKeys(tc.in)
		if len(got) != len(tc.want) {
			t.Errorf("SplitKeys(%q) = %v, want %v", tc.in, got, tc.want)
			continue
		}

		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("SplitKeys(%q)[%d] = %q, want %q", tc.in, i, got[i], tc.want[i])
			}
		}
	}
}

func TestMatchKey(t *testing.T) {
	bound := []string{"j", "down", "ctrl+n"}
	if !matchKey(bound, "down") {
		t.Error("matchKey should match a listed key")
	}

	if matchKey(bound, "up") {
		t.Error("matchKey should not match an unlisted key")
	}

	// Case sensitivity: "G" and "g" are distinct keys.
	if matchKey([]string{"g"}, "G") {
		t.Error("matchKey must be case-sensitive")
	}

	if matchKey(nil, "j") {
		t.Error("matchKey on empty list should be false")
	}
}

func TestKeyHintAndPrimaryKey(t *testing.T) {
	if got := keyHint([]string{"enter", "a"}); got != "enter/a" {
		t.Errorf("keyHint = %q, want enter/a", got)
	}

	if got := keyHint(nil); got != "" {
		t.Errorf("keyHint(nil) = %q, want empty", got)
	}

	if got := primaryKey([]string{"k", "up"}); got != "k" {
		t.Errorf("primaryKey = %q, want k", got)
	}

	if got := primaryKey(nil); got != "" {
		t.Errorf("primaryKey(nil) = %q, want empty", got)
	}
}

// TestOverlayCancelCtrlCExitsFromShippedConfig is the concrete regression for
// #1233: building the overlay cancel binding from the shipped config
// (config.Default(), i.e. the embedded default_config.toml) must keep Ctrl-C, so
// it still exits the dashboard, message viewer, and scroll pager. The shipped
// [keybindings.overlay] cancel value REPLACES — does not extend — each model's
// in-code aliases, so a default lacking ctrl+c silently dropped it from all
// three views even though the dashboard docs still list Ctrl-C.
func TestOverlayCancelCtrlCExitsFromShippedConfig(t *testing.T) {
	// The cancel binding exactly as the builders resolve it in production: the
	// config value wins because the shipped default names it.
	cancel := SplitKeys(config.Default().Keybindings.Overlay.Cancel)
	if !matchKey(cancel, "ctrl+c") {
		t.Fatalf("shipped [keybindings.overlay] cancel = %v, want it to include ctrl+c", cancel)
	}

	ctrlC := tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}

	assertQuit := func(t *testing.T, cmd tea.Cmd) {
		t.Helper()

		if cmd == nil {
			t.Fatal("ctrl+c should return a quit command")
		}

		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Fatalf("ctrl+c command did not produce a QuitMsg, got %T", cmd())
		}
	}

	t.Run("dashboard", func(t *testing.T) {
		m := NewDashboardModel(dashboardTestSessions(), nil)
		keys := DefaultDashboardKeys()
		keys.Cancel = cancel
		m.keys = keys
		m.width = 120
		m.height = 40

		_, cmd := m.Update(ctrlC)
		assertQuit(t, cmd)
	})

	t.Run("message viewer", func(t *testing.T) {
		fetch := func() ([]protocol.ConversationMessage, bool) { return nil, true }
		m := newMessageOverlayModel("self", fetch, map[string]string{})
		keys := DefaultMessageKeys()
		keys.Cancel = cancel
		m.keys = keys

		_, cmd := m.Update(ctrlC)
		assertQuit(t, cmd)
	})

	t.Run("scroll pager", func(t *testing.T) {
		m := newScrollViewModel("braw", "line one\nline two\n")
		keys := DefaultScrollKeys()
		keys.Cancel = cancel
		m.keys = keys

		res, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
		m = res.(scrollViewModel)

		_, cmd := m.Update(ctrlC)
		assertQuit(t, cmd)
	})
}

// TestDashboardEnterDeclinesDestructiveConfirmation is the safe-default
// regression for #1233. The prompt says [y/N], so the shipped overlay.confirm
// binding must make Enter decline both stop and delete rather than execute the
// destructive action. Explicit y and Y remain confirmations.
func TestDashboardEnterDeclinesDestructiveConfirmation(t *testing.T) {
	confirm := SplitKeys(config.Default().Keybindings.Overlay.Confirm)
	if matchKey(confirm, "enter") {
		t.Fatalf("shipped overlay.confirm = %v includes enter despite [y/N] prompt", confirm)
	}

	for _, tc := range []struct {
		name  string
		state dashboardState
	}{
		{"delete", dashStateConfirmDelete},
		{"stop", dashStateConfirmStop},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewDashboardModel(dashboardTestSessions(), nil)
			m.width, m.height = 120, 40
			m.state = tc.state
			m.confirmSessionID = "s1"
			m.keys.Confirm = confirm

			updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			dm := updated.(*DashboardModel)
			if dm.result != nil || cmd != nil {
				t.Fatalf("Enter executed %s confirmation: result=%+v cmd=%v", tc.name, dm.result, cmd)
			}

			if dm.state != dashStateNormal || dm.confirmSessionID != "" {
				t.Fatalf("Enter did not decline %s confirmation: state=%v id=%q", tc.name, dm.state, dm.confirmSessionID)
			}
		})
	}

	for _, key := range []string{"y", "Y"} {
		if !matchKey(confirm, key) {
			t.Errorf("shipped overlay.confirm = %v, want explicit %q confirmation", confirm, key)
		}
	}
}

// TestDashboardKeysRemapped confirms the dashboard honours a remapped attach key
// and ignores the old default once rebound (issue #1233).
func TestDashboardKeysRemapped(t *testing.T) {
	sessions := dashboardTestSessions()

	keys := DefaultDashboardKeys()
	keys.Attach = []string{"z"}

	m := NewDashboardModel(sessions, nil)
	m.keys = keys
	m.width = 120
	m.height = 40

	// The old default 'a' no longer attaches.
	dm := updateDash(m, "a")
	if dm.result != nil {
		t.Fatalf("old attach key 'a' should be inert after remap, got %+v", dm.result)
	}

	// The remapped 'z' attaches.
	dm = updateDash(dm, "z")
	if dm.result == nil || dm.result.Action != "attach" {
		t.Fatalf("remapped attach key 'z' did not attach: %+v", dm.result)
	}
}

func TestDashboardFooterReflectsConfiguredKeys(t *testing.T) {
	keys := DefaultDashboardKeys()
	keys.Delete = []string{"z"}

	m := NewDashboardModel(dashboardTestSessions(), nil)
	m.keys = keys
	m.width = 120
	m.height = 40

	view := m.View().Content
	if !strings.Contains(view, "z delete") {
		t.Errorf("dashboard footer should show remapped delete key; got:\n%s", view)
	}
}

// TestApprovalKeysRemapped confirms the approval overlay honours a remapped
// allow key.
func TestApprovalKeysRemapped(t *testing.T) {
	keys := DefaultApprovalKeys()
	keys.Allow = []string{"z"}

	m := newApprovalModel(approvalTestList())
	m.keys = keys

	// Old default 'y' no longer allows.
	res, _ := m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	got := res.(approvalModel)

	if len(got.results) != 0 {
		t.Fatalf("old allow key 'y' should be inert after remap: %+v", got.results)
	}

	// Remapped 'z' allows.
	res, _ = got.Update(tea.KeyPressMsg{Code: 'z', Text: "z"})
	got = res.(approvalModel)

	if len(got.results) != 1 || got.results[0].Decision != "allow" {
		t.Fatalf("remapped allow key 'z' did not allow: %+v", got.results)
	}
}

func TestApprovalFooterReflectsConfiguredKeys(t *testing.T) {
	keys := DefaultApprovalKeys()
	keys.AllowAll = []string{"z"}

	m := newApprovalModel(approvalTestList())
	m.keys = keys

	res, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = res.(approvalModel)

	view := m.View().Content
	if !strings.Contains(view, "z allow-all") {
		t.Errorf("approval footer should show remapped allow-all key; got:\n%s", view)
	}
}

// TestScrollKeysRemapped confirms the scroll pager honours a remapped quit key.
func TestScrollKeysRemapped(t *testing.T) {
	keys := DefaultScrollKeys()
	keys.Cancel = []string{"z"}

	m := newScrollViewModel("braw", "line one\nline two\n")
	m.keys = keys

	res, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = res.(scrollViewModel)

	// Old default 'q' no longer quits.
	if _, cmd := m.Update(tea.KeyPressMsg{Code: 'q', Text: "q"}); cmd != nil {
		t.Error("old quit key 'q' should be inert after remap")
	}

	// Remapped 'z' quits.
	if _, cmd := m.Update(tea.KeyPressMsg{Code: 'z', Text: "z"}); cmd == nil {
		t.Error("remapped quit key 'z' did not quit")
	}
}

// TestMessageKeysRemapped confirms the message viewer honours a remapped cancel
// key.
func TestMessageKeysRemapped(t *testing.T) {
	keys := DefaultMessageKeys()
	keys.Cancel = []string{"z"}

	fetch := func() ([]protocol.ConversationMessage, bool) { return nil, true }
	m := newMessageOverlayModel("self", fetch, map[string]string{})
	m.keys = keys

	// Old default 'q' no longer quits.
	if _, cmd := m.Update(tea.KeyPressMsg{Code: 'q', Text: "q"}); cmd != nil {
		t.Error("old quit key 'q' should be inert after remap")
	}

	// Remapped 'z' quits.
	if _, cmd := m.Update(tea.KeyPressMsg{Code: 'z', Text: "z"}); cmd == nil {
		t.Error("remapped quit key 'z' did not quit")
	}
}
