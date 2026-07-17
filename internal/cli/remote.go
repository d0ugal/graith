package cli

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var remoteCmd = &cobra.Command{
	Use:   "remote",
	Short: "Manage and attach to remote daemons over Tailscale (#615)",
}

var (
	remotePairPort    int
	remotePairProfile string
	remotePairLabel   string
)

var remotePairCmd = &cobra.Command{
	Use:   "pair <host>",
	Short: "Pair this device with a remote daemon (approve with `gr pair approve` on the host)",
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		return runRemotePair(paths, args[0], remotePairPort, remotePairProfile, remotePairLabel, client.PairRemote, out.Printf)
	},
}

// pairRemoteFn is the network pairing step, injectable so runRemotePair can be
// driven deterministically in tests without a live daemon.
type pairRemoteFn func(paths config.Paths, host string, port int, profile, deviceLabel, devicePubKey string) (*client.RemoteHost, error)

// runRemotePair drives `gr remote pair`: it ensures + durably persists the device
// key, runs the pairing exchange, and reports the result.
//
// Device-key establishment and the receipt-critical paired-host save are two
// separate, cross-process-locked transactions. The lock is deliberately absent
// from the potentially minutes-long PairRemote wait. completePairing persists the
// host against a freshly loaded store before its receipt ACK, so there is no
// follow-up save from a stale pre-wait snapshot (issues #1299 and #1330).
func runRemotePair(paths config.Paths, host string, port int, profile, label string, pair pairRemoteFn, printf func(string, ...any)) error {
	// Establish or reload the canonical identity under the cross-process store
	// lock. EnsureRemoteDeviceKey durably saves and releases that lock before the
	// human/network wait in pair begins (issue #1330).
	_, pubB64, err := client.EnsureRemoteDeviceKey(client.RemoteHostsPath(paths.DataDir))
	if err != nil {
		return err
	}

	if label == "" {
		label, _ = os.Hostname()
	}

	printf("Requesting pairing with %s:%d as %q — approve on the remote host: gr pair approve <id>\n", host, port, label)

	rh, err := pair(paths, host, port, profile, label, pubB64)
	if err != nil {
		return err
	}

	printf("Paired with %s (profile %q, TLS pin %s)\n", rh.Host, rh.Profile, rh.TLSPin)

	return nil
}

var remoteListCmd = &cobra.Command{
	Use:   "list",
	Short: "List paired remote hosts",
	Args:  cobra.NoArgs,
	RunE: func(_ *cobra.Command, _ []string) error {
		store, err := client.LoadRemoteHostStore(client.RemoteHostsPath(paths.DataDir))
		if err != nil {
			return err
		}

		names := store.Names() // sorted, deterministic

		if out.IsJSON() {
			type row struct {
				Host    string `json:"host"`
				Port    int    `json:"port"`
				Profile string `json:"profile"`
			}

			rows := make([]row, 0, len(names))
			for _, name := range names {
				h := store.Hosts[name]
				rows = append(rows, row{Host: h.Host, Port: h.Port, Profile: h.Profile})
			}

			return out.JSON(rows)
		}

		if len(names) == 0 {
			out.Printf("No paired remote hosts. Pair one with: gr remote pair <host>\n")
			return nil
		}

		for _, name := range names {
			h := store.Hosts[name]
			out.Printf("  %s:%d  profile=%q\n", h.Host, h.Port, h.Profile)
		}

		return nil
	},
}

var remoteAttachCmd = &cobra.Command{
	Use:   "attach <host>/<session>",
	Short: "Attach to a session on a paired remote daemon",
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		host, sessionArg, ok := strings.Cut(args[0], "/")
		if !ok || host == "" || sessionArg == "" {
			return fmt.Errorf("expected <host>/<session>, got %q", args[0])
		}

		store, err := client.LoadRemoteHostStore(client.RemoteHostsPath(paths.DataDir))
		if err != nil {
			return err
		}

		rh, candidates := store.Resolve(host)
		if rh == nil {
			if len(candidates) == 0 {
				return fmt.Errorf("no paired remote hosts — run: gr remote pair %s", host)
			}

			return fmt.Errorf("not paired with %q; paired hosts: %s (run: gr remote pair %s)",
				host, strings.Join(candidates, ", "), host)
		}

		priv, _, err := store.EnsureDeviceKey()
		if err != nil {
			return err
		}

		return runRemoteAttach(rh, priv, sessionArg)
	},
}

// runRemoteAttach attaches to a session on a paired remote daemon. It is a
// dedicated loop (NOT runAttachByID) so that reconnects re-dial the REMOTE host
// rather than the local daemon. Remote overlay / session-switching is not yet
// supported — those prefix actions detach with a notice instead of silently
// talking to the local daemon.
// remotePassthroughKeysFromConfig builds the prefix-action keys that a remote
// attach honours. Overlay/session-switching isn't supported over a remote
// connection, but every configured prefix binding must be mapped — identically
// to a local attach — so those prefix actions hit the "not yet supported —
// detaching" notice rather than silently forwarding the raw prefix/key bytes to
// the remote agent. In particular messages, approvals, and restart_session must
// be present: an omitted binding falls through the passthrough switch to the
// default arm, which re-injects {prefix, key} into the agent PTY (issue #1233).
// It delegates to the shared local builder so the two mappings can't drift.
func remotePassthroughKeysFromConfig() client.PassthroughKeys {
	return passthroughKeysFromConfig()
}

func runRemoteAttach(rh *client.RemoteHost, signer ed25519.PrivateKey, sessionArg string) error {
	if isInsideGraith() {
		return errors.New("cannot attach from inside a graith session")
	}

	cols, rows := client.FallbackGeometry()
	if w, h, gerr := term.GetSize(int(os.Stdout.Fd())); gerr == nil {
		cols, rows = uint16(w), uint16(h) //nolint:gosec // G115: terminal dims are small non-negative ints
	}

	keys := remotePassthroughKeysFromConfig()

	sessionID := ""

	for {
		c, err := client.ConnectRemote(paths, rh, signer, cols, rows)
		if err != nil {
			return err
		}

		if sessionID == "" {
			sessionID, err = resolveSession(c, sessionArg)
			if err != nil {
				c.Close()
				return err
			}
		}

		_ = c.SendControl("attach", protocol.AttachMsg{SessionID: sessionID})

		resp, err := c.ReadControlResponse()
		if err != nil {
			c.Close()
			return err
		}

		if resp.Type == "error" {
			var e protocol.ErrorMsg

			_ = protocol.DecodePayload(resp, &e)

			c.Close()

			return fmt.Errorf("%s", e.Message)
		}

		var info protocol.SessionInfo

		_ = protocol.DecodePayload(resp, &info)

		opts := client.PassthroughOpts{Keys: keys, SessionID: sessionID, Info: &info}
		opts.AutoPopApproval = cfg.Approvals.AutoPop
		opts.DragArrowKeys = cfg.Input.DragArrowKeys
		opts.DragArrowThreshold = cfg.Input.DragArrowThreshold

		result := c.RunPassthrough(context.Background(), opts) // closes c

		switch result {
		case client.ResultDetached, client.ResultQuit:
			return nil
		case client.ResultDisconnected:
			out.Printf("Connection lost. Reconnecting to %s...\n", rh.Host)
			continue
		default:
			out.Printf("(overlay and session switching are not yet supported over a remote attach — detaching)\n")
			return nil
		}
	}
}

// registerRemoteCmd registers this command on rootCmd. Called from registerCommands.
func registerRemoteCmd() {
	remotePairCmd.Flags().IntVar(&remotePairPort, "port", config.DefaultRemotePort, "remote daemon port")
	remotePairCmd.Flags().StringVar(&remotePairProfile, "profile", "", "remote daemon profile (if it runs a named profile)")
	remotePairCmd.Flags().StringVar(&remotePairLabel, "label", "", "device label shown to the remote human (default: hostname)")

	remoteCmd.AddCommand(remotePairCmd)
	remoteCmd.AddCommand(remoteListCmd)
	remoteCmd.AddCommand(remoteAttachCmd)
	rootCmd.AddCommand(remoteCmd)
}
