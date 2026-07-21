package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/output"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

var pathSelf bool

var pathCmd = &cobra.Command{
	Use:   "path [name-or-id]",
	Short: "Print the working directory for a session",
	Long: `Print the authoritative working directory assigned to a session.
Use with cd: cd "$(gr path <name>)". From inside a session, use: cd "$(gr path --self)".`,
	Args: pathArgs,
	ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if pathSelf {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}

		return completeSessionNames(cmd, args, toComplete)
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		args, err := selfArgs(pathSelf, args)
		if err != nil {
			return err
		}

		c, err := client.Connect(cfg, paths, cfgFile)
		if err != nil {
			return err
		}
		defer c.Close()

		return resolveAndPrintPath(c, cmd.OutOrStdout(), out, args[0])
	},
}

func pathArgs(cmd *cobra.Command, args []string) error {
	if pathSelf {
		if len(args) != 0 {
			return errors.New("--self cannot be combined with a session argument")
		}

		return nil
	}

	return cobra.ExactArgs(1)(cmd, args)
}

func resolveAndPrintPath(c controlConn, w io.Writer, o *output.Writer, nameOrID string) error {
	session, err := resolveSessionInfo(c, nameOrID)
	if err != nil {
		return err
	}

	return printPath(w, o, session, nameOrID)
}

func printPath(w io.Writer, o *output.Writer, session *protocol.SessionInfo, nameOrID string) error {
	if session.CWD == "" {
		return fmt.Errorf("session %q has no configured cwd", nameOrID)
	}

	if !filepath.IsAbs(session.CWD) {
		return fmt.Errorf("session %q has invalid cwd %q: path is not absolute", nameOrID, session.CWD)
	}

	info, err := os.Stat(session.CWD)
	if err != nil {
		return fmt.Errorf("session %q cwd %q is unavailable: %w", nameOrID, session.CWD, err)
	}

	if !info.IsDir() {
		return fmt.Errorf("session %q cwd %q is not a directory", nameOrID, session.CWD)
	}

	if o.IsJSON() {
		return o.JSON(map[string]string{
			"session_id": session.ID,
			"name":       session.Name,
			"cwd":        session.CWD,
		})
	}

	_, _ = fmt.Fprint(w, session.CWD)

	return nil
}

// registerPathCmd registers this command on rootCmd. Called from registerCommands.
func registerPathCmd() {
	pathCmd.Flags().BoolVar(&pathSelf, "self", false, "resolve the current session (from GRAITH_SESSION_ID/NAME)")
	rootCmd.AddCommand(pathCmd)
}
