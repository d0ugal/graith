package cli

import (
	"fmt"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

var gcForce bool

var gcCmd = &cobra.Command{
	Use:   "gc",
	Short: "Garbage-collect orphaned worktrees and scratch directories",
	Long: `Find worktree and scratch directories under the graith data dir that have no
matching session — typically left behind by a daemon crash mid-delete — and
optionally remove them.

By default gc runs as a dry run, listing what it would remove. Pass --force to
actually delete. Worktrees with uncommitted changes are never removed; they are
reported so their unreachable work can be recovered manually.`,
	Args: cobra.NoArgs,
	RunE: func(_ *cobra.Command, _ []string) error {
		c, err := client.Connect(cfg, paths, cfgFile)
		if err != nil {
			return err
		}
		defer c.Close()

		_ = c.SendControl("gc", protocol.GCMsg{Force: gcForce})

		resp, err := c.ReadControlResponse()
		if err != nil {
			return err
		}

		if resp.Type == "error" {
			var e protocol.ErrorMsg

			_ = protocol.DecodePayload(resp, &e)

			return fmt.Errorf("%s", e.Message)
		}

		var result protocol.GCResultMsg
		if err := protocol.DecodePayload(resp, &result); err != nil {
			return err
		}

		if out.IsJSON() {
			return out.JSON(result)
		}

		return printGCResult(result)
	},
}

func printGCResult(result protocol.GCResultMsg) error {
	if len(result.Orphans) == 0 {
		out.Printf("No orphaned worktrees or scratch directories found.\n")
		return nil
	}

	if result.DryRun {
		out.Printf("Found %d orphan(s) (dry run — pass --force to remove):\n\n", len(result.Orphans))
	} else {
		out.Printf("Processed %d orphan(s):\n\n", len(result.Orphans))
	}

	var removed, skipped int

	for _, o := range result.Orphans {
		status := "would remove"

		switch {
		case o.Removed:
			status = "removed"
			removed++
		case o.Skipped:
			status = "skipped"
			skipped++
		case o.HasDirtyFiles:
			status = "skip (uncommitted changes)"
		}

		out.Printf("  [%s] %s %s\n", o.Type, status, o.Path)

		if o.Reason != "" {
			out.Printf("        %s\n", o.Reason)
		}
	}

	if !result.DryRun {
		out.Printf("\nRemoved %d, skipped %d.\n", removed, skipped)
	}

	return nil
}

// registerGCCmd registers this command on rootCmd. Called from registerCommands.
func registerGCCmd() {
	gcCmd.Flags().BoolVar(&gcForce, "force", false, "actually remove orphans (default is a dry run)")
	rootCmd.AddCommand(gcCmd)
}
