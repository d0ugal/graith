package cli

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/daemon"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/d0ugal/graith/internal/version"
	"github.com/spf13/cobra"
)

func upgradeMsg() protocol.UpgradeMsg {
	execPath, _ := os.Executable()
	return protocol.UpgradeMsg{ExecPath: execPath, ClientVersion: version.Version}
}

var (
	adoptFrom  string
	forceClean bool
)

var daemonCmd = &cobra.Command{
	Use:     "daemon",
	Aliases: []string{"d"},
	Short:   "Manage the graith daemon",
}

var daemonStartCmd = &cobra.Command{
	Use:    "start",
	Short:  "Start the daemon",
	Hidden: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return daemon.Run(cfg, paths, cfgFile, adoptFrom)
	},
}

var daemonStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the daemon",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := daemon.StopDaemon(paths.PIDFile); err != nil {
			return err
		}

		out.Printf("Daemon stopped\n")

		return nil
	},
}

var daemonRestartCmd = &cobra.Command{
	Use:   "restart",
	Short: "Restart the daemon (preserves sessions by default)",
	Long: `Restart the daemon, picking up the latest binary and config.

By default, live sessions are preserved via exec.
Use --force to do a clean stop/start, which kills running agent sessions.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if forceClean {
			return restartClean()
		}

		err := execUpgrade("Daemon restarted (sessions preserved)")
		if err != nil {
			out.Printf("Preserve failed: %s\nFalling back to clean restart...\n", err)
			return restartClean()
		}

		return nil
	},
}

var daemonReloadCmd = &cobra.Command{
	Use:   "reload",
	Short: "Reload config without restarting the daemon",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := client.Connect(cfg, paths, cfgFile)
		if err != nil {
			conn, startErr := client.EnsureDaemon(paths, cfgFile)
			if startErr != nil {
				return fmt.Errorf("start daemon: %w", startErr)
			}

			_ = conn.Close()

			out.Printf("Daemon started with current config\n")

			return nil
		}
		defer c.Close()

		if err := c.SendControl("reload", struct{}{}); err != nil {
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

		out.Printf("Config reloaded\n")

		return nil
	},
}

// upgradeExchangeConn is the connection surface execUpgrade drives for the
// handshake + upgrade round-trip. It is an interface (satisfied by
// *client.Client) so the deadline installation and readiness handoff can be
// tested against a scripted connection without a live daemon.
type upgradeExchangeConn interface {
	SetDeadline(deadline time.Time) error
	Handshake() error
	SendControl(messageType string, payload any) error
	ReadControlResponse() (protocol.Envelope, error)
	Close()
}

// dialUpgradeClient opens the daemon connection execUpgrade drives. A package
// var so tests can substitute a scripted connection.
var dialUpgradeClient = func() (upgradeExchangeConn, error) {
	return client.New(cfg, paths, cfgFile)
}

func execUpgrade(successMsg string) error {
	c, err := dialUpgradeClient()
	if err != nil {
		return fmt.Errorf("connect to daemon: %w", err)
	}

	// client.New leaves the connection deadline-free, so the raw handshake and
	// reads below would hang forever against a stale/foreign daemon that accepts
	// but never replies. Bound the whole handshake + upgrade exchange with the
	// configured local handshake deadline — distinct from the post-exec startup
	// budget the readiness wait uses (issue #1242). A connection that can't accept
	// the deadline can't be safely bounded, so fail rather than proceed unbounded.
	if err := c.SetDeadline(connectionNow().Add(localDaemonHandshakeTimeout())); err != nil {
		c.Close()
		return fmt.Errorf("set upgrade handshake deadline: %w", err)
	}

	if err := c.Handshake(); err != nil {
		c.Close()
		return err
	}

	// Capture the daemon's pre-upgrade instance ID from its handshake_ok so
	// readiness below can prove the NEW generation is serving rather than the
	// inherited listener (issue #1319). This capture MUST succeed: if the read
	// fails, the reply isn't handshake_ok, or it doesn't decode, we cannot
	// establish the pre-upgrade generation — and a silently-empty priorInstanceID
	// would let the unchanged old daemon (any non-empty instance ID) satisfy the
	// "id != prior" readiness check and be falsely accepted as the replacement.
	// Fail the exchange instead. A successfully decoded (possibly legacy)
	// handshake_ok with a genuinely absent instance ID may keep "" for backward
	// compatibility.
	hsResp, err := c.ReadControlResponse()
	if err != nil {
		c.Close()
		return fmt.Errorf("read daemon handshake before upgrade: %w", err)
	}

	if hsResp.Type != "handshake_ok" {
		c.Close()
		return fmt.Errorf("unexpected pre-upgrade handshake response %q", hsResp.Type)
	}

	var hsOk protocol.HandshakeOkMsg
	if err := protocol.DecodePayload(hsResp, &hsOk); err != nil {
		c.Close()
		return fmt.Errorf("decode daemon handshake before upgrade: %w", err)
	}

	priorInstanceID := hsOk.DaemonInstanceID

	// Propagate a send failure rather than waiting for the readiness of an upgrade
	// that was never requested.
	if err := c.SendControl("upgrade", upgradeMsg()); err != nil {
		c.Close()
		return fmt.Errorf("send upgrade request: %w", err)
	}

	resp, err := c.ReadControlResponse()
	c.Close()

	// A decodable "error" reply means the daemon refused the upgrade outright;
	// surface it. Otherwise the connection either dropped (the daemon exec'd
	// itself) or we got "upgrading" — both mean the replacement daemon is coming
	// up, so fall through to the readiness wait.
	if err == nil && resp.Type == "error" {
		var e protocol.ErrorMsg

		_ = protocol.DecodePayload(resp, &e)

		return fmt.Errorf("%s", e.Message)
	}

	// Wait for the NEW daemon generation to report our version AND a different
	// boot instance ID. exec preserves the inherited listening socket and a
	// same-version rebuild keeps the version string, so neither a bare dial nor a
	// version match distinguishes the old daemon from the new one — only a
	// changed instance ID proves the replacement is serving (issue #1319). Bound
	// the wait by the effective [connection] start policy (start_timeout /
	// start_poll_interval) instead of a fixed 20×250ms retry count. The probe's
	// own dial and handshake deadlines stay distinct from this budget.
	if v, ready := waitForNewLocalDaemonGeneration(version.Version, priorInstanceID); !ready {
		// Old daemons that don't understand ExecPath exec back into the old binary;
		// report that concrete mismatch. Otherwise the replacement never presented a
		// new generation within the budget (an inherited/old listener kept answering,
		// or it never became reachable) — a failure, not a silent success, so the
		// caller falls back to a clean restart.
		if v != "" && v != version.Version {
			return fmt.Errorf("daemon exec'd into %s instead of %s", v, version.Version)
		}

		return fmt.Errorf("daemon did not present a new %s generation within the start budget", version.Version)
	}

	out.Printf("%s\n", successMsg)

	return nil
}

func probeDaemonIdentity() (daemonVersion, instanceID string) {
	conn, err := dialLocalDaemonSocket()
	if err != nil {
		return "", ""
	}
	defer func() { _ = conn.Close() }()

	// A stale/foreign socket that accepts but never responds must not hang the
	// post-upgrade version check. This is the same configured local handshake
	// policy used by normal client connections and gr doctor.
	setLocalDaemonHandshakeDeadline(conn)

	reader := protocol.NewFrameReader(conn)
	writer := protocol.NewFrameWriter(conn)

	hs := client.BuildHandshake(paths, 0, 0, "")
	hs.ClientID = fmt.Sprintf("upgrade-check-%d", os.Getpid())
	hsData, _ := encodeLocalControl("handshake", hs, localAuthToken())
	_ = writer.WriteFrame(protocol.ChannelControl, hsData)

	frame, err := reader.ReadFrame()
	if err != nil || frame.Channel != protocol.ChannelControl {
		return "", ""
	}

	env, _ := protocol.DecodeControl(frame.Payload)

	var hsOk protocol.HandshakeOkMsg
	if err := protocol.DecodePayload(env, &hsOk); err != nil {
		return "", ""
	}

	return hsOk.DaemonVersion, hsOk.DaemonInstanceID
}

func restartClean() error {
	if err := daemon.StopDaemon(paths.PIDFile); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: %v\n", err)
	}

	// Wait for the stopped daemon's socket to disappear, bounded by the effective
	// [connection] start policy rather than a fixed 20×100ms retry count (issue
	// #1319).
	pollLocalDaemon(func() bool {
		_, err := os.Stat(paths.SocketPath)

		return os.IsNotExist(err)
	})

	if _, err := os.Stat(paths.SocketPath); err == nil {
		if conn, err := dialLocalDaemonSocket(); err == nil {
			_ = conn.Close()
			return errors.New("daemon is still running, cannot restart cleanly")
		}
	}

	_ = os.Remove(paths.SocketPath)

	conn, err := client.EnsureDaemon(paths, cfgFile)
	if err != nil {
		return fmt.Errorf("restart daemon: %w", err)
	}

	_ = conn.Close()

	out.Printf("Daemon restarted (sessions killed)\n")

	return nil
}

// registerDaemonCmd registers this command on rootCmd. Called from registerCommands.
func registerDaemonCmd() {
	rootCmd.AddCommand(daemonCmd)
	daemonCmd.AddCommand(daemonStartCmd)
	daemonCmd.AddCommand(daemonStopCmd)
	daemonCmd.AddCommand(daemonRestartCmd)
	daemonCmd.AddCommand(daemonReloadCmd)
	daemonStartCmd.Flags().StringVar(&adoptFrom, "adopt-from", "", "")
	_ = daemonStartCmd.Flags().MarkHidden("adopt-from")

	daemonRestartCmd.Flags().BoolVar(&forceClean, "force", false, "Kill sessions and do a clean stop/start")
}
