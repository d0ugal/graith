package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/daemon"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/d0ugal/graith/internal/sessionlabel"
	"github.com/spf13/cobra"
)

type updateOptions struct {
	name         *string
	parent       *string
	starred      *bool
	addLabels    []string
	removeLabels []string
}

var updateCmd = &cobra.Command{
	Use:               "update <name-or-id>",
	Short:             "Update session properties",
	Long:              "Update session properties such as name, parent, and starred state. Use --parent \"\" to orphan a session.",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeSessionNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		nameFlag, _ := cmd.Flags().GetString("name")
		parentFlag, _ := cmd.Flags().GetString("parent")
		starredFlag, _ := cmd.Flags().GetBool("starred")
		addLabels, _ := cmd.Flags().GetStringArray("add-label")
		removeLabels, _ := cmd.Flags().GetStringArray("remove-label")
		opts := updateOptions{}

		if cmd.Flags().Changed("name") {
			opts.name = &nameFlag
		}

		if cmd.Flags().Changed("parent") {
			opts.parent = &parentFlag
		}

		if cmd.Flags().Changed("starred") {
			opts.starred = &starredFlag
		}

		if cmd.Flags().Changed("add-label") {
			opts.addLabels = addLabels
		}

		if cmd.Flags().Changed("remove-label") {
			opts.removeLabels = removeLabels
		}

		// Match the old rename command's fail-fast behavior: reject missing,
		// unsafe, and reserved names before opening a daemon connection.
		if err := validateUpdateOptions(opts); err != nil {
			return err
		}

		c, err := client.Connect(cfg, paths, cfgFile)
		if err != nil {
			return err
		}
		defer c.Close()

		return runUpdate(c, args[0], opts)
	},
}

func validateUpdateOptions(opts updateOptions) error {
	if opts.name == nil && opts.parent == nil && opts.starred == nil && opts.addLabels == nil && opts.removeLabels == nil {
		return errors.New("at least one of --name, --parent, --starred, --add-label, or --remove-label must be specified")
	}

	if opts.name != nil {
		if err := daemon.ValidateSessionName(*opts.name); err != nil {
			return err
		}
	}

	adds, err := sessionlabel.Normalize(opts.addLabels)
	if err != nil {
		return err
	}

	removes, err := sessionlabel.Normalize(opts.removeLabels)
	if err != nil {
		return err
	}

	for _, add := range adds {
		if sessionlabel.Contains(removes, add) {
			return fmt.Errorf("label %q cannot be both added and removed", add)
		}
	}

	return nil
}

func runUpdate(c controlConn, nameOrID string, opts updateOptions) error {
	if err := validateUpdateOptions(opts); err != nil {
		return err
	}

	session, err := resolveUpdatableSessionInfo(c, nameOrID)
	if err != nil {
		return err
	}

	msg := protocol.UpdateMsg{
		SessionID:    session.ID,
		Name:         opts.name,
		Starred:      opts.starred,
		AddLabels:    opts.addLabels,
		RemoveLabels: opts.removeLabels,
	}
	if opts.parent != nil {
		if *opts.parent == "" {
			empty := ""
			msg.ParentID = &empty
		} else {
			parent, err := resolveUpdatableSessionInfo(c, *opts.parent)
			if err != nil {
				return fmt.Errorf("resolving parent: %w", err)
			}

			msg.ParentID = &parent.ID
		}
	}

	if err := c.SendControl("update", msg); err != nil {
		return err
	}

	resp, err := c.ReadControlResponse()
	if err != nil {
		return err
	}

	if resp.Type == "error" {
		return fmt.Errorf("%s", errorMessage(resp))
	}

	if resp.Type != "updated" {
		return fmt.Errorf("unexpected update response: %s", resp.Type)
	}

	var result protocol.UpdateResultMsg
	if err := protocol.DecodePayload(resp, &result); err != nil {
		return fmt.Errorf("decoding update response: %w", err)
	}

	if jsonOutput {
		return out.JSON(result)
	}

	if opts.name != nil {
		out.Printf("Name: %s\n", result.Name)
	}

	if opts.parent != nil {
		if result.ParentID == "" {
			out.Printf("Parent: none\n")
		} else {
			out.Printf("Parent: %s\n", result.ParentID)
		}
	}

	if opts.starred != nil {
		out.Printf("Starred: %t\n", result.Starred)
	}

	if opts.addLabels != nil || opts.removeLabels != nil {
		if len(result.Labels) == 0 {
			out.Printf("Labels: none\n")
		} else {
			out.Printf("Labels: %s\n", strings.Join(result.Labels, ", "))
		}
	}

	return nil
}

// resolveUpdatableSessionInfo resolves a live session by exact ID or unambiguous
// name. If the reference only exists in the soft-delete list, return the same
// explicit recovery guidance as the daemon's raw-ID update guard.
func resolveUpdatableSessionInfo(c controlConn, nameOrID string) (*protocol.SessionInfo, error) {
	live, err := listSessions(c, false)
	if err != nil {
		return nil, err
	}

	// An exact live ID is authoritative and needs no deleted-list lookup.
	for i := range live {
		if live[i].ID == nameOrID {
			return &live[i], nil
		}
	}

	// Before treating the input as a live display name, rule out an exact ID in
	// the trash. Otherwise a live session named after a deleted session's ID
	// could be updated when the caller explicitly targeted the deleted session.
	deleted, err := listSessions(c, true)
	if err != nil {
		return nil, err
	}

	for i := range deleted {
		if deleted[i].ID == nameOrID {
			return nil, fmt.Errorf("session %q is soft-deleted; `gr restore` it first", deleted[i].Name)
		}
	}

	session, err := resolveByNameOrID(nameOrID, live)
	if err == nil {
		return session, nil
	}

	var notFound *sessionNotFoundError
	if !errors.As(err, &notFound) {
		return nil, err
	}

	deletedSession, deletedErr := resolveByNameOrID(nameOrID, deleted)
	if deletedErr == nil {
		return nil, fmt.Errorf("session %q is soft-deleted; `gr restore` it first", deletedSession.Name)
	}

	if !errors.As(deletedErr, &notFound) {
		return nil, deletedErr
	}

	return nil, err
}

// registerUpdateCmd registers this command on rootCmd. Called from registerCommands.
func registerUpdateCmd() {
	updateCmd.Flags().String("name", "", "new session name")
	updateCmd.Flags().String("parent", "", "new parent session (empty string to orphan)")
	updateCmd.Flags().Bool("starred", false, "set whether the session is starred (bare flag means true)")
	updateCmd.Flags().StringArray("add-label", nil, "add a session label (repeatable)")
	updateCmd.Flags().StringArray("remove-label", nil, "remove a session label (repeatable)")
	_ = updateCmd.RegisterFlagCompletionFunc("parent", func(cmd *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return completeSessionNames(cmd, nil, toComplete)
	})
	rootCmd.AddCommand(updateCmd)
}
