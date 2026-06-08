package cli

import (
	"fmt"
	"net"
	"os"
	"runtime"
	"time"

	"github.com/d0ugal/graith/internal/protocol"
	"github.com/d0ugal/graith/internal/sandbox"
	"github.com/d0ugal/graith/internal/version"
	"github.com/spf13/cobra"
)

var doctorAutofix bool

var doctorCmd = &cobra.Command{
	Use:     "doctor",
	Aliases: []string{"doc"},
	Short:   "Health checks and diagnostics",
	RunE: func(cmd *cobra.Command, args []string) error {
		ok := true

		out.Print("Checking graith health...\n\n")
		out.Print("  CLI version: %s (%s)\n", version.Version, version.CommitSHA)

		if _, err := os.Stat(paths.SocketPath); err == nil {
			conn, err := net.DialTimeout("unix", paths.SocketPath, 500*time.Millisecond)
			if err != nil {
				out.Print("  ✗ Socket exists but daemon not responding: %s\n", paths.SocketPath)
				if doctorAutofix {
					os.Remove(paths.SocketPath)
					out.Print("    → Removed stale socket\n")
				}
				ok = false
			} else {
				reader := protocol.NewFrameReader(conn)
				writer := protocol.NewFrameWriter(conn)

				hsData, _ := protocol.EncodeControl("handshake", protocol.HandshakeMsg{
					Version:  protocol.Version,
					ClientID: "doctor",
				})
				_ = writer.WriteFrame(protocol.ChannelControl, hsData)

				frame, err := reader.ReadFrame()
				if err == nil && frame.Channel == protocol.ChannelControl {
					env, _ := protocol.DecodeControl(frame.Payload)
					var hsOk protocol.HandshakeOkMsg
					_ = protocol.DecodePayload(env, &hsOk)

					out.Print("  ✓ Daemon is running (version: %s)\n", hsOk.DaemonVersion)

					if hsOk.DaemonVersion != version.Version {
						out.Print("  ✗ Version mismatch: CLI=%s, daemon=%s\n", version.Version, hsOk.DaemonVersion)
						out.Print("    → Run: gr daemon upgrade\n")
						ok = false
					}
				} else {
					out.Print("  ✓ Daemon is running\n")
				}

				conn.Close()
			}
		} else {
			out.Print("  ○ Daemon not running (will auto-start on first command)\n")
		}

		if _, err := os.Stat(paths.ConfigFile); err == nil {
			out.Print("  ✓ Config file: %s\n", paths.ConfigFile)
		} else {
			out.Print("  ○ No config file (using defaults): %s\n", paths.ConfigFile)
		}

		out.Print("  ✓ Data dir: %s\n", paths.DataDir)
		out.Print("  ✓ Runtime dir: %s\n", paths.RuntimeDir)
		out.Print("  ✓ Daemon log: %s\n", paths.DaemonLog)

		updateResult := version.CheckForUpdate(paths.DataDir)
		if updateResult != nil {
			out.Print("  ✗ Update available: %s → %s\n", updateResult.CurrentVersion, updateResult.LatestVersion)
			out.Print("    → Run: brew upgrade graith\n")
			ok = false
		} else if version.Version != "dev" {
			out.Print("  ✓ Up to date (%s)\n", version.Version)
		}

		if cfg.Sandbox.Enabled {
			safehouseCmd := cfg.Sandbox.Command
			if safehouseCmd == "" {
				safehouseCmd = "safehouse"
			}
			if runtime.GOOS != "darwin" {
				out.Print("  ✗ Sandbox enabled but not running macOS (safehouse requires macOS)\n")
				ok = false
			} else if !sandbox.AvailableCommand(safehouseCmd) {
				out.Print("  ✗ Sandbox enabled but %s not found in PATH\n", safehouseCmd)
				out.Print("    → Install: brew install eugene1g/tools/agent-safehouse\n")
				ok = false
			} else {
				out.Print("  ✓ Sandbox enabled (safehouse available)\n")
			}
		} else {
			out.Print("  ○ Sandbox disabled\n")
		}

		if !ok {
			return fmt.Errorf("issues found")
		}

		out.Print("\nAll checks passed.\n")
		return nil
	},
}

func init() {
	rootCmd.AddCommand(doctorCmd)
	doctorCmd.Flags().BoolVar(&doctorAutofix, "autofix", false, "auto-fix issues")
}
