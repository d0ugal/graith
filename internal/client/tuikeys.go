package client

import "strings"

// This file carries the configurable keybindings for the full-screen terminal
// overlays (dashboard, approval prompt, message viewer, scroll pager). Each
// action holds a list of bubbletea key names; pressing any listed key triggers
// it. The cli layer builds these from the [keybindings.overlay] config table,
// falling back to the Default*Keys() values below when a key is unset. See #1233.

// SplitKeys parses a space-separated key list (e.g. "j down ctrl+n") into its
// individual key names, dropping empty fields. It is exported so the cli layer
// can translate config strings into the key slices these overlays consume.
func SplitKeys(s string) []string {
	return strings.Fields(s)
}

// matchKey reports whether the pressed key string s is one of the bound keys.
// Matching is exact and case-sensitive so "g"/"G" and "o"/"O" stay distinct.
func matchKey(bound []string, s string) bool {
	for _, k := range bound {
		if k == s {
			return true
		}
	}

	return false
}

// keyHint renders a binding list for a help footer, joining the keys with "/"
// (e.g. ["enter" "a"] -> "enter/a"). An empty list renders as "" so callers can
// omit a hint for an unbound action.
func keyHint(bound []string) string {
	return strings.Join(bound, "/")
}

// primaryKey returns the first (representative) key of a binding list, for
// compact footers where listing every alias would overflow the line. An empty
// list yields "".
func primaryKey(bound []string) string {
	if len(bound) == 0 {
		return ""
	}

	return bound[0]
}

// DashboardKeys are the keys the `gr dashboard` TUI listens for.
type DashboardKeys struct {
	Up      []string
	Down    []string
	Attach  []string
	Stop    []string
	Delete  []string
	Resume  []string
	Confirm []string
	Cancel  []string
}

// DefaultDashboardKeys returns the built-in dashboard bindings. They mirror the
// [keybindings.overlay] defaults in default_config.toml.
func DefaultDashboardKeys() DashboardKeys {
	return DashboardKeys{
		Up:      []string{"k", "up"},
		Down:    []string{"j", "down"},
		Attach:  []string{"enter", "a"},
		Stop:    []string{"s"},
		Delete:  []string{"x", "d"},
		Resume:  []string{"r"},
		Confirm: []string{"y", "Y"},
		Cancel:  []string{"q", "esc", "ctrl+c"},
	}
}

// ApprovalKeys are the keys the pending-approvals overlay listens for.
type ApprovalKeys struct {
	Up       []string
	Down     []string
	Allow    []string
	Deny     []string
	AllowAll []string
	Cancel   []string
}

// DefaultApprovalKeys returns the built-in approval-overlay bindings.
func DefaultApprovalKeys() ApprovalKeys {
	return ApprovalKeys{
		Up:       []string{"k", "up"},
		Down:     []string{"j", "down"},
		Allow:    []string{"y", "enter"},
		Deny:     []string{"n", "x"},
		AllowAll: []string{"a"},
		Cancel:   []string{"q", "esc", "ctrl+c"},
	}
}

// MessageKeys are the keys the message-viewer overlay listens for.
type MessageKeys struct {
	Up          []string
	Down        []string
	PageUp      []string
	PageDown    []string
	Top         []string
	Bottom      []string
	Pin         []string
	ExpandAll   []string
	CollapseAll []string
	NextConv    []string
	PrevConv    []string
	Cancel      []string
}

// DefaultMessageKeys returns the built-in message-viewer bindings.
func DefaultMessageKeys() MessageKeys {
	return MessageKeys{
		Up:          []string{"k", "up", "ctrl+p"},
		Down:        []string{"j", "down", "ctrl+n"},
		PageUp:      []string{"pgup", "ctrl+u", "ctrl+b"},
		PageDown:    []string{"pgdown", "space", " ", "ctrl+d", "ctrl+f"},
		Top:         []string{"g", "home"},
		Bottom:      []string{"G", "end"},
		Pin:         []string{"enter"},
		ExpandAll:   []string{"O"},
		CollapseAll: []string{"C"},
		NextConv:    []string{"l", "right", "tab"},
		PrevConv:    []string{"h", "left", "shift+tab"},
		Cancel:      []string{"q", "esc", "ctrl+c"},
	}
}

// ScrollKeys are the keys the scrollback pager listens for on top of the
// bubbles viewport's own scrolling (up/down/page keys are handled by the
// viewport and are not remapped here).
type ScrollKeys struct {
	Top    []string
	Bottom []string
	Cancel []string
}

// DefaultScrollKeys returns the built-in scroll-pager bindings.
func DefaultScrollKeys() ScrollKeys {
	return ScrollKeys{
		Top:    []string{"g", "home"},
		Bottom: []string{"G", "end"},
		Cancel: []string{"q", "esc", "ctrl+c"},
	}
}
