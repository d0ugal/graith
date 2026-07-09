package client

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// updateNameModel applies a message and returns the concrete model type.
func updateNameModel(m nameInputModel, msg tea.Msg) nameInputModel {
	result, _ := m.Update(msg)
	return result.(nameInputModel)
}

func TestNewNameInputModel_Defaults(t *testing.T) {
	m := newNameInputModel("Rename bothy")

	if m.title != "Rename bothy" {
		t.Errorf("title = %q, want %q", m.title, "Rename bothy")
	}

	if m.done {
		t.Error("fresh model should not be done")
	}

	if !m.input.Focused() {
		t.Error("input should be focused on creation")
	}

	if m.input.CharLimit != 64 {
		t.Errorf("CharLimit = %d, want 64", m.input.CharLimit)
	}
}

func TestNameInputModel_InitReturnsBlink(t *testing.T) {
	m := newNameInputModel("kirk")
	if m.Init() == nil {
		t.Error("Init should return a (blink) command, got nil")
	}
}

func TestNameInputModel_WindowSizeUpdatesDimensions(t *testing.T) {
	m := newNameInputModel("kirk")

	m = updateNameModel(m, tea.WindowSizeMsg{Width: 120, Height: 40})
	if m.width != 120 || m.height != 40 {
		t.Errorf("dimensions = %dx%d, want 120x40", m.width, m.height)
	}
}

func TestNameInputModel_EnterWithValueCompletes(t *testing.T) {
	m := newNameInputModel("kirk")
	m.input.SetValue("braw-bothy")

	result, cmd := m.Update(keyPress("enter"))
	m = result.(nameInputModel)

	if !m.done {
		t.Error("expected done=true after enter with a non-empty value")
	}

	if cmd == nil {
		t.Error("expected a tea.Quit command after enter with a value")
	}
}

func TestNameInputModel_EnterWithWhitespaceOnlyStays(t *testing.T) {
	m := newNameInputModel("kirk")
	m.input.SetValue("   ")

	result, cmd := m.Update(keyPress("enter"))
	m = result.(nameInputModel)

	if m.done {
		t.Error("expected done=false when value is only whitespace")
	}

	if cmd != nil {
		t.Error("expected no command when enter is pressed with a blank value")
	}
}

func TestNameInputModel_EscQuitsWithoutDone(t *testing.T) {
	m := newNameInputModel("kirk")
	m.input.SetValue("dreich")

	result, cmd := m.Update(keyPress("esc"))
	m = result.(nameInputModel)

	if m.done {
		t.Error("esc should cancel, not complete the model")
	}

	if cmd == nil {
		t.Error("esc should return a tea.Quit command")
	}
}

func TestNameInputModel_CtrlCQuits(t *testing.T) {
	m := newNameInputModel("kirk")

	_, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Error("ctrl+c should return a tea.Quit command")
	}
}

func TestNameInputModel_SpaceInsertsDash(t *testing.T) {
	m := newNameInputModel("kirk")
	m.input.SetValue("braw")

	m = updateNameModel(m, keyPress(" "))

	if got := m.input.Value(); got != "braw-" {
		t.Errorf("value after space = %q, want %q (space normalises to dash)", got, "braw-")
	}
}

func TestNameInputModel_RegularRuneTyped(t *testing.T) {
	m := newNameInputModel("kirk")

	m = updateNameModel(m, keyPress("a"))
	m = updateNameModel(m, keyPress("b"))

	if got := m.input.Value(); got != "ab" {
		t.Errorf("value after typing = %q, want %q", got, "ab")
	}
}

func TestNameInputModel_ViewZeroSizeIsEmpty(t *testing.T) {
	m := newNameInputModel("kirk")

	if got := m.View().Content; got != "" {
		t.Errorf("View with zero dimensions should be empty, got %q", got)
	}
}

func TestNameInputModel_ViewRendersTitleAndHint(t *testing.T) {
	m := newNameInputModel("Rename bothy")
	m = updateNameModel(m, tea.WindowSizeMsg{Width: 80, Height: 24})

	out := m.View().Content
	if !strings.Contains(out, "Rename bothy") {
		t.Errorf("View should render the title, got:\n%s", out)
	}

	if !strings.Contains(out, "enter confirm") {
		t.Errorf("View should render the confirm hint, got:\n%s", out)
	}

	// AltScreen should be enabled so the prompt takes over the terminal.
	if !m.View().AltScreen {
		t.Error("View should set AltScreen=true")
	}

	// The rendered frame must fill the requested height so it fully covers the
	// background when composited.
	if lines := strings.Count(out, "\n") + 1; lines != 24 {
		t.Errorf("rendered frame has %d lines, want 24 (full height)", lines)
	}
}
