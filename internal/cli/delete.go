package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/git"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var deleteBatch batchFlags

var deleteCmd = &cobra.Command{
	Use:     "delete <name-or-id>",
	Aliases: []string{"rm"},
	Short:   "Delete a session by name or ID",
	Args: func(cmd *cobra.Command, args []string) error {
		if deleteBatch.active() {
			return cobra.NoArgs(cmd, args)
		}
		return cobra.ExactArgs(1)(cmd, args)
	},
	ValidArgsFunction: completeSessionNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		if deleteBatch.active() {
			return deleteBatchRun(cmd)
		}

		c, err := client.Connect(cfg, paths, cfgFile)
		if err != nil {
			return err
		}
		defer c.Close()

		session, err := resolveSessionInfo(c, args[0])
		if err != nil {
			return err
		}

		if !deleteBatch.force && session.WorktreePath != "" && !session.InPlace {
			confirmed, err := confirmDelete(session)
			if err != nil {
				return err
			}
			if !confirmed {
				return nil
			}
		}

		c.SendControl("delete", protocol.DeleteMsg{SessionID: session.ID})
		resp, err := c.ReadControlResponse()
		if err != nil {
			return err
		}
		if resp.Type == "error" {
			var e protocol.ErrorMsg
			protocol.DecodePayload(resp, &e)
			return fmt.Errorf("%s", e.Message)
		}

		out.Print("Session deleted\n")
		return nil
	},
}

func resolveSessionInfo(c *client.Client, nameOrID string) (*protocol.SessionInfo, error) {
	c.SendControl("list", struct{}{})
	resp, err := c.ReadControlResponse()
	if err != nil {
		return nil, err
	}
	var list protocol.SessionListMsg
	if err := protocol.DecodePayload(resp, &list); err != nil {
		return nil, err
	}
	for _, s := range list.Sessions {
		if s.Name == nameOrID || s.ID == nameOrID {
			return &s, nil
		}
	}
	return nil, fmt.Errorf("session %q not found", nameOrID)
}

type repoStatus struct {
	name            string
	dirtyFiles      []string
	unpushedCommits []string
	gitFailed       bool
}

func confirmDelete(session *protocol.SessionInfo) (bool, error) {
	var repos []repoStatus

	mainDirty, mainDirtyErr := git.DirtyFiles(session.WorktreePath)
	mainUnpushed, mainUnpushedErr := git.UnpushedCommitSummaries(session.WorktreePath, session.BaseBranch)
	repos = append(repos, repoStatus{
		name:            session.RepoName,
		dirtyFiles:      mainDirty,
		unpushedCommits: mainUnpushed,
		gitFailed:       mainDirtyErr != nil || (session.BaseBranch != "" && mainUnpushedErr != nil),
	})

	for _, inc := range session.Includes {
		incDirty, incDirtyErr := git.DirtyFiles(inc.WorktreePath)
		incUnpushed, incUnpushedErr := git.UnpushedCommitSummaries(inc.WorktreePath, inc.BaseBranch)
		repos = append(repos, repoStatus{
			name:            inc.RepoName,
			dirtyFiles:      incDirty,
			unpushedCommits: incUnpushed,
			gitFailed:       incDirtyErr != nil || (inc.BaseBranch != "" && incUnpushedErr != nil),
		})
	}

	hasWork := false
	for _, r := range repos {
		if len(r.dirtyFiles) > 0 || len(r.unpushedCommits) > 0 || r.gitFailed {
			hasWork = true
			break
		}
	}
	if !hasWork {
		return true, nil
	}

	if out.IsJSON() {
		return false, fmt.Errorf("session %q has uncommitted changes or unpushed commits; use --force to delete", session.Name)
	}

	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return false, fmt.Errorf("session %q has uncommitted changes or unpushed commits; use --force to delete", session.Name)
	}

	out.Print("Session %q has unsaved work:\n\n", session.Name)

	for _, r := range repos {
		if len(r.dirtyFiles) == 0 && len(r.unpushedCommits) == 0 && !r.gitFailed {
			continue
		}
		if len(repos) > 1 {
			out.Print("  %s:\n", r.name)
		}
		if len(r.dirtyFiles) > 0 {
			out.Print("    Dirty files:\n")
			for _, f := range r.dirtyFiles {
				out.Print("      %s\n", f)
			}
		}
		if len(r.unpushedCommits) > 0 {
			out.Print("    Unpushed commits:\n")
			for _, c := range r.unpushedCommits {
				out.Print("      %s\n", c)
			}
		}
		if r.gitFailed {
			out.Print("    Warning: could not fully check worktree status\n")
		}
		out.Print("\n")
	}

	out.Print("Delete anyway? [y/N] ")
	reader := bufio.NewReader(os.Stdin)
	answer, err := reader.ReadString('\n')
	if err != nil {
		return false, err
	}
	answer = strings.TrimSpace(strings.ToLower(answer))
	if answer != "y" && answer != "yes" {
		out.Print("Aborted\n")
		return false, nil
	}
	return true, nil
}

func deleteBatchRun(cmd *cobra.Command) error {
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

	matched, err := filterSessions(list.Sessions, &deleteBatch)
	if err != nil {
		return err
	}
	if len(matched) == 0 {
		out.Print("No sessions match the given filters\n")
		return nil
	}

	if !deleteBatch.force {
		confirmed, err := confirmBatch(cmd, "delete", "deleted", matched)
		if err != nil {
			return err
		}
		if !confirmed {
			return nil
		}
	}

	for _, s := range matched {
		c.SendControl("delete", protocol.DeleteMsg{SessionID: s.ID})
		resp, err := c.ReadControlResponse()
		if err != nil {
			return err
		}
		if resp.Type == "error" {
			var e protocol.ErrorMsg
			protocol.DecodePayload(resp, &e)
			return fmt.Errorf("deleting %s: %s", s.Name, e.Message)
		}
	}

	out.Print("Deleted %d sessions\n", len(matched))
	return nil
}

func init() {
	addBatchFlags(deleteCmd, &deleteBatch)
	rootCmd.AddCommand(deleteCmd)
}
