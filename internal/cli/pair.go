package cli

import (
	"fmt"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

// pairCmd groups the local-only device-pairing management commands for the
// remote control surface (design §B.2). These run over the local Unix socket
// (roleLocalHuman) — the daemon rejects them over the network.
var pairCmd = &cobra.Command{
	Use:   "pair",
	Short: "Manage remote device pairings (local only)",
}

var pairListCmd = &cobra.Command{
	Use:   "list",
	Short: "List pending pairing requests and paired devices",
	Args:  cobra.NoArgs,
	RunE: func(_ *cobra.Command, _ []string) error {
		c, err := client.Connect(cfg, paths, cfgFile)
		if err != nil {
			return err
		}
		defer c.Close()

		_ = c.SendControl("pair_list", struct{}{})

		resp, err := c.ReadControlResponse()
		if err != nil {
			return err
		}

		if resp.Type == "error" {
			return controlError(resp)
		}

		var pl protocol.PairListResponseMsg
		if err := protocol.DecodePayload(resp, &pl); err != nil {
			return err
		}

		if out.IsJSON() {
			return out.JSON(pl)
		}

		if len(pl.Pending) == 0 && len(pl.Paired) == 0 {
			out.Printf("No pending requests or paired devices.\n")
			return nil
		}

		if len(pl.Pending) > 0 {
			out.Printf("Pending pairing requests:\n")

			for _, p := range pl.Pending {
				out.Printf("  %s  %q  %s (%s)  requested %s\n", p.RequestID, p.DeviceLabel, p.TailnetUser, p.TailnetNode, p.RequestedAt)
			}
		}

		if len(pl.Paired) > 0 {
			out.Printf("Paired devices:\n")

			for _, d := range pl.Paired {
				out.Printf("  %s  %q  %s (%s)  paired %s\n", d.DeviceID, d.Label, d.TailnetUser, d.TailnetNode, d.CreatedAt)
			}
		}

		return nil
	},
}

var pairApproveCmd = &cobra.Command{
	Use:   "approve <request-id>",
	Short: "Approve a pending device pairing request",
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		c, err := client.Connect(cfg, paths, cfgFile)
		if err != nil {
			return err
		}
		defer c.Close()

		_ = c.SendControl("pair_approve", protocol.PairApproveMsg{RequestID: args[0]})

		resp, err := c.ReadControlResponse()
		if err != nil {
			return err
		}

		if resp.Type == "error" {
			return controlError(resp)
		}

		var pr protocol.PairResponseMsg
		if err := protocol.DecodePayload(resp, &pr); err != nil {
			return err
		}

		out.Printf("Device paired: %s\n", pr.DeviceID)
		out.Printf("The device received its credentials over its pairing connection.\n")

		if pr.TLSPinSPKI != "" {
			out.Printf("TLS SPKI pin: %s\n", pr.TLSPinSPKI)
		}

		return nil
	},
}

var pairRevokeCmd = &cobra.Command{
	Use:   "revoke <device-id>",
	Short: "Revoke a paired device (force-closes its live connections)",
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		c, err := client.Connect(cfg, paths, cfgFile)
		if err != nil {
			return err
		}
		defer c.Close()

		_ = c.SendControl("pair_revoke", protocol.PairRevokeMsg{DeviceID: args[0]})

		resp, err := c.ReadControlResponse()
		if err != nil {
			return err
		}

		if resp.Type == "error" {
			return controlError(resp)
		}

		out.Printf("Device revoked: %s\n", args[0])

		return nil
	},
}

func controlError(resp protocol.Envelope) error {
	var e protocol.ErrorMsg

	_ = protocol.DecodePayload(resp, &e)

	return fmt.Errorf("%s", e.Message)
}

// registerPairCmd registers this command on rootCmd. Called from registerCommands.
func registerPairCmd() {
	pairCmd.AddCommand(pairListCmd)
	pairCmd.AddCommand(pairApproveCmd)
	pairCmd.AddCommand(pairRevokeCmd)
	rootCmd.AddCommand(pairCmd)
}
