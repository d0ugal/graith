package cli

import (
	"fmt"
	"net"
	"os"
	"time"

	"github.com/dougalmatthews/graith/internal/client"
	"github.com/dougalmatthews/graith/internal/daemon"
	"github.com/dougalmatthews/graith/internal/protocol"
	"github.com/spf13/cobra"
)

var adoptFrom string

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Manage the graith daemon",
}

var daemonStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the daemon",
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
		out.Print("Daemon stopped\n")
		return nil
	},
}

var daemonRestartCmd = &cobra.Command{
	Use:   "restart",
	Short: "Stop and restart the daemon (sessions are preserved as stopped)",
	RunE: func(cmd *cobra.Command, args []string) error {
		_ = daemon.StopDaemon(paths.PIDFile)

		// Wait for the old socket to be cleaned up before starting a new daemon
		for range 20 {
			if _, err := os.Stat(paths.SocketPath); os.IsNotExist(err) {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		os.Remove(paths.SocketPath)

		conn, err := client.EnsureDaemon(paths.SocketPath, cfgFile)
		if err != nil {
			return fmt.Errorf("restart daemon: %w", err)
		}
		conn.Close()
		out.Print("Daemon restarted\n")
		return nil
	},
}

var daemonUpgradeCmd = &cobra.Command{
	Use:   "upgrade",
	Short: "Upgrade daemon binary without losing sessions",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := client.New(cfg, paths, cfgFile)
		if err != nil {
			return fmt.Errorf("connect to daemon: %w", err)
		}

		if err := c.Handshake(); err != nil {
			c.Close()
			return err
		}
		c.ReadControlResponse()

		c.SendControl("upgrade", struct{}{})
		resp, err := c.ReadControlResponse()
		if err != nil {
			// Connection dropped — expected, the daemon exec'd itself
			c.Close()

			// Wait for the new daemon to be ready
			for range 20 {
				time.Sleep(250 * time.Millisecond)
				conn, err := net.DialTimeout("unix", paths.SocketPath, 500*time.Millisecond)
				if err == nil {
					conn.Close()
					out.Print("Daemon upgraded successfully\n")
					return nil
				}
			}
			return fmt.Errorf("new daemon not responding after upgrade")
		}

		c.Close()
		if resp.Type == "error" {
			var e protocol.ErrorMsg
			protocol.DecodePayload(resp, &e)
			return fmt.Errorf("upgrade failed: %s", e.Message)
		}
		out.Print("Daemon upgrade initiated\n")
		return nil
	},
}

func init() {
	rootCmd.AddCommand(daemonCmd)
	daemonCmd.AddCommand(daemonStartCmd)
	daemonCmd.AddCommand(daemonStopCmd)
	daemonCmd.AddCommand(daemonRestartCmd)
	daemonCmd.AddCommand(daemonUpgradeCmd)

	daemonStartCmd.Flags().StringVar(&adoptFrom, "adopt-from", "", "")
	daemonStartCmd.Flags().MarkHidden("adopt-from")
}
