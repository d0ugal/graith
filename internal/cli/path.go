package cli

import (
	"fmt"
	"io"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/output"
	"github.com/d0ugal/graith/internal/protocol"
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

		return printPath(cmd.OutOrStdout(), out, session, args[0])
	},
}

func printPath(w io.Writer, o *output.Writer, session *protocol.SessionInfo, nameOrID string) error {
	if session.WorktreePath == "" {
		return fmt.Errorf("session %q has no worktree path", nameOrID)
	}

	if o.IsJSON() {
		return o.JSON(map[string]string{
			"session_id":    session.ID,
			"name":          session.Name,
			"worktree_path": session.WorktreePath,
		})
	}

	_, _ = fmt.Fprint(w, session.WorktreePath)

	return nil
}

// registerPathCmd registers this command on rootCmd. Called from registerCommands.
func registerPathCmd() {
	rootCmd.AddCommand(pathCmd)
}
