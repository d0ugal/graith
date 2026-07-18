package cli

import (
	"fmt"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

// remotePairingsCmd groups the local-only device-pairing administration
// commands under the remote control surface (design §B.2). These run over the
// local Unix socket (roleLocalHuman) — the daemon rejects them over the network.
var remotePairingsCmd = &cobra.Command{
	Use:   "pairings",
	Short: "Manage remote device pairings (local only)",
}

var remotePairingsListCmd = &cobra.Command{
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

		return writePairings(pl)
	},
}

var remotePairingsApproveCmd = &cobra.Command{
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

		writePairingApproval(pr)

		return nil
	},
}

var remotePairingsRevokeCmd = &cobra.Command{
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

		writePairingRevocation(args[0])

		return nil
	},
}

func writePairings(pl protocol.PairListResponseMsg) error {
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
}

func writePairingApproval(pr protocol.PairResponseMsg) {
	out.Printf("Device paired: %s\n", pr.DeviceID)
	out.Printf("The device received its credentials over its pairing connection.\n")

	if pr.TLSPinSPKI != "" {
		out.Printf("TLS SPKI pin: %s\n", pr.TLSPinSPKI)
	}
}

func writePairingRevocation(deviceID string) {
	out.Printf("Device revoked: %s\n", deviceID)
}

func controlError(resp protocol.Envelope) error {
	var e protocol.ErrorMsg

	_ = protocol.DecodePayload(resp, &e)

	return fmt.Errorf("%s", e.Message)
}

// registerRemotePairingsCmd registers pairing administration below remoteCmd.
// Called from registerRemoteCmd so no root-level compatibility command exists.
func registerRemotePairingsCmd() {
	remotePairingsCmd.AddCommand(remotePairingsListCmd)
	remotePairingsCmd.AddCommand(remotePairingsApproveCmd)
	remotePairingsCmd.AddCommand(remotePairingsRevokeCmd)
	remoteCmd.AddCommand(remotePairingsCmd)
}
