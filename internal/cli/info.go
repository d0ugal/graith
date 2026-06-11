package cli

import (
	"fmt"
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

		c.SendControl("list", struct{}{})
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
			return fmt.Errorf("not inside a graith session worktree")
		}

		if jsonOutput {
			return out.JSON(*best)
		}
		out.Print("Session:   %s (%s)\n", best.Name, best.ID)
		out.Print("Agent:     %s\n", best.Agent)
		out.Print("Repo:      %s\n", best.RepoName)
		out.Print("Branch:    %s\n", best.Branch)
		out.Print("Worktree:  %s\n", best.WorktreePath)
		out.Print("Status:    %s\n", best.Status)
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

func init() {
	rootCmd.AddCommand(infoCmd)
}
