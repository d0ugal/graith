package cli

import (
	"fmt"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

var stopBatch batchFlags

var stopCmd = &cobra.Command{
	Use:   "stop <name-or-id>",
	Short: "Stop a running session without deleting it",
	Args: func(cmd *cobra.Command, args []string) error {
		if stopBatch.active() {
			return cobra.NoArgs(cmd, args)
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

		sessionID, err := resolveSession(c, args[0])
		if err != nil {
			return err
		}

		c.SendControl("stop", protocol.StopMsg{SessionID: sessionID})
		resp, err := c.ReadControlResponse()
		if err != nil {
			return err
		}
		if resp.Type == "error" {
			var e protocol.ErrorMsg
			protocol.DecodePayload(resp, &e)
			return fmt.Errorf("%s", e.Message)
		}

		out.Print("Session stopped (worktree preserved)\n")
		return nil
	},
}

func stopBatchRun(cmd *cobra.Command) error {
	c, err := client.Connect(cfg, paths, cfgFile)
	if err != nil {
		return err
	}
	defer c.Close()

	c.SendControl("list", struct{}{})
	resp, err := c.ReadControlResponse()
	if err != nil {
		return err
	}
	var list protocol.SessionListMsg
	if err := protocol.DecodePayload(resp, &list); err != nil {
		return err
	}

	matched, err := filterSessions(list.Sessions, &stopBatch)
	if err != nil {
		return err
	}
	if len(matched) == 0 {
		out.Print("No sessions match the given filters\n")
		return nil
	}

	if !stopBatch.force {
		confirmed, err := confirmBatch(cmd, "stop", "stopped", matched)
		if err != nil {
			return err
		}
		if !confirmed {
			return nil
		}
	}

	for _, s := range matched {
		c.SendControl("stop", protocol.StopMsg{SessionID: s.ID})
		resp, err := c.ReadControlResponse()
		if err != nil {
			return err
		}
		if resp.Type == "error" {
			var e protocol.ErrorMsg
			protocol.DecodePayload(resp, &e)
			return fmt.Errorf("stopping %s: %s", s.Name, e.Message)
		}
	}

	out.Print("Stopped %d sessions\n", len(matched))
	return nil
}

func init() {
	addBatchFlags(stopCmd, &stopBatch)
	rootCmd.AddCommand(stopCmd)
}
