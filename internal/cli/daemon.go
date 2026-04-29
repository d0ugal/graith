package cli

import (
	"fmt"
	"net"
	"os"
	"time"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/daemon"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

func upgradeMsg() protocol.UpgradeMsg {
	execPath, _ := os.Executable()
	return protocol.UpgradeMsg{ExecPath: execPath}
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
		out.Print("Daemon stopped\n")
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
			out.Print("Preserve failed: %s\nFalling back to clean restart...\n", err)
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
			conn, startErr := client.EnsureDaemon(paths.SocketPath, cfgFile)
			if startErr != nil {
				return fmt.Errorf("start daemon: %w", startErr)
			}
			conn.Close()
			out.Print("Daemon started with current config\n")
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
			protocol.DecodePayload(resp, &e)
			return fmt.Errorf("%s", e.Message)
		}
		out.Print("Config reloaded\n")
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
	c.ReadControlResponse()

	c.SendControl("upgrade", upgradeMsg())
	resp, err := c.ReadControlResponse()
	if err != nil {
		// Connection dropped — expected, the daemon exec'd itself
		c.Close()

		for range 20 {
			time.Sleep(250 * time.Millisecond)
			conn, err := net.DialTimeout("unix", paths.SocketPath, 500*time.Millisecond)
			if err == nil {
				conn.Close()
				out.Print("%s\n", successMsg)
				return nil
			}
		}
		return fmt.Errorf("new daemon not responding after exec")
	}

	c.Close()
	if resp.Type == "error" {
		var e protocol.ErrorMsg
		protocol.DecodePayload(resp, &e)
		return fmt.Errorf("%s", e.Message)
	}
	out.Print("%s\n", successMsg)
	return nil
}

func restartClean() error {
	_ = daemon.StopDaemon(paths.PIDFile)

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
	out.Print("Daemon restarted (sessions killed)\n")
	return nil
}

func init() {
	rootCmd.AddCommand(daemonCmd)
	daemonCmd.AddCommand(daemonStartCmd)
	daemonCmd.AddCommand(daemonStopCmd)
	daemonCmd.AddCommand(daemonRestartCmd)
	daemonCmd.AddCommand(daemonReloadCmd)
	daemonStartCmd.Flags().StringVar(&adoptFrom, "adopt-from", "", "")
	daemonStartCmd.Flags().MarkHidden("adopt-from")

	daemonRestartCmd.Flags().BoolVar(&forceClean, "force", false, "Kill sessions and do a clean stop/start")
}
