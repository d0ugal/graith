package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/store"
	"github.com/spf13/cobra"
)

var storeRepoFlag string
var storeSharedFlag bool

var storeCmd = &cobra.Command{
	Use:     "store",
	Aliases: []string{"s"},
	Short:   "Shared document store",
}

// resolveStoreRepoPath resolves the repo path from: --repo flag,
// GRAITH_REPO_PATH env var (canonical source repo), or the CWD git root.
func resolveStoreRepoPath() (string, error) {
	if storeRepoFlag != "" {
		return config.ResolvePath(storeRepoFlag), nil
	}

	if repoPath := os.Getenv("GRAITH_REPO_PATH"); repoPath != "" {
		return config.ResolvePath(repoPath), nil
	}

	gitOut, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("could not detect repo path: use --repo, --shared, or run from inside a git repository")
	}

	return config.ResolvePath(strings.TrimSpace(string(gitOut))), nil
}

// inGraithSessionWithNoRepo returns true if running inside a graith session
// that has no repo context (e.g. the orchestrator).
func inGraithSessionWithNoRepo() bool {
	return os.Getenv("GRAITH_SESSION_ID") != "" &&
		os.Getenv("GRAITH_REPO_PATH") == "" &&
		resolveCurrentRepo() == ""
}

// resolveStorePath returns the store path and a display label ("shared" or the repo path).
func resolveStorePath() (storePath string, label string, err error) {
	if storeSharedFlag && storeRepoFlag != "" {
		return "", "", fmt.Errorf("--shared and --repo are mutually exclusive")
	}

	if storeSharedFlag || (storeRepoFlag == "" && inGraithSessionWithNoRepo()) {
		sp := store.SharedStorePath(paths.DataDir)
		return sp, "shared", nil
	}

	repo, err := resolveStoreRepoPath()
	if err != nil {
		return "", "", err
	}

	sp := store.StorePath(paths.DataDir, repo)

	return sp, repo, nil
}

// resolveCurrentRepo returns the current repo path or empty string on failure.
func resolveCurrentRepo() string {
	if repoPath := os.Getenv("GRAITH_REPO_PATH"); repoPath != "" {
		return config.ResolvePath(repoPath)
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

		storePath, label, err := resolveStorePath()
		if err != nil {
			return err
		}

		if !storeSharedFlag {
			if err := checkWritePermission(label); err != nil {
				return err
			}
		}

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
			}{key, label})
		}

		out.Printf("Stored %s\n", key)

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

		storePath, _, err := resolveStorePath()
		if err != nil {
			return err
		}

		body, err := store.Get(storePath, key)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("document %q not found", key)
			}

			return fmt.Errorf("get %q: %w", key, err)
		}

		fmt.Print(body)

		return nil
	},
}

// --- gr store list ---

var storeListAll bool

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

		if storeListAll {
			return listAllStores(prefix)
		}

		storePath, label, err := resolveStorePath()
		if err != nil {
			if storeSharedFlag || storeRepoFlag != "" {
				return err
			}

			return listAllStores(prefix)
		}

		entries, err := store.List(storePath, prefix)
		if err != nil {
			return err
		}

		type entryWithRepo struct {
			Key       string `json:"key"`
			Repo      string `json:"repo"`
			UpdatedAt string `json:"updated_at"`
		}

		if jsonOutput {
			result := make([]entryWithRepo, len(entries))
			for i, e := range entries {
				result[i] = entryWithRepo{e.Key, label, e.UpdatedAt.Format(time.RFC3339)}
			}

			return out.JSON(result)
		}

		if len(entries) == 0 {
			out.Printf("No documents found\n")
			return nil
		}

		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "REPO\tKEY\tUPDATED")

		for _, entry := range entries {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", label, entry.Key, entry.UpdatedAt.Format("2006-01-02 15:04:05"))
		}

		_ = tw.Flush()

		return nil
	},
}

func listAllStores(prefix string) error {
	stores, err := store.ListStores(paths.DataDir)
	if err != nil {
		return err
	}

	if len(stores) == 0 {
		out.Printf("No stores found\n")
		return nil
	}

	for i := range stores {
		entries, err := store.List(stores[i].Path, prefix)
		if err != nil {
			return err
		}

		stores[i].Entries = entries
	}

	type entryWithRepo struct {
		Key       string `json:"key"`
		Repo      string `json:"repo"`
		UpdatedAt string `json:"updated_at"`
	}

	if jsonOutput {
		var result []entryWithRepo

		for _, s := range stores {
			for _, e := range s.Entries {
				result = append(result, entryWithRepo{e.Key, s.Name, e.UpdatedAt.Format(time.RFC3339)})
			}
		}

		return out.JSON(result)
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "REPO\tKEY\tUPDATED")

	for _, s := range stores {
		for _, entry := range s.Entries {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", s.Name, entry.Key, entry.UpdatedAt.Format("2006-01-02 15:04:05"))
		}
	}

	_ = tw.Flush()

	return nil
}

// --- gr store append ---

var storeAppendFile string

var storeAppendCmd = &cobra.Command{
	Use:   "append <key> [line]",
	Short: "Append a line to a document in the store",
	Long:  "Append a line to a document, creating it if it doesn't exist. Useful for JSONL logs and other append-only data.",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		key := args[0]
		bodyArgs := args[1:]

		line, err := resolveBody(bodyArgs, storeAppendFile)
		if err != nil {
			return err
		}

		storePath, label, err := resolveStorePath()
		if err != nil {
			return err
		}

		if !storeSharedFlag {
			if err := checkWritePermission(label); err != nil {
				return err
			}
		}

		if err := store.Init(storePath); err != nil {
			return err
		}

		if err := store.Append(storePath, key, line); err != nil {
			return err
		}

		if jsonOutput {
			return out.JSON(struct {
				Key  string `json:"key"`
				Repo string `json:"repo"`
			}{key, label})
		}

		out.Printf("Appended to %s\n", key)

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

		storePath, label, err := resolveStorePath()
		if err != nil {
			return err
		}

		if !storeSharedFlag {
			if err := checkWritePermission(label); err != nil {
				return err
			}
		}

		if err := store.Remove(storePath, key); err != nil {
			return err
		}

		if jsonOutput {
			return out.JSON(struct {
				Key     string `json:"key"`
				Deleted bool   `json:"deleted"`
			}{key, true})
		}

		out.Printf("Removed %s\n", key)

		return nil
	},
}

// registerStoreCmd registers this command on rootCmd. Called from registerCommands.
func registerStoreCmd() {
	rootCmd.AddCommand(storeCmd)
	storeCmd.PersistentFlags().StringVar(&storeRepoFlag, "repo", "", "repo path (default: auto-detect)")
	storeCmd.PersistentFlags().BoolVar(&storeSharedFlag, "shared", false, "use the shared store (not scoped to any repo)")

	storeCmd.AddCommand(storePutCmd)
	storePutCmd.Flags().StringVarP(&storePutFile, "file", "f", "", "read body from file")

	storeCmd.AddCommand(storeGetCmd)
	storeCmd.AddCommand(storeListCmd)
	storeListCmd.Flags().BoolVarP(&storeListAll, "all", "a", false, "list documents across all repos")
	storeCmd.AddCommand(storeRmCmd)

	storeCmd.AddCommand(storeAppendCmd)
	storeAppendCmd.Flags().StringVarP(&storeAppendFile, "file", "f", "", "read line from file")
}
