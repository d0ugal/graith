package cli

import (
	"fmt"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/daemon"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

var (
	forkBackground bool
)

var forkCmd = &cobra.Command{
	Use:               "fork <source-session> <new-name>",
	Short:             "Fork a session (new worktree + agent conversation history)",
	Args:              cobra.ExactArgs(2),
	ValidArgsFunction: completeSessionNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		sourceName := args[0]

		newName := args[1]
		if err := daemon.ValidateSessionName(newName); err != nil {
			return err
		}

		c, err := client.Connect(cfg, paths, cfgFile)
		if err != nil {
			return err
		}
		defer c.Close()

		c.SendControl("list", struct{}{})

		listResp, err := c.ReadControlResponse()
		if err != nil {
			return err
		}

		var list protocol.SessionListMsg
		if err := protocol.DecodePayload(listResp, &list); err != nil {
			return err
		}

		var sourceID string

		for _, s := range list.Sessions {
			if s.Name == sourceName || s.ID == sourceName {
				sourceID = s.ID
				break
			}
		}

		if sourceID == "" {
			return fmt.Errorf("session %q not found", sourceName)
		}

		c.SendControl("fork", protocol.ForkMsg{
			Name:            newName,
			SourceSessionID: sourceID,
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

		var info protocol.SessionInfo
		protocol.DecodePayload(resp, &info)

		if jsonOutput {
			return out.JSON(info)
		}

		out.Printf("Forked session %s → %s (%s) in %s\n", sourceName, info.Name, info.ID, info.WorktreePath)

		if forkBackground {
			return nil
		}

		return runAttachByID(c, info.ID, nil)
	},
}

func init() {
	rootCmd.AddCommand(forkCmd)
	forkCmd.Flags().BoolVar(&forkBackground, "background", false, "fork without attaching")
}
