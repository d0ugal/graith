package cli

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"text/tabwriter"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/store"
	"github.com/spf13/cobra"
)

var storeRepoFlag string

var storeCmd = &cobra.Command{
	Use:     "store",
	Aliases: []string{"s"},
	Short:   "Shared document store",
}

// resolveStoreRepoPath resolves the repo path from: --repo flag,
// GRAITH_WORKTREE_PATH env var, or the CWD git root.
func resolveStoreRepoPath() (string, error) {
	if storeRepoFlag != "" {
		return config.ResolvePath(storeRepoFlag), nil
	}

	if wtPath := os.Getenv("GRAITH_WORKTREE_PATH"); wtPath != "" {
		cmd := exec.Command("git", "rev-parse", "--show-toplevel")
		cmd.Dir = wtPath
		out, err := cmd.Output()
		if err == nil {
			return config.ResolvePath(strings.TrimSpace(string(out))), nil
		}
	}

	gitOut, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("could not detect repo path: use --repo or run from inside a git repository")
	}
	return config.ResolvePath(strings.TrimSpace(string(gitOut))), nil
}

// resolveCurrentRepo returns the current repo path or empty string on failure.
func resolveCurrentRepo() string {
	if wtPath := os.Getenv("GRAITH_WORKTREE_PATH"); wtPath != "" {
		cmd := exec.Command("git", "rev-parse", "--show-toplevel")
		cmd.Dir = wtPath
		out, err := cmd.Output()
		if err == nil {
			return config.ResolvePath(strings.TrimSpace(string(out)))
		}
	}

	gitOut, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return ""
	}
	return config.ResolvePath(strings.TrimSpace(string(gitOut)))
}

// checkWritePermission rejects writes when --repo targets a different repo
// than the current one.
func checkWritePermission(repo string) error {
	current := resolveCurrentRepo()
	if current == "" {
		return nil
	}
	if storeRepoFlag != "" && repo != current {
		return fmt.Errorf("cannot write to store for %s from repo %s", repo, current)
	}
	return nil
}

// --- gr store put ---

var storePutFile string

var storePutCmd = &cobra.Command{
	Use:   "put <key> [body]",
	Short: "Put a document into the store",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		key := args[0]
		bodyArgs := args[1:]

		body, err := resolveBody(bodyArgs, storePutFile)
		if err != nil {
			return err
		}

		repo, err := resolveStoreRepoPath()
		if err != nil {
			return err
		}

		if err := checkWritePermission(repo); err != nil {
			return err
		}

		storePath := store.StorePath(paths.DataDir, repo)
		if err := store.Init(storePath); err != nil {
			return err
		}
		if err := store.Put(storePath, key, body); err != nil {
			return err
		}

		if jsonOutput {
			return out.JSON(struct {
				Key  string `json:"key"`
				Repo string `json:"repo"`
			}{key, repo})
		}
		out.Print("Stored %s\n", key)
		return nil
	},
}

// --- gr store get ---

var storeGetCmd = &cobra.Command{
	Use:   "get <key>",
	Short: "Get a document from the store",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		key := args[0]

		repo, err := resolveStoreRepoPath()
		if err != nil {
			return err
		}

		storePath := store.StorePath(paths.DataDir, repo)
		body, err := store.Get(storePath, key)
		if err != nil {
			return fmt.Errorf("document %q not found", key)
		}

		fmt.Print(body)
		return nil
	},
}

// --- gr store list ---

var storeListCmd = &cobra.Command{
	Use:     "list [prefix]",
	Aliases: []string{"ls"},
	Short:   "List documents in the store",
	Args:    cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		var prefix string
		if len(args) > 0 {
			prefix = args[0]
		}

		repo, err := resolveStoreRepoPath()
		if err != nil {
			return err
		}

		storePath := store.StorePath(paths.DataDir, repo)
		entries, err := store.List(storePath, prefix)
		if err != nil {
			return err
		}

		if jsonOutput {
			return out.JSON(entries)
		}

		if len(entries) == 0 {
			out.Print("No documents found\n")
			return nil
		}

		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "KEY\tUPDATED")
		for _, entry := range entries {
			fmt.Fprintf(tw, "%s\t%s\n", entry.Key, entry.UpdatedAt.Format("2006-01-02 15:04:05"))
		}
		tw.Flush()
		return nil
	},
}

// --- gr store rm ---

var storeRmCmd = &cobra.Command{
	Use:   "rm <key>",
	Short: "Remove a document from the store",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		key := args[0]

		repo, err := resolveStoreRepoPath()
		if err != nil {
			return err
		}

		if err := checkWritePermission(repo); err != nil {
			return err
		}

		storePath := store.StorePath(paths.DataDir, repo)
		if err := store.Remove(storePath, key); err != nil {
			return err
		}

		if jsonOutput {
			return out.JSON(struct {
				Key     string `json:"key"`
				Deleted bool   `json:"deleted"`
			}{key, true})
		}
		out.Print("Removed %s\n", key)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(storeCmd)
	storeCmd.PersistentFlags().StringVar(&storeRepoFlag, "repo", "", "repo path (default: auto-detect)")

	storeCmd.AddCommand(storePutCmd)
	storePutCmd.Flags().StringVarP(&storePutFile, "file", "f", "", "read body from file")

	storeCmd.AddCommand(storeGetCmd)
	storeCmd.AddCommand(storeListCmd)
	storeCmd.AddCommand(storeRmCmd)
}
