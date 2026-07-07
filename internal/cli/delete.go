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

var (
	deleteBatch    batchFlags
	deleteChildren bool
)

var deleteCmd = &cobra.Command{
	Use:     "delete <name-or-id>",
	Aliases: []string{"rm"},
	Short:   "Delete a session by name or ID",
	Args: func(cmd *cobra.Command, args []string) error {
		if deleteChildren && deleteBatch.active() {
			return fmt.Errorf("--children cannot be combined with batch filters")
		}

		if deleteBatch.active() {
			return cobra.NoArgs(cmd, args)
		}

		if deleteChildren {
			return cobra.MaximumNArgs(1)(cmd, args)
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

		var (
			sessionID   string
			excludeRoot bool
		)

		if deleteChildren && len(args) == 0 {
			sessionID = os.Getenv("GRAITH_SESSION_ID")
			if sessionID == "" {
				return fmt.Errorf("--children with no session arg requires GRAITH_SESSION_ID to be set")
			}

			excludeRoot = true
		} else {
			session, err := resolveSessionInfo(c, args[0])
			if err != nil {
				return err
			}

			sessionID = session.ID

			if !deleteBatch.force && session.WorktreePath != "" && !session.InPlace {
				confirmed, err := confirmDelete(session)
				if err != nil {
					return err
				}

				if !confirmed {
					return nil
				}
			}
		}

		_ = c.SendControl("delete", protocol.DeleteMsg{
			SessionID:   sessionID,
			Children:    deleteChildren,
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

		if deleteChildren {
			var result struct {
				Deleted []string `json:"deleted"`
			}

			_ = protocol.DecodePayload(resp, &result)
			out.Printf("Deleted %d sessions\n", len(result.Deleted))
		} else {
			out.Printf("Session deleted\n")
		}

		return nil
	},
}

func resolveSessionInfo(c *client.Client, nameOrID string) (*protocol.SessionInfo, error) {
	_ = c.SendControl("list", struct{}{})

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

// dirtyFilesFn and unpushedSummariesFn indirect the git status lookups so
// tests can inject fakes without spawning git.
var (
	dirtyFilesFn        = git.DirtyFiles
	unpushedSummariesFn = git.UnpushedCommitSummaries
)

// liveGitStatus is a live dirty/unpushed summary for a single session,
// aggregated across its main worktree and any included repos.
type liveGitStatus struct {
	dirty     bool
	unpushed  int
	gitFailed bool
}

// liveSessionStatus recomputes a session's dirty/unpushed state with live git
// checks, rather than trusting the daemon's cached SessionInfo fields (which
// the background refresh loop skips for non-running sessions — see #209).
//
// Some sessions carry no deletion-relevant git state and are reported clean:
//   - In-place sessions: deleting one never removes the worktree.
//   - Shared-worktree sessions: WorktreePath points at the source session's
//     worktree, but deletion only removes the shared scratch dir — attributing
//     the source's dirty/unpushed work here would be misleading. This matches
//     the daemon refresh loop and the overlay's shared-worktree suppression.
//   - No-repo sessions (empty RepoPath): the scratch worktree is not a git
//     repo, so a git check would spuriously fail.
func liveSessionStatus(s protocol.SessionInfo) liveGitStatus {
	var st liveGitStatus

	if s.InPlace || s.SharedWorktree || s.RepoPath == "" {
		return st
	}

	check := func(worktreePath, baseBranch string) {
		if worktreePath == "" {
			return
		}

		dirty, dirtyErr := dirtyFilesFn(worktreePath)
		unpushed, unpushedErr := unpushedSummariesFn(worktreePath, baseBranch)

		if len(dirty) > 0 {
			st.dirty = true
		}

		st.unpushed += len(unpushed)

		if dirtyErr != nil || (baseBranch != "" && unpushedErr != nil) {
			st.gitFailed = true
		}
	}

	check(s.WorktreePath, s.BaseBranch)

	for _, inc := range s.Includes {
		check(inc.WorktreePath, inc.BaseBranch)
	}

	return st
}

func confirmDelete(session *protocol.SessionInfo) (bool, error) {
	var repos []repoStatus

	mainDirty, mainDirtyErr := dirtyFilesFn(session.WorktreePath)
	mainUnpushed, mainUnpushedErr := unpushedSummariesFn(session.WorktreePath, session.BaseBranch)
	repos = append(repos, repoStatus{
		name:            session.RepoName,
		dirtyFiles:      mainDirty,
		unpushedCommits: mainUnpushed,
		gitFailed:       mainDirtyErr != nil || (session.BaseBranch != "" && mainUnpushedErr != nil),
	})

	for _, inc := range session.Includes {
		incDirty, incDirtyErr := dirtyFilesFn(inc.WorktreePath)
		incUnpushed, incUnpushedErr := unpushedSummariesFn(inc.WorktreePath, inc.BaseBranch)
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

	out.Printf("Session %q has unsaved work:\n\n", session.Name)

	for _, r := range repos {
		if len(r.dirtyFiles) == 0 && len(r.unpushedCommits) == 0 && !r.gitFailed {
			continue
		}

		if len(repos) > 1 {
			out.Printf("  %s:\n", r.name)
		}

		if len(r.dirtyFiles) > 0 {
			out.Printf("    Dirty files:\n")

			for _, f := range r.dirtyFiles {
				out.Printf("      %s\n", f)
			}
		}

		if len(r.unpushedCommits) > 0 {
			out.Printf("    Unpushed commits:\n")

			for _, c := range r.unpushedCommits {
				out.Printf("      %s\n", c)
			}
		}

		if r.gitFailed {
			out.Printf("    Warning: could not fully check worktree status\n")
		}

		out.Printf("\n")
	}

	out.Printf("Delete anyway? [y/N] ")

	reader := bufio.NewReader(os.Stdin)

	answer, err := reader.ReadString('\n')
	if err != nil {
		return false, err
	}

	answer = strings.TrimSpace(strings.ToLower(answer))
	if answer != "y" && answer != "yes" {
		out.Printf("Aborted\n")
		return false, nil
	}

	return true, nil
}

func deleteBatchRun(cmd *cobra.Command) error {
	return runBatch(cmd, &deleteBatch, "delete", "deleted", "deleting", "delete",
		func(sessionID string) any {
			return protocol.DeleteMsg{SessionID: sessionID}
		})
}

// registerDeleteCmd registers this command on rootCmd. Called from registerCommands.
func registerDeleteCmd() {
	addBatchFlags(deleteCmd, &deleteBatch)
	deleteCmd.Flags().BoolVar(&deleteChildren, "children", false, "also delete all descendant sessions")
	rootCmd.AddCommand(deleteCmd)
}
