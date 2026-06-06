package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

var infoCmd = &cobra.Command{
	Use:   "info",
	Short: "Show current session info",
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, _ := os.Getwd()

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

		for _, s := range list.Sessions {
			if strings.HasPrefix(cwd, s.WorktreePath) {
				if jsonOutput {
					return out.JSON(s)
				}
				out.Print("Session:   %s (%s)\n", s.Name, s.ID)
				out.Print("Agent:     %s\n", s.Agent)
				out.Print("Repo:      %s\n", s.RepoName)
				out.Print("Branch:    %s\n", s.Branch)
				out.Print("Worktree:  %s\n", s.WorktreePath)
				out.Print("Status:    %s\n", s.Status)
				return nil
			}
		}

		return fmt.Errorf("not inside a graith session worktree")
	},
}

func init() {
	rootCmd.AddCommand(infoCmd)
}
