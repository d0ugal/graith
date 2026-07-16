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

func execUpgrade(successMsg string) error {
	c, err := client.New(cfg, paths, cfgFile)
	if err != nil {
		return fmt.Errorf("connect to daemon: %w", err)
	}

	if err := c.Handshake(); err != nil {
		c.Close()
		return err
	}

	_, _ = c.ReadControlResponse()

	_ = c.SendControl("upgrade", upgradeMsg())

	resp, err := c.ReadControlResponse()
	if err != nil {
		// Connection dropped — expected, the daemon exec'd itself
		c.Close()

		for range 20 {
			time.Sleep(250 * time.Millisecond)

			conn, err := dialLocalDaemonSocket()
			if err == nil {
				_ = conn.Close()
				break
			}
		}
	} else {
		c.Close()

		if resp.Type == "error" {
			var e protocol.ErrorMsg

			_ = protocol.DecodePayload(resp, &e)

			return fmt.Errorf("%s", e.Message)
		}
		// Got "upgrading" — give the daemon a moment to exec.
		time.Sleep(500 * time.Millisecond)
	}

	// Verify the daemon is running our version. Old daemons that don't
	// understand ExecPath will exec back into the old binary.
	if v := probeDaemonVersion(); v != "" && v != version.Version {
		return fmt.Errorf("daemon exec'd into %s instead of %s", v, version.Version)
	}

	out.Printf("%s\n", successMsg)

	return nil
}

func probeDaemonVersion() string {
	conn, err := dialLocalDaemonSocket()
	if err != nil {
		return ""
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
		return ""
	}

	env, _ := protocol.DecodeControl(frame.Payload)

	var hsOk protocol.HandshakeOkMsg
	if err := protocol.DecodePayload(env, &hsOk); err != nil {
		return ""
	}

	return hsOk.DaemonVersion
}

func restartClean() error {
	if err := daemon.StopDaemon(paths.PIDFile); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: %v\n", err)
	}

	for range 20 {
		if _, err := os.Stat(paths.SocketPath); os.IsNotExist(err) {
			break
		}

		time.Sleep(100 * time.Millisecond)
	}

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
