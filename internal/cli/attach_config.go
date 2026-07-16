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
		Messages:            parseKeyByte(cfg.Keybindings.Messages),
		Approvals:           parseKeyByte(cfg.Keybindings.Approvals),
		RestartSession:      parseKeyByte(cfg.Keybindings.RestartSession),
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

// overrideKeys replaces def with the parsed config value when it names at least
// one key, so an unset [keybindings.overlay] field keeps its built-in default.
func overrideKeys(cfgVal string, def []string) []string {
	if ks := client.SplitKeys(cfgVal); len(ks) > 0 {
		return ks
	}

	return def
}

// dashboardKeysFromConfig builds the dashboard TUI keybindings from the
// [keybindings.overlay] config table, falling back to the defaults per key.
func dashboardKeysFromConfig() client.DashboardKeys {
	ov := cfg.Keybindings.Overlay
	k := client.DefaultDashboardKeys()
	k.Up = overrideKeys(ov.Up, k.Up)
	k.Down = overrideKeys(ov.Down, k.Down)
	k.Attach = overrideKeys(ov.DashboardAttach, k.Attach)
	k.Stop = overrideKeys(ov.DashboardStop, k.Stop)
	k.Delete = overrideKeys(ov.DashboardDelete, k.Delete)
	k.Resume = overrideKeys(ov.DashboardResume, k.Resume)
	k.Confirm = overrideKeys(ov.Confirm, k.Confirm)
	k.Cancel = overrideKeys(ov.Cancel, k.Cancel)

	return k
}

// approvalKeysFromConfig builds the approval-overlay keybindings from config.
func approvalKeysFromConfig() client.ApprovalKeys {
	ov := cfg.Keybindings.Overlay
	k := client.DefaultApprovalKeys()
	k.Up = overrideKeys(ov.Up, k.Up)
	k.Down = overrideKeys(ov.Down, k.Down)
	k.Allow = overrideKeys(ov.ApprovalAllow, k.Allow)
	k.Deny = overrideKeys(ov.ApprovalDeny, k.Deny)
	k.AllowAll = overrideKeys(ov.ApprovalAllowAll, k.AllowAll)
	k.Cancel = overrideKeys(ov.Cancel, k.Cancel)

	return k
}

// messageKeysFromConfig builds the message-viewer keybindings from config.
func messageKeysFromConfig() client.MessageKeys {
	ov := cfg.Keybindings.Overlay
	k := client.DefaultMessageKeys()
	k.Up = overrideKeys(ov.Up, k.Up)
	k.Down = overrideKeys(ov.Down, k.Down)
	k.PageUp = overrideKeys(ov.PageUp, k.PageUp)
	k.PageDown = overrideKeys(ov.PageDown, k.PageDown)
	k.Top = overrideKeys(ov.Top, k.Top)
	k.Bottom = overrideKeys(ov.Bottom, k.Bottom)
	k.Pin = overrideKeys(ov.MessagePin, k.Pin)
	k.ExpandAll = overrideKeys(ov.MessageExpandAll, k.ExpandAll)
	k.CollapseAll = overrideKeys(ov.MessageCollapseAll, k.CollapseAll)
	k.NextConv = overrideKeys(ov.MessageNextConv, k.NextConv)
	k.PrevConv = overrideKeys(ov.MessagePrevConv, k.PrevConv)
	k.Cancel = overrideKeys(ov.Cancel, k.Cancel)

	return k
}

// scrollKeysFromConfig builds the scroll-pager keybindings from config.
func scrollKeysFromConfig() client.ScrollKeys {
	ov := cfg.Keybindings.Overlay
	k := client.DefaultScrollKeys()
	k.Top = overrideKeys(ov.Top, k.Top)
	k.Bottom = overrideKeys(ov.Bottom, k.Bottom)
	k.Cancel = overrideKeys(ov.Cancel, k.Cancel)

	return k
}
