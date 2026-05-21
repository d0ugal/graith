package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

var storeRepoFlag string

var storeCmd = &cobra.Command{
	Use:     "store",
	Aliases: []string{"s"},
	Short:   "Shared document store",
}

// resolveRepoPath resolves the repo path from: --repo flag, current session's
// RepoPath (via GRAITH_SESSION_ID), or the CWD git root.
func resolveRepoPath(c *client.Client) (string, error) {
	if storeRepoFlag != "" {
		abs, err := filepath.Abs(storeRepoFlag)
		if err != nil {
			return storeRepoFlag, nil
		}
		return abs, nil
	}

	sessionID := os.Getenv("GRAITH_SESSION_ID")
	if sessionID != "" {
		c.SendControl("list", struct{}{})
		resp, err := c.ReadControlResponse()
		if err != nil {
			return "", err
		}
		var list protocol.SessionListMsg
		if err := protocol.DecodePayload(resp, &list); err != nil {
			return "", err
		}
		for _, s := range list.Sessions {
			if s.ID == sessionID {
				if s.RepoPath != "" {
					return s.RepoPath, nil
				}
				break
			}
		}
	}

	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("could not detect repo path: use --repo or run from inside a git repository")
	}
	return strings.TrimSpace(string(out)), nil
}

// expandContentType expands content type shorthands to MIME types.
func expandContentType(ct string) (string, error) {
	switch ct {
	case "":
		return "", fmt.Errorf("--type is required (md, json, text, or a MIME type)")
	case "md", "markdown":
		return "text/markdown", nil
	case "json":
		return "application/json", nil
	case "text":
		return "text/plain", nil
	default:
		if strings.Contains(ct, "/") {
			return ct, nil
		}
		return "", fmt.Errorf("unknown content type shorthand %q (use md, json, text, or a MIME type)", ct)
	}
}

// --- gr store put ---

var (
	storePutContentType string
	storePutFile        string
)

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

		contentType, err := expandContentType(storePutContentType)
		if err != nil {
			return err
		}

		c, err := client.Connect(cfg, paths, cfgFile)
		if err != nil {
			return err
		}
		defer c.Close()

		repo, err := resolveRepoPath(c)
		if err != nil {
			return err
		}

		senderID, senderName := detectSender()

		if err := c.SendControl("store_put", protocol.StorePutMsg{
			Repo:        repo,
			Key:         key,
			Body:        body,
			ContentType: contentType,
			AuthorID:    senderID,
			AuthorName:  senderName,
		}); err != nil {
			return err
		}

		resp, err := c.ReadControlResponse()
		if err != nil {
			return err
		}
		if resp.Type == "error" {
			var e protocol.ErrorMsg
			protocol.DecodePayload(resp, &e)
			return fmt.Errorf("%s", e.Message)
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

		c, err := client.Connect(cfg, paths, cfgFile)
		if err != nil {
			return err
		}
		defer c.Close()

		repo, err := resolveRepoPath(c)
		if err != nil {
			return err
		}

		if err := c.SendControl("store_get", protocol.StoreGetMsg{
			Repo: repo,
			Key:  key,
		}); err != nil {
			return err
		}

		resp, err := c.ReadControlResponse()
		if err != nil {
			return err
		}
		if resp.Type == "error" {
			var e protocol.ErrorMsg
			protocol.DecodePayload(resp, &e)
			return fmt.Errorf("%s", e.Message)
		}

		var result protocol.StoreGetResponseMsg
		if err := protocol.DecodePayload(resp, &result); err != nil {
			return err
		}
		if !result.Found || result.Document == nil {
			return fmt.Errorf("document %q not found", key)
		}

		fmt.Print(result.Document.Body)
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

		c, err := client.Connect(cfg, paths, cfgFile)
		if err != nil {
			return err
		}
		defer c.Close()

		repo, err := resolveRepoPath(c)
		if err != nil {
			return err
		}

		if err := c.SendControl("store_list", protocol.StoreListMsg{
			Repo:   repo,
			Prefix: prefix,
		}); err != nil {
			return err
		}

		resp, err := c.ReadControlResponse()
		if err != nil {
			return err
		}
		if resp.Type == "error" {
			var e protocol.ErrorMsg
			protocol.DecodePayload(resp, &e)
			return fmt.Errorf("%s", e.Message)
		}

		var result protocol.StoreListResponseMsg
		if err := protocol.DecodePayload(resp, &result); err != nil {
			return err
		}

		if jsonOutput {
			return out.JSON(result)
		}

		if len(result.Documents) == 0 {
			out.Print("No documents found\n")
			return nil
		}

		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "KEY\tTYPE\tAUTHOR\tUPDATED")
		for _, doc := range result.Documents {
			author := doc.AuthorName
			if author == "" {
				author = doc.AuthorID
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", doc.Key, doc.ContentType, author, doc.UpdatedAt)
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

		c, err := client.Connect(cfg, paths, cfgFile)
		if err != nil {
			return err
		}
		defer c.Close()

		repo, err := resolveRepoPath(c)
		if err != nil {
			return err
		}

		if err := c.SendControl("store_delete", protocol.StoreDeleteMsg{
			Repo: repo,
			Key:  key,
		}); err != nil {
			return err
		}

		resp, err := c.ReadControlResponse()
		if err != nil {
			return err
		}
		if resp.Type == "error" {
			var e protocol.ErrorMsg
			protocol.DecodePayload(resp, &e)
			return fmt.Errorf("%s", e.Message)
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
	storePutCmd.Flags().StringVar(&storePutContentType, "type", "", "content type (md, json, text, or MIME type)")
	storePutCmd.Flags().StringVarP(&storePutFile, "file", "f", "", "read body from file")

	storeCmd.AddCommand(storeGetCmd)
	storeCmd.AddCommand(storeListCmd)
	storeCmd.AddCommand(storeRmCmd)
}
