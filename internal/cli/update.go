package cli

import (
	"errors"
	"fmt"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

var updateCmd = &cobra.Command{
	Use:               "update <name-or-id>",
	Short:             "Update session properties",
	Long:              "Update session properties such as name and parent. Use --parent \"\" to orphan a session.",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeSessionNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		nameFlag, _ := cmd.Flags().GetString("name")
		parentFlag, _ := cmd.Flags().GetString("parent")
		nameSet := cmd.Flags().Changed("name")
		parentSet := cmd.Flags().Changed("parent")

		if !nameSet && !parentSet {
			return errors.New("at least one of --name or --parent must be specified")
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

		msg := protocol.UpdateMsg{SessionID: sessionID}
		if nameSet {
			msg.Name = &nameFlag
		}

		if parentSet {
			if parentFlag == "" {
				msg.ParentID = &parentFlag
			} else {
				parentSessionID, err := resolveSession(c, parentFlag)
				if err != nil {
					return fmt.Errorf("resolving parent: %w", err)
				}

				msg.ParentID = &parentSessionID
			}
		}

		_ = c.SendControl("update", msg)

		resp, err := c.ReadControlResponse()
		if err != nil {
			return err
		}

		if resp.Type == "error" {
			var e protocol.ErrorMsg

			_ = protocol.DecodePayload(resp, &e)

			return fmt.Errorf("%s", e.Message)
		}

		if nameSet {
			out.Printf("Name updated to %s\n", nameFlag)
		}

		if parentSet {
			if parentFlag == "" {
				out.Printf("Parent removed\n")
			} else {
				out.Printf("Parent set to %s\n", parentFlag)
			}
		}

		return nil
	},
}

// registerUpdateCmd registers this command on rootCmd. Called from registerCommands.
func registerUpdateCmd() {
	updateCmd.Flags().String("name", "", "new session name")
	updateCmd.Flags().String("parent", "", "new parent session (empty string to orphan)")
	rootCmd.AddCommand(updateCmd)
}
