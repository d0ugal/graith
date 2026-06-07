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

var deleteForce bool

var deleteCmd = &cobra.Command{
	Use:               "delete <name-or-id>",
	Aliases:           []string{"rm"},
	Short:             "Delete a session by name or ID",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeSessionNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := client.Connect(cfg, paths, cfgFile)
		if err != nil {
			return err
		}
		defer c.Close()

		session, err := resolveSessionInfo(c, args[0])
		if err != nil {
			return err
		}

		if !deleteForce && session.WorktreePath != "" {
			confirmed, err := confirmDelete(session)
			if err != nil {
				return err
			}
			if !confirmed {
				return nil
			}
		}

		c.SendControl("delete", protocol.DeleteMsg{SessionID: session.ID})
		resp, err := c.ReadControlResponse()
		if err != nil {
			return err
		}
		if resp.Type == "error" {
			var e protocol.ErrorMsg
			protocol.DecodePayload(resp, &e)
			return fmt.Errorf("%s", e.Message)
		}

		out.Print("Session deleted\n")
		return nil
	},
}

func resolveSessionInfo(c *client.Client, nameOrID string) (*protocol.SessionInfo, error) {
	c.SendControl("list", struct{}{})
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

func confirmDelete(session *protocol.SessionInfo) (bool, error) {
	dirtyFiles, dirtyErr := git.DirtyFiles(session.WorktreePath)
	unpushedCommits, unpushedErr := git.UnpushedCommitSummaries(session.WorktreePath, session.BaseBranch)

	gitFailed := dirtyErr != nil || (session.BaseBranch != "" && unpushedErr != nil)

	if len(dirtyFiles) == 0 && len(unpushedCommits) == 0 && !gitFailed {
		return true, nil
	}

	if out.IsJSON() {
		return false, fmt.Errorf("session %q has uncommitted changes or unpushed commits; use --force to delete", session.Name)
	}

	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return false, fmt.Errorf("session %q has uncommitted changes or unpushed commits; use --force to delete", session.Name)
	}

	out.Print("Session %q has unsaved work:\n\n", session.Name)

	if len(dirtyFiles) > 0 {
		out.Print("  Dirty files:\n")
		for _, f := range dirtyFiles {
			out.Print("    %s\n", f)
		}
		out.Print("\n")
	}

	if len(unpushedCommits) > 0 {
		out.Print("  Unpushed commits:\n")
		for _, c := range unpushedCommits {
			out.Print("    %s\n", c)
		}
		out.Print("\n")
	}

	if gitFailed {
		out.Print("  Warning: could not fully check worktree status\n\n")
	}

	out.Print("Delete anyway? [y/N] ")
	reader := bufio.NewReader(os.Stdin)
	answer, err := reader.ReadString('\n')
	if err != nil {
		return false, err
	}
	answer = strings.TrimSpace(strings.ToLower(answer))
	if answer != "y" && answer != "yes" {
		out.Print("Aborted\n")
		return false, nil
	}
	return true, nil
}

func init() {
	deleteCmd.Flags().BoolVarP(&deleteForce, "force", "f", false, "Skip confirmation for sessions with unsaved work")
	rootCmd.AddCommand(deleteCmd)
}
