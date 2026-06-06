package cli

import (
	"fmt"
	"net"
	"os"
	"time"

	"github.com/spf13/cobra"
)

var doctorAutofix bool

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Health checks and diagnostics",
	RunE: func(cmd *cobra.Command, args []string) error {
		ok := true

		out.Print("Checking graith health...\n\n")

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
				conn.Close()
				out.Print("  ✓ Daemon is running\n")
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
