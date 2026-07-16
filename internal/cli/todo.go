package cli

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

var todoCmd = &cobra.Command{
	Use:   "todo",
	Short: "Manage the shared, persistent todo list",
	Long: `Manage a durable todo list owned by the daemon.

Items are scoped to a session subtree (the default — the calling session and its
children share one list, anchored at the subtree root) or to a scenario
(--scenario). Claiming is atomic: parallel agents pulling from a shared list
never grab the same item twice.`,
}

// todoScopeFromFlags builds a TodoScope from the shared --scenario/--session/--all flags.
func todoScopeFromFlags(cmd *cobra.Command) protocol.TodoScope {
	scenario, _ := cmd.Flags().GetString("scenario")
	session, _ := cmd.Flags().GetString("session")
	all, _ := cmd.Flags().GetBool("all")

	return protocol.TodoScope{Scenario: scenario, Session: session, All: all}
}

// todoRoundTrip sends a control message and returns the response, translating an
// error envelope into a Go error.
func todoRoundTrip(msgType string, payload any) (protocol.Envelope, error) {
	c, err := client.Connect(cfg, paths, cfgFile)
	if err != nil {
		return protocol.Envelope{}, err
	}
	defer c.Close()

	if err := c.SendControl(msgType, payload); err != nil {
		return protocol.Envelope{}, err
	}

	resp, err := c.ReadControlResponse()
	if err != nil {
		return protocol.Envelope{}, err
	}

	if resp.Type == "error" {
		var e protocol.ErrorMsg

		_ = protocol.DecodePayload(resp, &e)

		return protocol.Envelope{}, fmt.Errorf("%s", e.Message)
	}

	return resp, nil
}

// printTodoItem renders a single item reply (human) or passes JSON through.
func printTodoItem(resp protocol.Envelope, verb string) error {
	if jsonOutput {
		return out.JSON(resp.Payload)
	}

	var r protocol.TodoResponse
	if err := protocol.DecodePayload(resp, &r); err != nil {
		return err
	}

	out.Printf("%s %s: %s [%s]\n", verb, r.Item.ID, r.Item.Title, r.Item.Status)

	return nil
}

var todoAddCmd = &cobra.Command{
	Use:   "add <title>",
	Short: "Add a todo item",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		tags, _ := cmd.Flags().GetStringArray("tag")
		parent, _ := cmd.Flags().GetString("parent")
		note, _ := cmd.Flags().GetString("note")
		assign, _ := cmd.Flags().GetString("assign")

		resp, err := todoRoundTrip("todo_add", protocol.TodoAddMsg{
			Scope: todoScopeFromFlags(cmd), Title: args[0], Tags: tags,
			ParentID: parent, Note: note, Assignee: assign,
		})
		if err != nil {
			return err
		}

		return printTodoItem(resp, "Added")
	},
}

var todoListCmd = &cobra.Command{
	Use:   "list",
	Short: "List todo items in the current scope",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		status, _ := cmd.Flags().GetString("status")
		tag, _ := cmd.Flags().GetString("tag")

		resp, err := todoRoundTrip("todo_list", protocol.TodoListMsg{
			Scope: todoScopeFromFlags(cmd), Status: status, Tag: tag,
		})
		if err != nil {
			return err
		}

		if jsonOutput {
			return out.JSON(resp.Payload)
		}

		var r protocol.TodoListResponse
		if err := protocol.DecodePayload(resp, &r); err != nil {
			return err
		}

		if len(r.Items) == 0 {
			out.Printf("No todo items.\n")

			return nil
		}

		tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		_, _ = fmt.Fprintf(tw, "ID\tSTATUS\tTITLE\tOWNER\tTAGS\n")

		for _, it := range r.Items {
			title := it.Title
			if it.ParentID != "" {
				title = "  " + title
			}

			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", it.ID, it.Status, title, it.Owner, strings.Join(it.Tags, ","))
		}

		_ = tw.Flush()

		return nil
	},
}

func todoClaimRun(cmd *cobra.Command, args []string) error {
	id := ""
	if len(args) == 1 {
		id = args[0]
	}

	resp, err := todoRoundTrip("todo_claim", protocol.TodoClaimMsg{ID: id, Scope: todoScopeFromFlags(cmd)})
	if err != nil {
		return err
	}

	if jsonOutput {
		return out.JSON(resp.Payload)
	}

	var r protocol.TodoClaimResponse
	if err := protocol.DecodePayload(resp, &r); err != nil {
		return err
	}

	if !r.Claimed {
		if id != "" {
			out.Printf("Item %s was already claimed.\n", id)
		} else {
			out.Printf("No unclaimed items in scope.\n")
		}

		return nil
	}

	out.Printf("Claimed %s: %s\n", r.Item.ID, r.Item.Title)

	return nil
}

var todoClaimCmd = &cobra.Command{
	Use:   "claim <id>",
	Short: "Atomically claim a specific item",
	Args:  cobra.ExactArgs(1),
	RunE:  todoClaimRun,
}

var todoStartCmd = &cobra.Command{
	Use:   "start <id>",
	Short: "Alias for claim",
	Args:  cobra.ExactArgs(1),
	RunE:  todoClaimRun,
}

var todoNextCmd = &cobra.Command{
	Use:   "next",
	Short: "Claim the next unclaimed item in scope",
	Args:  cobra.NoArgs,
	RunE:  todoClaimRun,
}

func todoTransitionCmd(use, short, status string, wantNote bool) *cobra.Command {
	// Only `block` takes an optional note as a second arg; done/reopen take just
	// the id so a stray note isn't silently swallowed.
	args := cobra.ExactArgs(1)
	if wantNote {
		args = cobra.RangeArgs(1, 2)
	}

	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  args,
		RunE: func(cmd *cobra.Command, args []string) error {
			note := ""
			if wantNote && len(args) == 2 {
				note = args[1]
			}

			resp, err := todoRoundTrip("todo_transition", protocol.TodoTransitionMsg{
				ID: args[0], Status: status, Note: note,
			})
			if err != nil {
				return err
			}

			return printTodoItem(resp, "Updated")
		},
	}
}

var todoRemoveCmd = &cobra.Command{
	Use:     "rm <id>",
	Aliases: []string{"remove"},
	Short:   "Remove a todo item (and its sub-items)",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		resp, err := todoRoundTrip("todo_remove", protocol.TodoRemoveMsg{ID: args[0]})
		if err != nil {
			return err
		}

		if jsonOutput {
			return out.JSON(resp.Payload)
		}

		out.Printf("Removed %s\n", args[0])

		return nil
	},
}

var todoAssignCmd = &cobra.Command{
	Use:   "assign <id> <session-id>",
	Short: "Assign an item to a scenario member (orchestrator/human)",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		resp, err := todoRoundTrip("todo_assign", protocol.TodoAssignMsg{ID: args[0], Assignee: args[1]})
		if err != nil {
			return err
		}

		return printTodoItem(resp, "Assigned")
	},
}

var todoExportCmd = &cobra.Command{
	Use:   "export",
	Short: "Export the current scope's items to the document store",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		format, _ := cmd.Flags().GetString("format")

		resp, err := todoRoundTrip("todo_export", protocol.TodoExportMsg{
			Scope: todoScopeFromFlags(cmd), Format: format,
		})
		if err != nil {
			return err
		}

		if jsonOutput {
			return out.JSON(resp.Payload)
		}

		var r protocol.TodoExportResponse
		if err := protocol.DecodePayload(resp, &r); err != nil {
			return err
		}

		out.Printf("Exported to store: %s\n", r.Key)

		return nil
	},
}

func registerTodoCmd() {
	// Shared scope flags on the parent so every subcommand inherits them.
	todoCmd.PersistentFlags().String("scenario", "", "target a scenario's shared list by name")
	todoCmd.PersistentFlags().String("session", "", "target a specific session's subtree list")
	todoCmd.PersistentFlags().Bool("all", false, "span every scope (human/orchestrator, list only)")

	todoAddCmd.Flags().StringArray("tag", nil, "tag (repeatable)")
	todoAddCmd.Flags().String("parent", "", "parent item id (create a sub-item)")
	todoAddCmd.Flags().String("note", "", "optional one-line note")
	todoAddCmd.Flags().String("assign", "", "assign to a session id (scenario completion)")

	todoListCmd.Flags().String("status", "", "filter by status (todo/in-progress/done/blocked)")
	todoListCmd.Flags().String("tag", "", "filter by tag")

	todoExportCmd.Flags().String("format", "md", "export format (md|json)")

	doneCmd := todoTransitionCmd("done <id>", "Mark an item done", "done", false)
	blockCmd := todoTransitionCmd("block <id> [note]", "Mark an item blocked", "blocked", true)
	reopenCmd := todoTransitionCmd("reopen <id>", "Reopen an item (clears the owner)", "todo", false)

	todoCmd.AddCommand(todoAddCmd, todoListCmd, todoClaimCmd, todoStartCmd, todoNextCmd,
		doneCmd, blockCmd, reopenCmd, todoRemoveCmd, todoAssignCmd, todoExportCmd)

	rootCmd.AddCommand(todoCmd)
}
