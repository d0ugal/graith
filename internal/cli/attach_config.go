package cli

import (
	"os"
	"sort"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/config"
)

func isInsideGraith() bool {
	return os.Getenv("GRAITH_ATTACHED") != "" || os.Getenv("GRAITH_SESSION_ID") != ""
}

// agentChoices returns the configured agent names (sorted, with the default
// first) and the default agent, for use by the interactive create form.
func agentChoices() ([]string, string) {
	return orderAgents(cfg.Agents, cfg.DefaultAgent), cfg.DefaultAgent
}

// orderAgents returns the agent names sorted alphabetically, with def hoisted to
// the front when it is present in the map. A def that is empty or absent leaves
// the list in plain sorted order.
func orderAgents(agents map[string]config.Agent, def string) []string {
	names := make([]string, 0, len(agents))
	for name := range agents {
		names = append(names, name)
	}

	sort.Strings(names)

	if def != "" {
		for i, n := range names {
			if n == def {
				names = append(names[:i], names[i+1:]...)
				names = append([]string{def}, names...)

				break
			}
		}
	}

	return names
}

// passthroughKeysFromConfig builds the prefix-action keybindings for the attach
// passthrough loop from the [keybindings] config table.
func passthroughKeysFromConfig() client.PassthroughKeys {
	return client.PassthroughKeys{
		Prefix:              parsePrefixKey(cfg.Keybindings.Prefix),
		Detach:              parseKeyByte(cfg.Keybindings.Detach),
		SessionList:         parseKeyByte(cfg.Keybindings.SessionList),
		Shell:               parseKeyByte(cfg.Keybindings.Shell),
		NextSession:         parseKeyByte(cfg.Keybindings.NextSession),
		PrevSession:         parseKeyByte(cfg.Keybindings.PrevSession),
		LastSession:         parseKeyByte(cfg.Keybindings.LastSession),
		NewSession:          parseKeyByte(cfg.Keybindings.NewSession),
		ForkSession:         parseKeyByte(cfg.Keybindings.ForkSession),
		OrchestratorSession: parseKeyByte(cfg.Keybindings.OrchestratorSession),
		RenameSession:       parseKeyByte(cfg.Keybindings.RenameSession),
		ScrollMode:          parseKeyByte(cfg.Keybindings.ScrollMode),
	}
}

// overlayKeysFromConfig builds the session-picker keybindings from the
// [keybindings] config table.
func overlayKeysFromConfig() client.OverlayKeys {
	return client.OverlayKeys{
		DeleteSession: cfg.Keybindings.DeleteSession,
		ResumeSession: cfg.Keybindings.ResumeSession,
		Search:        cfg.Keybindings.Search,
	}
}
