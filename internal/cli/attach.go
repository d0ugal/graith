package cli

import (
	"errors"
	"fmt"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

// attachYes skips the convert-to-interactive confirmation prompt for headless
// sessions (`gr attach --yes`). See runAttachByID / issue #1137.
var attachYes bool

// attachReadOnly requests a read-only attach (`gr attach --read-only`): PTY
// output streams but keyboard input is blocked, so an observer can watch a
// session without risk of injecting input (issue #31). It is a property of the
// whole attach invocation, so it rides every (re)attach in the loop.
var attachReadOnly bool

// attachMsg builds an AttachMsg carrying the invocation-wide read-only flag, so
// every (re)attach in the passthrough loop preserves the observer's mode.
func attachMsg(sessionID string) protocol.AttachMsg {
	return protocol.AttachMsg{SessionID: sessionID, ReadOnly: attachReadOnly}
}

var attachCmd = &cobra.Command{
	Use:               "attach [name-or-id]",
	Aliases:           []string{"a"},
	Short:             "Attach to a session",
	Args:              cobra.MaximumNArgs(1),
	ValidArgsFunction: completeSessionNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		name := ""
		if len(args) > 0 {
			name = args[0]
		}

		return runAttach(cmd, name)
	},
}

// registerAttachCmd registers this command on rootCmd. Called from registerCommands.
func registerAttachCmd() {
	attachCmd.Flags().BoolVarP(&attachYes, "yes", "y", false, "skip the convert-to-interactive confirmation when attaching to a headless session")
	attachCmd.Flags().BoolVar(&attachReadOnly, "read-only", false, "attach read-only: stream output but block keyboard input (prefix key still works)")
	rootCmd.AddCommand(attachCmd)
}

func runAttach(cmd *cobra.Command, name string) error {
	if isInsideGraith() {
		return errors.New("cannot attach from inside a graith session (nested sessions are not supported)")
	}

	c, err := client.Connect(cfg, paths, cfgFile)
	if err != nil {
		return err
	}
	defer c.Close()

	list, err := fetchSessionList(c, struct{}{})
	if err != nil {
		return err
	}

	if len(list.Sessions) == 0 {
		out.Printf("No sessions. Create one with: gr new <name>\n")
		return nil
	}

	if name == "" {
		return runAttachFromOverlay(c, list.Sessions)
	}

	for _, s := range list.Sessions {
		if s.Name == name || s.ID == name {
			return runAttachByID(c, s.ID, nil)
		}
	}

	return fmt.Errorf("session %q not found", name)
}

// runAttachFromOverlay opens the session picker with no session currently
// attached (the `gr attach` with no argument entry point), then attaches to the
// picked or newly created session.
func runAttachFromOverlay(c *client.Client, sessions []protocol.SessionInfo) error {
	repos := client.DiscoverRepos(cfg.AllowedRepoPaths, sessions)
	agents, defaultAgent := agentChoices()

	result := client.RunOverlay(client.RunOverlayOpts{
		Sessions:         sessions,
		CurrentSessionID: "",
		FetchPreview:     previewFetcher(),
		RefreshSessions:  sessionRefresher(),
		RefreshDeleted:   deletedRefresher(),
		DeleteSession:    deleteSession,
		RestartSession:   restartSession,
		StopSession:      stopSession,
		ToggleStar:       toggleStar,
		RestoreSession:   restoreSession,
		Profile:          paths.Profile,
		Collapsed:        nil,
		RepoSuggestions:  repos,
		ShortcutKeys:     cfg.Overlay.ShortcutKeys,
		Agents:           agents,
		DefaultAgent:     defaultAgent,
		Keys:             overlayKeysFromConfig(),
	})
	if result == nil || result.Action == "" {
		return nil
	}

	if result.Action == "create" {
		_ = c.SendControl("create", protocol.CreateMsg{
			Name:     result.CreateName,
			RepoPath: result.CreateRepoPath,
			Agent:    result.CreateAgent,
		})

		createResp, err := c.ReadControlResponse()
		if err != nil {
			return err
		}

		if createResp.Type == "error" {
			out.Printf("Create failed: %s\n", errorMessage(createResp))
			return nil
		}

		var newInfo protocol.SessionInfo

		_ = protocol.DecodePayload(createResp, &newInfo)

		return runAttachByID(c, newInfo.ID, result.Collapsed)
	}

	return runAttachByID(c, result.SessionID, result.Collapsed)
}

// terminalResetSequence is the escape-sequence blob that undoes terminal state
// agents (Claude Code, etc.) may have set during the session: alternate screen
// buffer, mouse tracking, bracketed paste, Kitty keyboard protocol, hidden
// cursor, scroll regions, and text attributes. Kept as a pure function so the
// exact sequence can be asserted in tests without driving a real terminal.
func terminalResetSequence() string {
	return "" +
		"\x1b[r" + // reset scroll region
		"\x1b[?1049l" + // leave alternate screen buffer
		"\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1006l" + // disable mouse tracking
		"\x1b[?1004l" + // disable focus event reporting
		"\x1b[?2004l" + // disable bracketed paste
		"\x1b[?1l\x1b>" + // leave application cursor keys and keypad mode
		"\x1b[<u" + // pop Kitty keyboard protocol
		"\x1b[0m" + // reset text attributes (bold, color, etc.)
		"\x1b[?25h" + // show cursor
		"\x1b[2J\x1b[H" // clear screen, cursor home
}

// resetTerminal writes terminalResetSequence to stdout. Safe to call on any
// terminal — unsupported sequences are ignored.
func resetTerminal() {
	fmt.Print(terminalResetSequence())
}

// restoreScreen repaints a session's last screen snapshot after an overlay or
// prompt drew over it. It is a package var so the attach-loop state helpers can
// be unit-tested without a live daemon; production always fetches the real
// snapshot.
var restoreScreen = func(sessionID string) {
	snap := client.FetchScreenSnapshot(cfg, paths, cfgFile, sessionID)
	client.WriteScreenRestore(snap)
}
