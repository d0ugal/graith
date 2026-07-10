package cli

import (
	"fmt"
	"os"

	"github.com/d0ugal/graith/internal/client"
	"github.com/spf13/cobra"
)

var (
	purgeBatch    batchFlags
	purgeChildren bool
	purgeYes      bool
)

var purgeCmd = &cobra.Command{
	Use:   "purge <name-or-id>",
	Short: "Permanently delete a session now, bypassing the recovery window",
	Long: "Hard-delete a session immediately: remove its worktree, branch, and state with no " +
		"recovery. Works on a live session or one already soft-deleted (to empty a single trash " +
		"entry). This is the destructive verb — use `gr delete` for a recoverable soft delete.",
	Args: func(cmd *cobra.Command, args []string) error {
		if purgeChildren && purgeBatch.active() {
			return fmt.Errorf("--children cannot be combined with batch filters")
		}

		if purgeBatch.active() {
			return cobra.NoArgs(cmd, args)
		}

		if purgeChildren {
			return cobra.MaximumNArgs(1)(cmd, args)
		}

		return cobra.ExactArgs(1)(cmd, args)
	},
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

		c, err := client.Connect(cfg, paths, cfgFile)
		if err != nil {
			return err
		}
		defer c.Close()

		var (
			sessionID   string
			excludeRoot bool
		)

		if purgeChildren && len(args) == 0 {
			sessionID = os.Getenv("GRAITH_SESSION_ID")
			if sessionID == "" {
				return fmt.Errorf("--children with no session arg requires GRAITH_SESSION_ID to be set")
			}

			excludeRoot = true
		} else {
			session, err := resolveDeletableSessionInfo(c, args[0])
			if err != nil {
				return err
			}

			sessionID = session.ID

			// Purge is irrecoverable, so prompt on unsaved work unless -y/--force.
			// (Skipped for --children, which has no single session to inspect —
			// mirrors the delete/stop --children behaviour.)
			if !skipPrompt && !purgeChildren && session.WorktreePath != "" && !session.InPlace {
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

// registerPurgeCmd registers this command on rootCmd. Called from registerCommands.
func registerPurgeCmd() {
	addBatchFlags(purgeCmd, &purgeBatch)
	purgeCmd.Flags().BoolVar(&purgeChildren, "children", false, "also purge all descendant sessions")
	purgeCmd.Flags().BoolVarP(&purgeYes, "yes", "y", false, "skip the unsaved-work confirmation prompt")
	rootCmd.AddCommand(purgeCmd)
}
