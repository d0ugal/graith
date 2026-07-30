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
	return passthroughKeysFromKeybindings(cfg.Keybindings)
}

func passthroughKeysFromKeybindings(keybindings config.Keybindings) client.PassthroughKeys {
	return client.PassthroughKeys{
		Prefix:              parsePrefixKey(keybindings.Prefix),
		Detach:              parseKeyByte(keybindings.Detach),
		SessionNavigator:    parseKeyByte(keybindings.SessionNavigator),
		Shell:               parseKeyByte(keybindings.Shell),
		NextSession:         parseKeyByte(keybindings.NextSession),
		PrevSession:         parseKeyByte(keybindings.PrevSession),
		LastSession:         parseKeyByte(keybindings.LastSession),
		NewSession:          parseKeyByte(keybindings.NewSession),
		ForkSession:         parseKeyByte(keybindings.ForkSession),
		OrchestratorSession: parseKeyByte(keybindings.OrchestratorSession),
		RenameSession:       parseKeyByte(keybindings.RenameSession),
		ScrollMode:          parseKeyByte(keybindings.ScrollMode),
		Messages:            parseKeyByte(keybindings.Messages),
		RestartSession:      parseKeyByte(keybindings.RestartSession),
	}
}

// sessionNavigatorKeysFromConfig builds the Session Navigator keybindings from
// the [keybindings] config table.
func sessionNavigatorKeysFromConfig() client.SessionNavigatorKeys {
	return client.SessionNavigatorKeys{
		DeleteSession: navigatorActionKey(cfg.Keybindings.DeleteSession),
		ResumeSession: navigatorActionKey(cfg.Keybindings.ResumeSession),
		Search:        navigatorActionKey(cfg.Keybindings.Search),
		Cancel:        splitTUIKeysFromConfig(cfg.Keybindings.TUI.Cancel),
	}
}

func navigatorActionKey(value string) string {
	return config.NormalizeTUIKeyName(value)
}

func splitTUIKeysFromConfig(value string) []string {
	keys := client.SplitKeys(value)
	for i := range keys {
		keys[i] = config.NormalizeTUIKeyName(keys[i])
	}

	return keys
}

// overrideKeys replaces def with the parsed config value when it names at least
// one key, so an unset [keybindings.tui] field keeps its built-in default.
func overrideKeys(cfgVal string, def []string) []string {
	if ks := splitTUIKeysFromConfig(cfgVal); len(ks) > 0 {
		return ks
	}

	return def
}

// messageKeysFromConfig builds the message-viewer keybindings from config.
func messageKeysFromConfig() client.MessageKeys {
	tui := cfg.Keybindings.TUI
	k := client.DefaultMessageKeys()
	k.Up = overrideKeys(tui.Up, k.Up)
	k.Down = overrideKeys(tui.Down, k.Down)
	k.PageUp = overrideKeys(tui.PageUp, k.PageUp)
	k.PageDown = overrideKeys(tui.PageDown, k.PageDown)
	k.Top = overrideKeys(tui.Top, k.Top)
	k.Bottom = overrideKeys(tui.Bottom, k.Bottom)
	k.Pin = overrideKeys(tui.MessagePin, k.Pin)
	k.ExpandAll = overrideKeys(tui.MessageExpandAll, k.ExpandAll)
	k.CollapseAll = overrideKeys(tui.MessageCollapseAll, k.CollapseAll)
	k.NextConv = overrideKeys(tui.MessageNextConv, k.NextConv)
	k.PrevConv = overrideKeys(tui.MessagePrevConv, k.PrevConv)
	k.Topics = overrideKeys(tui.MessageTopics, k.Topics)
	k.Direct = overrideKeys(tui.MessageDirect, k.Direct)
	k.Cancel = overrideKeys(tui.Cancel, k.Cancel)

	return k
}

// scrollKeysFromConfig builds the scroll-pager keybindings from config.
func scrollKeysFromConfig() client.ScrollKeys {
	tui := cfg.Keybindings.TUI
	k := client.DefaultScrollKeys()
	k.Top = overrideKeys(tui.Top, k.Top)
	k.Bottom = overrideKeys(tui.Bottom, k.Bottom)
	k.Cancel = overrideKeys(tui.Cancel, k.Cancel)

	return k
}
