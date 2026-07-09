package cli

import (
	"fmt"
	"os"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

var (
	stopBatch    batchFlags
	stopChildren bool
)

var stopCmd = &cobra.Command{
	Use:   "stop <name-or-id>",
	Short: "Stop a running session without deleting it",
	Args: func(cmd *cobra.Command, args []string) error {
		if stopChildren && stopBatch.active() {
			return fmt.Errorf("--children cannot be combined with batch filters")
		}

		if stopBatch.active() {
			return cobra.NoArgs(cmd, args)
		}

		if stopChildren {
			return cobra.MaximumNArgs(1)(cmd, args)
		}

		return cobra.ExactArgs(1)(cmd, args)
	},
	ValidArgsFunction: completeSessionNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		if stopBatch.active() {
			return stopBatchRun(cmd)
		}

		c, err := client.Connect(cfg, paths, cfgFile)
		if err != nil {
			return err
		}
		defer c.Close()

		if stopChildren {
			return stopChildrenRun(c, args)
		}

		sessionID, err := resolveSession(c, args[0])
		if err != nil {
			return err
		}

		_ = c.SendControl("stop", protocol.StopMsg{SessionID: sessionID})

		resp, err := c.ReadControlResponse()
		if err != nil {
			return err
		}

		if resp.Type == "error" {
			var e protocol.ErrorMsg

			_ = protocol.DecodePayload(resp, &e)

			return fmt.Errorf("%s", e.Message)
		}

		out.Printf("Session stopped (worktree preserved)\n")

		return nil
	},
}

func stopChildrenRun(c *client.Client, args []string) error {
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

	_ = c.SendControl("stop", protocol.StopMsg{
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

		_ = protocol.DecodePayload(resp, &e)

		return fmt.Errorf("%s", e.Message)
	}

	var result struct {
		Stopped []string `json:"stopped"`
	}

	_ = protocol.DecodePayload(resp, &result)
	out.Printf("Stopped %d sessions\n", len(result.Stopped))

	return nil
}

func stopBatchRun(cmd *cobra.Command) error {
	return runBatch(cmd, &stopBatch, "stop", "stopped", "stopping", "stop",
		func(sessionID string) any {
			return protocol.StopMsg{SessionID: sessionID}
		}, stopNoOpSkip)
}

// registerStopCmd registers this command on rootCmd. Called from registerCommands.
func registerStopCmd() {
	addBatchFlags(stopCmd, &stopBatch)
	stopCmd.Flags().BoolVar(&stopChildren, "children", false, "also stop all descendant sessions")
	rootCmd.AddCommand(stopCmd)
}
