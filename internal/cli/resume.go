package cli

import (
	"fmt"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

var resumeAttach bool

var resumeCmd = &cobra.Command{
	Use:               "resume <name-or-id>",
	Short:             "Resume a stopped session without attaching",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeSessionNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		if resumeAttach && jsonOutput {
			return fmt.Errorf("--attach cannot be combined with --json (attach enters interactive passthrough)")
		}

		c, err := client.Connect(cfg, paths, cfgFile)
		if err != nil {
			return err
		}
		defer c.Close()

		session, err := resolveSessionInfo(c, args[0])
		if err != nil {
			return err
		}

		if err := resumeStatusErr(session.Name, session.Status); err != nil {
			return err
		}

		_ = c.SendControl("resume", protocol.ResumeMsg{SessionID: session.ID})

		resp, err := c.ReadControlResponse()
		if err != nil {
			return err
		}

		if resp.Type == "error" {
			var e protocol.ErrorMsg

			_ = protocol.DecodePayload(resp, &e)

			return fmt.Errorf("%s", e.Message)
		}

		var info protocol.SessionInfo

		_ = protocol.DecodePayload(resp, &info)

		if jsonOutput {
			return out.JSON(info)
		}

		out.Printf("Session %s resumed\n", info.Name)

		if resumeAttach {
			return runAttachByID(c, info.ID, nil)
		}

		return nil
	},
}

// resumeStatusErr rejects a resume when the session is already running. The
// daemon treats resume of a running session as a silent no-op, so the guard
// lives here to give the user a clear message instead.
func resumeStatusErr(name, status string) error {
	if status == "running" {
		return fmt.Errorf("session %q is already running", name)
	}

	return nil
}

// registerResumeCmd registers this command on rootCmd. Called from registerCommands.
func registerResumeCmd() {
	resumeCmd.Flags().BoolVar(&resumeAttach, "attach", false, "attach to the session after resuming")
	rootCmd.AddCommand(resumeCmd)
}
