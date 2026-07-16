package cli

import (
	"errors"
	"os"
	"path/filepath"
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

		_ = c.SendControl("list", struct{}{})

		resp, err := c.ReadControlResponse()
		if err != nil {
			return err
		}

		var list protocol.SessionListMsg
		if err := protocol.DecodePayload(resp, &list); err != nil {
			return err
		}

		best := matchSession(cwd, list.Sessions)
		if best == nil {
			return errors.New("not inside a graith session worktree")
		}

		if jsonOutput {
			return out.JSON(*best)
		}

		out.Printf("Session:   %s (%s)\n", best.Name, best.ID)
		out.Printf("Agent:     %s\n", best.Agent)

		if best.Model != "" {
			out.Printf("Model:     %s\n", best.Model)
		}

		out.Printf("Repo:      %s\n", best.RepoName)
		out.Printf("Branch:    %s\n", best.Branch)
		out.Printf("Worktree:  %s\n", best.WorktreePath)
		out.Printf("Status:    %s\n", best.Status)

		return nil
	},
}

func matchSession(cwd string, sessions []protocol.SessionInfo) *protocol.SessionInfo {
	cwd = filepath.Clean(cwd)

	var best *protocol.SessionInfo

	for i, s := range sessions {
		wt := filepath.Clean(s.WorktreePath)
		if cwd != wt && !strings.HasPrefix(cwd, wt+string(filepath.Separator)) {
			continue
		}

		if best == nil || len(wt) > len(filepath.Clean(best.WorktreePath)) {
			best = &sessions[i]
		}
	}

	return best
}

// registerInfoCmd registers this command on rootCmd. Called from registerCommands.
func registerInfoCmd() {
	rootCmd.AddCommand(infoCmd)
}
