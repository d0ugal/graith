package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/git"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var (
	deleteBatch    batchFlags
	deleteChildren bool
	// deleteYesNoop keeps `gr delete -y` working as an accepted, inert alias
	// (`--force` is provided by addBatchFlags and is likewise inert here). `gr
	// delete` is always a recoverable soft delete now, so there is nothing to
	// force or confirm — kept for backward compatibility with existing scripts.
	deleteYesNoop bool
)

var deleteCmd = &cobra.Command{
	Use:     "delete <name-or-id>",
	Aliases: []string{"rm"},
	Short:   "Soft-delete a session (recoverable with `gr restore`)",
	Long: "Soft-delete a session: stop its agent and hide it, but keep its worktree, branch, " +
		"and state for the retention window so `gr restore` can recover it. Use `gr purge` to " +
		"delete immediately and irrecoverably. When soft delete is disabled (`retention = \"0\"`), " +
		"`gr delete` is rejected — use `gr purge`.",
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
		warnDeleteForceDeprecated(cmd)

		if deleteBatch.active() {
			return deleteBatchRun(cmd, &deleteBatch, false)
		}

		c, err := client.Connect(cfg, paths, cfgFile)
		if err != nil {
			return err
		}
		defer c.Close()

		sessionID, excludeRoot, err := resolveDeleteTarget(c, args, deleteChildren)
		if err != nil {
			return err
		}

		// gr delete is always a recoverable soft delete — the daemon rejects it
		// when soft delete is disabled (retention=0) rather than destroying — so
		// there is no unsaved-work prompt and Purge is never set.
		return sendDelete(c, sessionID, deleteChildren, excludeRoot, false)
	},
}

// warnDeleteForceDeprecated prints a one-line stderr deprecation notice when the
// inert --force/-y aliases are passed to `gr delete`. They no longer do anything
// (delete is always recoverable); the notice points users at `gr purge`.
func warnDeleteForceDeprecated(cmd *cobra.Command) {
	if deleteBatch.force || deleteYesNoop {
		_, _ = fmt.Fprintln(cmd.ErrOrStderr(),
			"--force/-y are deprecated: gr delete is now recoverable; use gr purge to remove immediately")
	}
}

// resolveDeleteTarget resolves the target session ID (and excludeRoot) for a
// single delete/purge, honouring `--children` with no positional arg
// (auto-resolving from GRAITH_SESSION_ID).
func resolveDeleteTarget(c *client.Client, args []string, children bool) (sessionID string, excludeRoot bool, err error) {
	if children && len(args) == 0 {
		sessionID = os.Getenv("GRAITH_SESSION_ID")
		if sessionID == "" {
			return "", false, fmt.Errorf("--children with no session arg requires GRAITH_SESSION_ID to be set")
		}

		return sessionID, true, nil
	}

	// gr delete resolves live-only: a soft-deleted namesake must not make
	// `gr delete <live>` ambiguous. Purging an already-trashed session is `gr
	// purge`'s job (it unions live+deleted).
	session, err := resolveSessionInfo(c, args[0])
	if err != nil {
		return "", false, err
	}

	return session.ID, false, nil
}

// sendDelete sends a delete control message (soft when purge is false, hard when
// true) and renders the daemon's DeleteResultMsg response.
func sendDelete(c *client.Client, sessionID string, children, excludeRoot, purge bool) error {
	_ = c.SendControl("delete", protocol.DeleteMsg{
		SessionID:   sessionID,
		Children:    children,
		ExcludeRoot: excludeRoot,
		Purge:       purge,
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

	var result protocol.DeleteResultMsg

	_ = protocol.DecodePayload(resp, &result)

	if jsonOutput {
		return out.JSON(result)
	}

	printDeleteResult(result, children)

	return nil
}

// printDeleteResult renders the delete outcome: a soft delete shows the
// recovery deadline and the commands to restore or purge; a hard delete (or
// --children batch) reports counts.
func printDeleteResult(r protocol.DeleteResultMsg, children bool) {
	if children {
		soft := 0

		for _, c := range r.Affected {
			if c.Soft {
				soft++
			}
		}

		if soft > 0 {
			out.Printf("Soft-deleted %d sessions (recover with `gr restore --children`, purged after the retention window)\n", len(r.Affected))
		} else {
			out.Printf("Purged %d sessions (permanently)\n", len(r.Affected))
		}

		return
	}

	name := r.Name
	if name == "" {
		name = r.SessionID
	}

	// A non-soft result only ever comes from `gr purge` (gr delete is always soft
	// or an error), so this is the purge success line.
	if !r.Soft {
		out.Printf("Purged %s (permanently)\n", name)
		return
	}

	if expiry := formatDeleteDeadline(r.ExpiresAt); expiry != "" {
		out.Printf("Soft-deleted %s. Recoverable until %s.\n", name, expiry)
	} else {
		out.Printf("Soft-deleted %s. Recoverable with `gr restore`.\n", name)
	}

	out.Printf("  gr restore %s   to bring it back\n", name)
	out.Printf("  gr purge %s     to remove it now\n", name)
}

// formatDeleteDeadline renders an RFC3339 expiry as "2006-01-02 15:04 (in 23h)"
// relative to now, or "" if the timestamp is empty/unparseable.
func formatDeleteDeadline(expiresAt string) string {
	if expiresAt == "" {
		return ""
	}

	t, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		return ""
	}

	remaining := time.Until(t)
	if remaining <= 0 {
		return t.Format("2006-01-02 15:04")
	}

	return fmt.Sprintf("%s (in %s)", t.Format("2006-01-02 15:04"), client.ShortDuration(remaining))
}

// resolveDeletableSessionInfo finds a session by name or ID among both live and
// soft-deleted sessions. `gr purge` needs to reach soft-deleted sessions so it
// can hard-delete one that was already soft-deleted (empty one trash entry). If
// the name is ambiguous across the combined set (e.g. a live session and a
// soft-deleted one share a name), it requires an explicit ID.
func resolveDeletableSessionInfo(c *client.Client, nameOrID string) (*protocol.SessionInfo, error) {
	live, err := listSessions(c, false)
	if err != nil {
		return nil, err
	}

	deleted, err := listSessions(c, true)
	if err != nil {
		return nil, err
	}

	return resolveByNameOrID(nameOrID, append(live, deleted...))
}

// resolveDeletedSessionInfo finds a soft-deleted session by name or ID, with the
// same ambiguity handling. Used by `gr restore`.
func resolveDeletedSessionInfo(c *client.Client, nameOrID string) (*protocol.SessionInfo, error) {
	deleted, err := listSessions(c, true)
	if err != nil {
		return nil, err
	}

	return resolveByNameOrID(nameOrID, deleted)
}

func resolveSessionInfo(c *client.Client, nameOrID string) (*protocol.SessionInfo, error) {
	return resolveSessionInfoFiltered(c, nameOrID, false)
}

// resolveSessionInfoFiltered looks up a session by name or ID. When deleted is
// true it searches the soft-deleted sessions; otherwise the live ones.
func resolveSessionInfoFiltered(c *client.Client, nameOrID string, deleted bool) (*protocol.SessionInfo, error) {
	sessions, err := listSessions(c, deleted)
	if err != nil {
		return nil, err
	}

	return resolveByNameOrID(nameOrID, sessions)
}

// listSessions fetches the session list (live or soft-deleted) from the daemon.
func listSessions(c *client.Client, deleted bool) ([]protocol.SessionInfo, error) {
	_ = c.SendControl("list", protocol.ListMsg{Deleted: deleted})

	resp, err := c.ReadControlResponse()
	if err != nil {
		return nil, err
	}

	var list protocol.SessionListMsg
	if err := protocol.DecodePayload(resp, &list); err != nil {
		return nil, err
	}

	return list.Sessions, nil
}

// resolveByNameOrID resolves nameOrID against a session list. An exact ID match is
// unambiguous (IDs are unique) and wins immediately. Otherwise a name may match
// more than one session (names are not unique — e.g. delete/recreate cycles);
// in that case the caller must disambiguate with an explicit ID rather than
// acting on an arbitrary first match.
func resolveByNameOrID(nameOrID string, sessions []protocol.SessionInfo) (*protocol.SessionInfo, error) {
	var byName []protocol.SessionInfo

	for i := range sessions {
		if sessions[i].ID == nameOrID {
			return &sessions[i], nil
		}

		if sessions[i].Name == nameOrID {
			byName = append(byName, sessions[i])
		}
	}

	switch len(byName) {
	case 0:
		return nil, fmt.Errorf("session %q not found", nameOrID)
	case 1:
		return &byName[0], nil
	default:
		ids := make([]string, len(byName))
		for i, s := range byName {
			ids[i] = s.ID
		}

		return nil, fmt.Errorf("%q is ambiguous — matches %d sessions (%s); use an explicit ID",
			nameOrID, len(byName), strings.Join(ids, ", "))
	}
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

	// confirmDelete now guards `gr purge` (the destructive verb). Steer
	// non-interactive callers toward the recoverable `gr delete` rather than
	// forcing the irrecoverable purge.
	if out.IsJSON() || !term.IsTerminal(int(os.Stdin.Fd())) {
		return false, fmt.Errorf("session %q has uncommitted changes or unpushed commits; use `gr delete` to keep it recoverable, or `gr purge -y` to destroy it now", session.Name)
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

	out.Printf("Purge anyway? [y/N] ")

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

// deleteBatchRun runs a batch delete (purge=false) or batch purge (purge=true).
// A soft batch delete never prompts (nothing is lost); a batch purge prompts
// once for the whole batch unless the caller's --force/-y is set (runBatch reads
// bf.force for that).
func deleteBatchRun(cmd *cobra.Command, bf *batchFlags, purge bool) error {
	verb := "delete"
	if purge {
		verb = "purge"
	}

	effective := bf
	if !purge {
		// Soft delete is not destructive, so skip the batch confirmation prompt
		// regardless of --force.
		cp := *bf
		cp.force = true
		effective = &cp
	}

	return runBatch(cmd, effective, verb, verb+"d", verb+"ing", verb,
		func(sessionID string) any {
			return protocol.DeleteMsg{SessionID: sessionID, Purge: purge}
		}, nil)
}

// registerDeleteCmd registers this command on rootCmd. Called from registerCommands.
func registerDeleteCmd() {
	addBatchFlags(deleteCmd, &deleteBatch)
	deleteCmd.Flags().BoolVar(&deleteChildren, "children", false, "also soft-delete all descendant sessions")
	// --force and --yes are accepted but inert: gr delete is always a recoverable
	// soft delete, so there is nothing to force or confirm. Kept for backward
	// compatibility with existing `gr delete --force` scripts (now a safety
	// upgrade — soft instead of destroy).
	deleteCmd.Flags().BoolVarP(&deleteYesNoop, "yes", "y", false, "deprecated no-op (gr delete is always recoverable)")
	_ = deleteCmd.Flags().MarkHidden("yes")
	rootCmd.AddCommand(deleteCmd)
}
