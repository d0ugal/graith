package cli

import (
	"fmt"
	"os"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

var (
	restartBackground bool
	restartChildren   bool
)

var restartCmd = &cobra.Command{
	Use:   "restart <name-or-id>",
	Short: "Restart a session (with --children, restarts all descendants)",
	Args: func(cmd *cobra.Command, args []string) error {
		if restartChildren {
			return cobra.MaximumNArgs(1)(cmd, args)
		}

		return cobra.ExactArgs(1)(cmd, args)
	},
	ValidArgsFunction: completeSessionNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := client.Connect(cfg, paths, cfgFile)
		if err != nil {
			return err
		}
		defer c.Close()

		if restartChildren {
			return restartChildrenRun(c, args)
		}

		session, err := resolveSessionInfo(c, args[0])
		if err != nil {
			return err
		}

		if session.Status == "running" {
			return fmt.Errorf("session %q is already running", session.Name)
		}

		c.SendControl("resume", protocol.ResumeMsg{SessionID: session.ID})

		resp, err := c.ReadControlResponse()
		if err != nil {
			return err
		}

		if resp.Type == "error" {
			var e protocol.ErrorMsg
			protocol.DecodePayload(resp, &e)

			return fmt.Errorf("%s", e.Message)
		}

		var info protocol.SessionInfo
		protocol.DecodePayload(resp, &info)

		if jsonOutput {
			return out.JSON(info)
		}

		out.Printf("Session %s restarted\n", info.Name)

		if restartBackground {
			return nil
		}

		return runAttachByID(c, info.ID, nil)
	},
}

func restartChildrenRun(c *client.Client, args []string) error {
	var (
		sessionID   string
		excludeRoot bool
	)

	if len(args) == 1 {
		var err error

		sessionID, err = resolveSession(c, args[0])
		if err != nil {
			return err
		}

		excludeRoot = false
	} else {
		sessionID = os.Getenv("GRAITH_SESSION_ID")
		if sessionID == "" {
			return fmt.Errorf("--children with no session arg requires GRAITH_SESSION_ID to be set")
		}

		excludeRoot = true
	}

	c.SendControl("restart", protocol.RestartMsg{
		SessionID:   sessionID,
		Children:    true,
		ExcludeRoot: excludeRoot,
	})

	resp, err := c.ReadControlResponse()
	if err != nil {
		return err
	}

	if resp.Type == "error" {
		var e protocol.ErrorMsg
		protocol.DecodePayload(resp, &e)

		return fmt.Errorf("%s", e.Message)
	}

	var result struct {
		Restarted []string `json:"restarted"`
	}
	protocol.DecodePayload(resp, &result)

	if jsonOutput {
		return out.JSON(result)
	}

	out.Printf("Restarted %d sessions\n", len(result.Restarted))

	return nil
}

func init() {
	rootCmd.AddCommand(restartCmd)
	restartCmd.Flags().BoolVar(&restartBackground, "background", false, "restart without attaching")
	restartCmd.Flags().BoolVar(&restartChildren, "children", false, "restart all descendant sessions")
}
