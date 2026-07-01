package cli

import (
	"fmt"
	"strings"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

var typeCmd = &cobra.Command{
	Use:               "type <name-or-id> <text>",
	Aliases:           []string{"t"},
	Short:             "Type text into a session's PTY stdin",
	Args:              cobra.MinimumNArgs(2),
	ValidArgsFunction: completeSessionNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := client.Connect(cfg, paths, cfgFile)
		if err != nil {
			return err
		}
		defer c.Close()

		sessionID, err := resolveSession(c, args[0])
		if err != nil {
			return err
		}

		noNewline, _ := cmd.Flags().GetBool("no-newline")
		text := strings.Join(args[1:], " ")

		if err := c.SendControl("type", protocol.TypeMsg{
			SessionID: sessionID,
			Input:     text,
			NoNewline: noNewline,
		}); err != nil {
			return err
		}

		resp, err := c.ReadControlResponse()
		if err != nil {
			return err
		}

		if resp.Type == "error" {
			var e protocol.ErrorMsg

			_ = protocol.DecodePayload(resp, &e)

			return fmt.Errorf("%s", e.Message)
		}

		out.Printf("Typed into session\n")

		return nil
	},
}

// registerTypeCmd registers this command on rootCmd. Called from registerCommands.
func registerTypeCmd() {
	typeCmd.Flags().Bool("no-newline", false, "Do not append a newline after the text")
	rootCmd.AddCommand(typeCmd)
}
