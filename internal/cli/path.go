package cli

import (
	"fmt"

	"github.com/d0ugal/graith/internal/client"
	"github.com/spf13/cobra"
)

var pathCmd = &cobra.Command{
	Use:               "path <name-or-id>",
	Short:             "Print the worktree path for a session",
	Long:              `Print the worktree path for a session. Use with cd: cd "$(gr path <name>)"`,
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeSessionNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := client.Connect(cfg, paths, cfgFile)
		if err != nil {
			return err
		}
		defer c.Close()

		session, err := resolveSessionInfo(c, args[0])
		if err != nil {
			return err
		}

		if session.WorktreePath == "" {
			return fmt.Errorf("session %q has no worktree path", args[0])
		}

		if out.IsJSON() {
			return out.JSON(map[string]string{
				"session_id":    session.ID,
				"name":          session.Name,
				"worktree_path": session.WorktreePath,
			})
		}

		fmt.Print(session.WorktreePath)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(pathCmd)
}
