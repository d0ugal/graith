package client

import (
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/config"
)

// prefixActionEntry ties a config keybinding label to the PassthroughKeys field
// it maps to and the PassthroughResult the runtime switch returns for it. It is
// the bridge between the config-layer conflict reporting (config.Keybindings)
// and the runtime switch in runPassthroughLoop, so a single test can assert the
// reported collision winner is the action that actually executes (issue #1233).
type prefixActionEntry struct {
	label  string
	setKey func(*PassthroughKeys, PassthroughBinding)
	setCfg func(*config.Keybindings, string)
	result PassthroughResult
}

func prefixActionRegistry() []prefixActionEntry {
	return []prefixActionEntry{
		{"detach", func(k *PassthroughKeys, b PassthroughBinding) { k.Detach = b }, func(c *config.Keybindings, s string) { c.Detach = s }, ResultDetached},
		{"approvals", func(k *PassthroughKeys, b PassthroughBinding) { k.Approvals = b }, func(c *config.Keybindings, s string) { c.Approvals = s }, ResultApprovalOverlay},
		{"session_list", func(k *PassthroughKeys, b PassthroughBinding) { k.SessionList = b }, func(c *config.Keybindings, s string) { c.SessionList = s }, ResultOverlay},
		{"messages", func(k *PassthroughKeys, b PassthroughBinding) { k.Messages = b }, func(c *config.Keybindings, s string) { c.Messages = s }, ResultMessageOverlay},
		{"shell", func(k *PassthroughKeys, b PassthroughBinding) { k.Shell = b }, func(c *config.Keybindings, s string) { c.Shell = s }, ResultShell},
		{"next_session", func(k *PassthroughKeys, b PassthroughBinding) { k.NextSession = b }, func(c *config.Keybindings, s string) { c.NextSession = s }, ResultNextSession},
		{"prev_session", func(k *PassthroughKeys, b PassthroughBinding) { k.PrevSession = b }, func(c *config.Keybindings, s string) { c.PrevSession = s }, ResultPrevSession},
		{"restart_session", func(k *PassthroughKeys, b PassthroughBinding) { k.RestartSession = b }, func(c *config.Keybindings, s string) { c.RestartSession = s }, ResultRestart},
		{"last_session", func(k *PassthroughKeys, b PassthroughBinding) { k.LastSession = b }, func(c *config.Keybindings, s string) { c.LastSession = s }, ResultLastSession},
		{"new_session", func(k *PassthroughKeys, b PassthroughBinding) { k.NewSession = b }, func(c *config.Keybindings, s string) { c.NewSession = s }, ResultNewSession},
		{"fork_session", func(k *PassthroughKeys, b PassthroughBinding) { k.ForkSession = b }, func(c *config.Keybindings, s string) { c.ForkSession = s }, ResultForkSession},
		{"orchestrator_session", func(k *PassthroughKeys, b PassthroughBinding) { k.OrchestratorSession = b }, func(c *config.Keybindings, s string) { c.OrchestratorSession = s }, ResultOrchestratorSession},
		{"rename_session", func(k *PassthroughKeys, b PassthroughBinding) { k.RenameSession = b }, func(c *config.Keybindings, s string) { c.RenameSession = s }, ResultRenameSession},
		{"scroll_mode", func(k *PassthroughKeys, b PassthroughBinding) { k.ScrollMode = b }, func(c *config.Keybindings, s string) { c.ScrollMode = s }, ResultScrollMode},
	}
}

// TestPassthroughActionMapping confirms every config action maps to the runtime
// result the precedence registry claims. This guards the registry itself, which
// TestConflictWinnerIsExecutedWinner then relies on.
func TestPassthroughActionMapping(t *testing.T) {
	for _, e := range prefixActionRegistry() {
		t.Run(e.label, func(t *testing.T) {
			keys := PassthroughKeys{Prefix: 0x02}
			e.setKey(&keys, NewPassthroughBinding('z'))

			got := runPrefixSequenceWithOpts(t, PassthroughOpts{Keys: keys}, []byte{'z'})
			if got != e.result {
				t.Errorf("prefix+z with %s bound = %d, want %d", e.label, got, e.result)
			}
		})
	}
}

// TestConflictWinnerIsExecutedWinner is the round-3 acceptance test (issue
// #1233): for every colliding pair of actions, the winner named by
// config.Keybindings.Conflicts() must be the action runPassthroughLoop actually
// executes. This ties the config-layer precedence order to the runtime switch,
// so reordering one without the other fails here.
func TestConflictWinnerIsExecutedWinner(t *testing.T) {
	reg := prefixActionRegistry()

	resultLabel := map[PassthroughResult]string{}
	for _, e := range reg {
		resultLabel[e.result] = e.label
	}

	for i := range reg {
		for j := i + 1; j < len(reg); j++ {
			a, b := reg[i], reg[j]

			t.Run(a.label+"_vs_"+b.label, func(t *testing.T) {
				// Executed winner: bind both to the same byte and see which fires.
				keys := PassthroughKeys{Prefix: 0x02}
				a.setKey(&keys, NewPassthroughBinding('x'))
				b.setKey(&keys, NewPassthroughBinding('x'))

				executed := resultLabel[runPrefixSequenceWithOpts(t, PassthroughOpts{Keys: keys}, []byte{'x'})]

				// Reported winner: build the equivalent config collision.
				var kb config.Keybindings
				a.setCfg(&kb, "x")
				b.setCfg(&kb, "x")

				conflicts := kb.Conflicts()
				if len(conflicts) != 1 {
					t.Fatalf("Conflicts() = %v, want exactly one collision", conflicts)
				}

				if !strings.Contains(conflicts[0], executed+" takes precedence") {
					t.Errorf("collision %q reports the wrong winner; runtime executes %q", conflicts[0], executed)
				}
			})
		}
	}
}
