package cli

import (
	"fmt"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

// setSessionStar resolves nameOrID, sends the star/unstar control message built
// from the resolved session ID, and reports success. starCmd and unstarCmd
// differ only by the message type, payload, and success line.
func setSessionStar(nameOrID, msgType string, makeMsg func(sessionID string) any, successMsg string) error {
	c, err := client.Connect(cfg, paths, cfgFile)
	if err != nil {
		return err
	}
	defer c.Close()

	sessionID, err := resolveSession(c, nameOrID)
	if err != nil {
		return err
	}

	_ = c.SendControl(msgType, makeMsg(sessionID))

	resp, err := c.ReadControlResponse()
	if err != nil {
		return err
	}

	if resp.Type == "error" {
		var e protocol.ErrorMsg

		_ = protocol.DecodePayload(resp, &e)

		return fmt.Errorf("%s", e.Message)
	}

	out.Printf("%s\n", successMsg)

	return nil
}

var starCmd = &cobra.Command{
	Use:               "star <name-or-id>",
	Short:             "Star a session (protects from deletion)",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeSessionNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		return setSessionStar(args[0], "star",
			func(id string) any { return protocol.StarMsg{SessionID: id} },
			"Session starred")
	},
}

var unstarCmd = &cobra.Command{
	Use:               "unstar <name-or-id>",
	Short:             "Unstar a session",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeSessionNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		return setSessionStar(args[0], "unstar",
			func(id string) any { return protocol.UnstarMsg{SessionID: id} },
			"Session unstarred")
	},
}

// registerStarCmd registers this command on rootCmd. Called from registerCommands.
func registerStarCmd() {
	rootCmd.AddCommand(starCmd)
	rootCmd.AddCommand(unstarCmd)
}
