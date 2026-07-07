package cli

import (
	"fmt"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

var interruptCmd = &cobra.Command{
	Use:   "interrupt <name-or-id>",
	Short: "Send an interrupt (Ctrl-C) to a session's agent",
	Long: "Interrupt a running agent by sending Ctrl-C to its PTY. The number of\n" +
		"presses and the delay between them follow the agent's per-agent config\n" +
		"(interrupt_count / interrupt_delay_ms) — Claude, for example, needs two\n" +
		"rapid presses to actually interrupt.",
	Args:              cobra.ExactArgs(1),
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

		if err := c.SendControl("interrupt", protocol.InterruptMsg{
			SessionID: sessionID,
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

		out.Printf("Interrupted session\n")

		return nil
	},
}

// registerInterruptCmd registers this command on rootCmd. Called from registerCommands.
func registerInterruptCmd() {
	rootCmd.AddCommand(interruptCmd)
}
