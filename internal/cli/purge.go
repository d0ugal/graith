package cli

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/d0ugal/graith/internal/client"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var (
	purgeBatch    batchFlags
	purgeChildren bool
	purgeYes      bool
	purgeSelf     bool
)

var purgeCmd = &cobra.Command{
	Use:   "purge <name-or-id>",
	Short: "Permanently delete a session now, bypassing the recovery window",
	Long: "Hard-delete a session immediately: remove its worktree, branch, and state with no " +
		"recovery. Works on a live session or one already soft-deleted (to empty a single trash " +
		"entry). This is the destructive verb — use `gr delete` for a recoverable soft delete.",
	Args:              selfChildrenBatchArgs(&purgeSelf, &purgeChildren, &purgeBatch),
	ValidArgsFunction: completeSessionOrDeletedNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		skipPrompt := purgeBatch.force || purgeYes

		if purgeBatch.active() {
			// runBatch reads bf.force to decide whether to skip the batch prompt;
			// fold -y into it so either flag skips confirmation.
			bf := purgeBatch
			bf.force = skipPrompt

			return deleteBatchRun(cmd, &bf, true)
		}

		args, err := selfArgs(purgeSelf, args)
		if err != nil {
			return err
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

		if purgeChildren {
			// A subtree purge destroys every descendant's worktree and branch. We
			// can't cheaply per-session inspect the whole subtree here, so require
			// an explicit confirmation for the whole operation (unless -y/--force).
			rootLabel := "this session"

			if len(args) == 0 {
				sessionID = os.Getenv("GRAITH_SESSION_ID")
				if sessionID == "" {
					return errors.New("--children with no session arg requires GRAITH_SESSION_ID to be set")
				}

				excludeRoot = true
			} else {
				session, err := resolveDeletableSessionInfo(c, args[0])
				if err != nil {
					return err
				}

				sessionID = session.ID
				rootLabel = session.Name
			}

			if !skipPrompt {
				confirmed, err := confirmPurgeSubtree(rootLabel)
				if err != nil {
					return err
				}

				if !confirmed {
					return nil
				}
			}
		} else {
			session, err := resolveDeletableSessionInfo(c, args[0])
			if err != nil {
				return err
			}

			sessionID = session.ID

			// Purge is irrecoverable, so prompt on unsaved work unless -y/--force.
			if !skipPrompt && session.WorktreePath != "" && !session.InPlace {
				confirmed, err := confirmDelete(session)
				if err != nil {
					return err
				}

				if !confirmed {
					return nil
				}
			}
		}

		return sendDelete(c, sessionID, purgeChildren, excludeRoot, true)
	},
}

// confirmPurgeSubtree asks for confirmation before a --children purge, which
// destroys the worktree and branch of the root and every descendant with no
// recovery. In JSON / non-TTY mode it errors (require -y) rather than prompting.
func confirmPurgeSubtree(rootLabel string) (bool, error) {
	if out.IsJSON() || !term.IsTerminal(int(os.Stdin.Fd())) {
		return false, fmt.Errorf("purging %s and its descendants is irrecoverable; pass -y to confirm", rootLabel)
	}

	out.Printf("Purge %s and ALL its descendants? This is irrecoverable. [y/N] ", rootLabel)

	answer, err := bufio.NewReader(os.Stdin).ReadString('\n')
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

// registerPurgeCmd registers this command on rootCmd. Called from registerCommands.
func registerPurgeCmd() {
	addBatchFlags(purgeCmd, &purgeBatch)
	purgeCmd.Flags().BoolVar(&purgeChildren, "children", false, "also purge all descendant sessions")
	purgeCmd.Flags().BoolVar(&purgeSelf, "self", false, "purge the current session (from GRAITH_SESSION_ID/NAME)")
	purgeCmd.Flags().BoolVarP(&purgeYes, "yes", "y", false, "skip the unsaved-work confirmation prompt")
	rootCmd.AddCommand(purgeCmd)
}
