package cli

import (
	"reflect"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
)

func TestOrderAgents(t *testing.T) {
	agents := map[string]config.Agent{
		"codex":  {},
		"claude": {},
		"cursor": {},
	}

	tests := []struct {
		name   string
		agents map[string]config.Agent
		def    string
		want   []string
	}{
		{
			name:   "sorted with default hoisted to front",
			agents: agents,
			def:    "cursor",
			want:   []string{"cursor", "claude", "codex"},
		},
		{
			name:   "default already first stays first",
			agents: agents,
			def:    "claude",
			want:   []string{"claude", "codex", "cursor"},
		},
		{
			name:   "empty default leaves plain sorted order",
			agents: agents,
			def:    "",
			want:   []string{"claude", "codex", "cursor"},
		},
		{
			name:   "default absent from map leaves plain sorted order",
			agents: agents,
			def:    "thrawn",
			want:   []string{"claude", "codex", "cursor"},
		},
		{
			name:   "empty map yields empty list",
			agents: map[string]config.Agent{},
			def:    "claude",
			want:   []string{},
		},
		{
			name:   "single agent",
			agents: map[string]config.Agent{"claude": {}},
			def:    "claude",
			want:   []string{"claude"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := orderAgents(tt.agents, tt.def)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("orderAgents(%v, %q) = %v, want %v", tt.agents, tt.def, got, tt.want)
			}
		})
	}
}

// TestIsInsideGraithCov covers the two env vars that mark a nested session, and
// the negative case where neither is set.
func TestIsInsideGraithCov(t *testing.T) {
	// t.Setenv restores the prior value automatically at test end.
	t.Run("neither set", func(t *testing.T) {
		t.Setenv("GRAITH_ATTACHED", "")
		t.Setenv("GRAITH_SESSION_ID", "")

		if isInsideGraith() {
			t.Error("expected false when no graith env vars are set")
		}
	})

	t.Run("attached marker set", func(t *testing.T) {
		t.Setenv("GRAITH_SESSION_ID", "")
		t.Setenv("GRAITH_ATTACHED", "1")

		if !isInsideGraith() {
			t.Error("expected true when GRAITH_ATTACHED is set")
		}
	})

	t.Run("session id set", func(t *testing.T) {
		t.Setenv("GRAITH_ATTACHED", "")
		t.Setenv("GRAITH_SESSION_ID", "braw-123")

		if !isInsideGraith() {
			t.Error("expected true when GRAITH_SESSION_ID is set")
		}
	})
}

// TestAgentChoicesCov verifies agentChoices threads the configured agents and
// default through orderAgents, hoisting the default to the front.
func TestAgentChoicesCov(t *testing.T) {
	oldCfg := cfg

	t.Cleanup(func() { cfg = oldCfg })

	cfg = &config.Config{
		DefaultAgent: "codex",
		Agents: map[string]config.Agent{
			"claude": {},
			"codex":  {},
			"cursor": {},
		},
	}

	names, def := agentChoices()

	if def != "codex" {
		t.Errorf("default agent = %q, want codex", def)
	}

	want := []string{"codex", "claude", "cursor"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("agentChoices names = %v, want %v", names, want)
	}
}

// TestPassthroughKeysFromConfig verifies the [keybindings] config maps into
// PassthroughKeys, including the detach/session_list/shell keys wired for #918.
func TestPassthroughKeysFromConfig(t *testing.T) {
	oldCfg := cfg

	t.Cleanup(func() { cfg = oldCfg })

	cfg = &config.Config{
		Keybindings: config.Keybindings{
			Prefix:              "ctrl+a",
			Detach:              "q",
			SessionList:         "z",
			Shell:               "v",
			NextSession:         "n",
			PrevSession:         "p",
			LastSession:         "l",
			NewSession:          "c",
			ForkSession:         "f",
			OrchestratorSession: "o",
		},
	}

	keys := passthroughKeysFromConfig()

	want := client.PassthroughKeys{
		Prefix:              0x01, // ctrl+a
		Detach:              'q',
		SessionList:         'z',
		Shell:               'v',
		NextSession:         'n',
		PrevSession:         'p',
		LastSession:         'l',
		NewSession:          'c',
		ForkSession:         'f',
		OrchestratorSession: 'o',
	}
	if keys != want {
		t.Errorf("passthroughKeysFromConfig() = %+v, want %+v", keys, want)
	}
}

// TestRemotePassthroughKeysFromConfig verifies remote attach still wires
// session_list and shell (regression for the #918 review): without them,
// prefix+w / prefix+s forward raw bytes to the remote agent instead of hitting
// the "not yet supported — detaching" notice.
func TestRemotePassthroughKeysFromConfig(t *testing.T) {
	oldCfg := cfg

	t.Cleanup(func() { cfg = oldCfg })

	cfg = &config.Config{
		Keybindings: config.Keybindings{
			Prefix:      "ctrl+b",
			Detach:      "d",
			SessionList: "w",
			Shell:       "s",
			NextSession: "n",
			PrevSession: "p",
		},
	}

	keys := remotePassthroughKeysFromConfig()

	if keys.SessionList != 'w' {
		t.Errorf("remote SessionList = %q, want 'w'", keys.SessionList)
	}

	if keys.Shell != 's' {
		t.Errorf("remote Shell = %q, want 's'", keys.Shell)
	}

	if keys.Detach != 'd' || keys.Prefix != 0x02 {
		t.Errorf("remote Detach/Prefix = %q/%#x, want 'd'/0x02", keys.Detach, keys.Prefix)
	}
}

// TestOverlayKeysFromConfig verifies the picker keybindings map into OverlayKeys.
func TestOverlayKeysFromConfig(t *testing.T) {
	oldCfg := cfg

	t.Cleanup(func() { cfg = oldCfg })

	cfg = &config.Config{
		Keybindings: config.Keybindings{
			DeleteSession: "z",
			ResumeSession: "Z",
			Search:        "?",
		},
	}

	keys := overlayKeysFromConfig()

	want := client.OverlayKeys{DeleteSession: "z", ResumeSession: "Z", Search: "?"}
	if keys != want {
		t.Errorf("overlayKeysFromConfig() = %+v, want %+v", keys, want)
	}
}

// TestSortedSessionIDsCov checks that sessions are grouped by repo name
// (alphabetically, with the empty repo bucketed as "(no repo)") and ordered
// within each group by running-status-first then name.
func TestSortedSessionIDsCov(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "b1", Name: "zephyr", RepoName: "bothy", Status: "stopped"},
		{ID: "b2", Name: "alpha", RepoName: "bothy", Status: "running"},
		{ID: "c1", Name: "gamma", RepoName: "croft", Status: "running"},
		{ID: "n1", Name: "naught", RepoName: ""},
	}

	got := sortedSessionIDs(sessions, "b2")

	// "(no repo)" sorts first (paren < letters), then bothy, then croft.
	// Within bothy: running (alpha=b2) before stopped (zephyr=b1).
	want := []string{"n1", "b2", "b1", "c1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sortedSessionIDs = %v, want %v", got, want)
	}
}

func TestSortedSessionIDsEmptyCov(t *testing.T) {
	if got := sortedSessionIDs(nil, ""); got != nil {
		t.Errorf("expected nil for empty input, got %v", got)
	}
}

// TestAdjacentSessionCov covers forward/backward navigation with wraparound,
// the too-few-sessions guard, and an unknown current id.
func TestAdjacentSessionCov(t *testing.T) {
	ids := []string{"canny", "braw", "thrawn"}

	tests := []struct {
		name    string
		ids     []string
		current string
		forward bool
		want    string
	}{
		{"forward middle", ids, "canny", true, "braw"},
		{"forward wraps to first", ids, "thrawn", true, "canny"},
		{"backward middle", ids, "braw", false, "canny"},
		{"backward wraps to last", ids, "canny", false, "thrawn"},
		{"current not found", ids, "haar", true, ""},
		{"single session", []string{"only"}, "only", true, ""},
		{"empty list", nil, "any", false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := adjacentSession(tt.ids, tt.current, tt.forward); got != tt.want {
				t.Errorf("adjacentSession(%v, %q, %v) = %q, want %q",
					tt.ids, tt.current, tt.forward, got, tt.want)
			}
		})
	}
}

// TestRunAttachRejectsInsideGraithCov verifies attach refuses to start a nested
// session before it ever touches the daemon socket.
func TestRunAttachRejectsInsideGraithCov(t *testing.T) {
	t.Setenv("GRAITH_SESSION_ID", "ben-parent")

	err := runAttach(attachCmd, "bairn")
	if err == nil {
		t.Fatal("expected error attaching from inside a graith session")
	}

	if !strings.Contains(err.Error(), "nested sessions are not supported") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestRunAttachByIDRejectsInsideGraithCov is the same guard on the direct-by-ID
// path, which callers reach after picking a session in the overlay.
func TestRunAttachByIDRejectsInsideGraithCov(t *testing.T) {
	t.Setenv("GRAITH_ATTACHED", "1")

	err := runAttachByID(nil, "braw-999", nil)
	if err == nil {
		t.Fatal("expected error attaching by ID from inside a graith session")
	}

	if !strings.Contains(err.Error(), "nested sessions are not supported") {
		t.Errorf("unexpected error: %v", err)
	}
}
