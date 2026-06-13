package cli

import (
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/d0ugal/graith/internal/sandbox"
	"github.com/d0ugal/graith/internal/version"
	"github.com/spf13/cobra"
)

var doctorAutofix bool

type doctorCheck struct {
	Section string `json:"section"`
	Level   string `json:"level"`
	Message string `json:"message"`
}

type doctorReport struct {
	CLIVersion    string                   `json:"cli_version"`
	DaemonVersion string                   `json:"daemon_version,omitempty"`
	OK            bool                     `json:"ok"`
	Checks        []doctorCheck            `json:"checks"`
	Diagnostics   *protocol.DiagnosticsMsg `json:"diagnostics,omitempty"`
}

type doctorContext struct {
	checks []doctorCheck
	ok     bool
}

func newDoctorContext() *doctorContext {
	return &doctorContext{ok: true}
}

func (dc *doctorContext) pass(section, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	dc.checks = append(dc.checks, doctorCheck{Section: section, Level: "ok", Message: msg})
	out.Print("  ✓ %s\n", msg)
}

func (dc *doctorContext) warn(section, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	dc.checks = append(dc.checks, doctorCheck{Section: section, Level: "warn", Message: msg})
	out.Print("  ○ %s\n", msg)
}

func (dc *doctorContext) fail(section, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	dc.checks = append(dc.checks, doctorCheck{Section: section, Level: "fail", Message: msg})
	out.Print("  ✗ %s\n", msg)
	dc.ok = false
}

func (dc *doctorContext) hint(format string, args ...any) {
	out.Print("    → "+format+"\n", args...)
}

func (dc *doctorContext) section(name string) {
	out.Print("\n%s\n", name)
}

var doctorCmd = &cobra.Command{
	Use:     "doctor",
	Aliases: []string{"doc"},
	Short:   "Health checks and diagnostics",
	RunE: func(cmd *cobra.Command, args []string) error {
		dc := newDoctorContext()
		report := &doctorReport{CLIVersion: version.Version}

		out.Print("Checking graith health...\n")

		daemonVersion := dc.checkVersion(report)
		dc.checkEnvironment()
		diag := dc.checkDaemon(daemonVersion)
		if diag != nil {
			report.Diagnostics = diag
			report.DaemonVersion = daemonVersion
			dc.checkSessions(diag)
			dc.checkStorage(diag)
		}

		report.OK = dc.ok
		report.Checks = dc.checks

		if jsonOutput {
			return out.JSON(report)
		}

		if dc.ok {
			out.Print("\nAll checks passed.\n")
		} else {
			count := 0
			for _, c := range dc.checks {
				if c.Level == "fail" {
					count++
				}
			}
			out.Print("\n%d issue(s) found.\n", count)
		}

		if !dc.ok {
			return fmt.Errorf("issues found")
		}
		return nil
	},
}

func (dc *doctorContext) checkVersion(report *doctorReport) string {
	dc.section("Version")
	dc.pass("version", "CLI version: %s (%s)", version.Version, version.CommitSHA)

	var daemonVersion string

	if _, err := os.Stat(paths.SocketPath); err == nil {
		conn, err := net.DialTimeout("unix", paths.SocketPath, 500*time.Millisecond)
		if err != nil {
			dc.fail("version", "Socket exists but daemon not responding: %s", paths.SocketPath)
			if doctorAutofix {
				os.Remove(paths.SocketPath)
				dc.hint("Removed stale socket")
			}
		} else {
			reader := protocol.NewFrameReader(conn)
			writer := protocol.NewFrameWriter(conn)

			hs := client.BuildHandshake(paths, 0, 0, "")
			hs.ClientID = "doctor"
			hsData, _ := protocol.EncodeControl("handshake", hs)
			_ = writer.WriteFrame(protocol.ChannelControl, hsData)

			frame, err := reader.ReadFrame()
			if err == nil && frame.Channel == protocol.ChannelControl {
				env, _ := protocol.DecodeControl(frame.Payload)
				var hsOk protocol.HandshakeOkMsg
				_ = protocol.DecodePayload(env, &hsOk)
				daemonVersion = hsOk.DaemonVersion

				if daemonVersion != "" && daemonVersion != version.Version {
					dc.fail("version", "Version mismatch: CLI=%s, daemon=%s", version.Version, daemonVersion)
					dc.hint("Run: gr daemon restart")
				} else if daemonVersion != "" {
					dc.pass("version", "Daemon version: %s", daemonVersion)
				}
			}
			conn.Close()
		}
	}

	updateResult := version.CheckForUpdate(paths.DataDir)
	if updateResult != nil {
		dc.fail("version", "Update available: %s → %s", updateResult.CurrentVersion, updateResult.LatestVersion)
		dc.hint("Run: brew upgrade graith")
	} else if version.Version != "dev" {
		dc.pass("version", "Up to date (%s)", version.Version)
	}

	return daemonVersion
}

func (dc *doctorContext) checkEnvironment() {
	dc.section("Environment")

	if _, err := os.Stat(paths.ConfigFile); err == nil {
		dc.pass("environment", "Config file: %s", paths.ConfigFile)
	} else {
		dc.warn("environment", "No config file (using defaults): %s", paths.ConfigFile)
		dc.hint("Run: gr config reset")
	}

	if dataDirSize, err := dirSize(paths.DataDir); err == nil {
		dc.pass("environment", "Data dir: %s (%s)", paths.DataDir, formatBytes(dataDirSize))
	} else {
		dc.pass("environment", "Data dir: %s", paths.DataDir)
	}

	if info, err := os.Stat(paths.DaemonLog); err == nil {
		size := info.Size()
		if size > 10*1024*1024 {
			dc.warn("environment", "Daemon log: %s (%s)", paths.DaemonLog, formatBytes(size))
			if doctorAutofix {
				if err := truncateFileKeepTail(paths.DaemonLog, 1024*1024); err == nil {
					dc.hint("Truncated daemon log to ~1 MB")
				}
			} else {
				dc.hint("Use --autofix to truncate")
			}
		} else {
			dc.pass("environment", "Daemon log: %s (%s)", paths.DaemonLog, formatBytes(size))
		}
	} else {
		dc.pass("environment", "Daemon log: %s", paths.DaemonLog)
	}

	if info, err := os.Stat(paths.StateFile); err == nil {
		dc.pass("environment", "State file: %s (%s)", paths.StateFile, formatBytes(info.Size()))
	} else {
		dc.pass("environment", "State file: %s", paths.StateFile)
	}

	if info, err := os.Stat(paths.MessagesDB); err == nil {
		dc.pass("environment", "Messages DB: %s (%s)", paths.MessagesDB, formatBytes(info.Size()))
	} else {
		dc.pass("environment", "Messages DB: %s", paths.MessagesDB)
	}

	if paths.Profile != "" {
		dc.pass("environment", "Profile: %s", paths.Profile)
	}

	if cfg.Sandbox.Enabled {
		safehouseCmd := cfg.Sandbox.Command
		if safehouseCmd == "" {
			safehouseCmd = "safehouse"
		}
		switch {
		case runtime.GOOS != "darwin":
			dc.fail("environment", "Sandbox enabled but not running macOS")
		case !sandbox.AvailableCommand(safehouseCmd):
			dc.fail("environment", "Sandbox enabled but %s not found in PATH", safehouseCmd)
			dc.hint("Install: brew install eugene1g/tools/agent-safehouse")
		default:
			dc.pass("environment", "Sandbox enabled (safehouse available)")
		}
	} else {
		dc.warn("environment", "Sandbox disabled")
	}
}

func (dc *doctorContext) checkDaemon(daemonVersion string) *protocol.DiagnosticsMsg {
	dc.section("Daemon")

	if _, err := os.Stat(paths.SocketPath); err != nil {
		dc.warn("daemon", "Not running (will auto-start on first command)")
		return nil
	}

	// Use a raw dial with deadline instead of client.Connect to avoid
	// triggering auto-upgrade/restart as a side effect of diagnostics.
	conn, err := net.DialTimeout("unix", paths.SocketPath, 2*time.Second)
	if err != nil {
		dc.fail("daemon", "Cannot connect to daemon: %v", err)
		dc.checkStalePID()
		return nil
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))

	reader := protocol.NewFrameReader(conn)
	writer := protocol.NewFrameWriter(conn)

	hs := client.BuildHandshake(paths, 0, 0, "")
	hs.ClientID = "doctor-diag"
	hsData, _ := protocol.EncodeControl("handshake", hs)
	if err := writer.WriteFrame(protocol.ChannelControl, hsData); err != nil {
		dc.fail("daemon", "Failed to send handshake: %v", err)
		return nil
	}

	frame, err := reader.ReadFrame()
	if err != nil || frame.Channel != protocol.ChannelControl {
		dc.fail("daemon", "Failed to read handshake response")
		return nil
	}

	diagData, _ := protocol.EncodeControl("diagnostics", struct{}{})
	if err := writer.WriteFrame(protocol.ChannelControl, diagData); err != nil {
		dc.fail("daemon", "Failed to request diagnostics: %v", err)
		return nil
	}

	frame, err = reader.ReadFrame()
	if err != nil {
		dc.warn("daemon", "Daemon does not support diagnostics (upgrade daemon)")
		dc.hint("Run: gr daemon restart")
		return nil
	}
	if frame.Channel != protocol.ChannelControl {
		dc.fail("daemon", "Unexpected response from daemon")
		return nil
	}

	resp, err := protocol.DecodeControl(frame.Payload)
	if err != nil {
		dc.fail("daemon", "Failed to decode response: %v", err)
		return nil
	}

	if resp.Type == "error" {
		dc.warn("daemon", "Daemon does not support diagnostics (upgrade daemon)")
		dc.hint("Run: gr daemon restart")
		return nil
	}

	var diag protocol.DiagnosticsMsg
	if err := protocol.DecodePayload(resp, &diag); err != nil {
		dc.fail("daemon", "Failed to decode diagnostics: %v", err)
		return nil
	}

	dc.pass("daemon", "Running (PID %d, uptime %s)", diag.DaemonPID, diag.DaemonUptime)
	return &diag
}

func (dc *doctorContext) checkStalePID() {
	pidData, err := os.ReadFile(paths.PIDFile)
	if err != nil {
		return
	}
	var pid int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(pidData)), "%d", &pid); err != nil {
		return
	}
	if pid <= 0 || syscall.Kill(pid, 0) != nil {
		dc.fail("daemon", "PID file points to dead process (PID %d)", pid)
		if doctorAutofix {
			os.Remove(paths.PIDFile)
			dc.hint("Removed stale PID file")
		} else {
			dc.hint("Use --autofix to remove stale PID file")
		}
	}
}

func (dc *doctorContext) checkSessions(diag *protocol.DiagnosticsMsg) {
	f := diag.Fleet
	parts := []string{}
	if f.Active > 0 {
		parts = append(parts, fmt.Sprintf("%d active", f.Active))
	}
	if f.Approval > 0 {
		parts = append(parts, fmt.Sprintf("%d approval", f.Approval))
	}
	if f.Ready > 0 {
		parts = append(parts, fmt.Sprintf("%d ready", f.Ready))
	}
	if f.Errored > 0 {
		parts = append(parts, fmt.Sprintf("%d errored", f.Errored))
	}
	if f.Stopped > 0 {
		parts = append(parts, fmt.Sprintf("%d stopped", f.Stopped))
	}

	summary := ""
	if len(parts) > 0 {
		summary = ": " + strings.Join(parts, ", ")
	}
	dc.section(fmt.Sprintf("Sessions (%d total%s)", f.Total, summary))

	issues := 0
	for _, s := range diag.Sessions {
		if s.Status == "running" && s.PID > 0 && !s.PIDAlive {
			dc.fail("sessions", "%q (%s): PID %d not alive but status is running", s.Name, s.ID, s.PID)
			dc.hint("Run: gr daemon restart")
			issues++
		}
		if s.WorktreePath != "" && !s.WorktreeExists {
			dc.fail("sessions", "%q (%s): worktree path does not exist", s.Name, s.ID)
			dc.hint("Run: gr delete %s", s.Name)
			issues++
		}
		if s.ConfigStale {
			dc.warn("sessions", "%q (%s): config has drifted since creation", s.Name, s.ID)
			dc.hint("Restart session to pick up new config")
			issues++
		}
		if s.Saturated {
			dc.warn("sessions", "%q (%s): scrollback saturated (%s)", s.Name, s.ID, formatBytes(s.ScrollbackMax))
			issues++
		}
	}

	if issues == 0 {
		dc.pass("sessions", "No issues found")
	}
}

func (dc *doctorContext) checkStorage(diag *protocol.DiagnosticsMsg) {
	dc.section("Storage")

	sb := diag.Scrollback
	if sb.SaturatedCount > 0 {
		dc.warn("storage", "Scrollback: %d files, %s total (%d saturated)", sb.TotalFiles, formatBytes(sb.TotalBytes), sb.SaturatedCount)
	} else {
		dc.pass("storage", "Scrollback: %d files, %s total", sb.TotalFiles, formatBytes(sb.TotalBytes))
	}

	msg := diag.Messages
	dc.pass("storage", "Messages: %d streams, %d messages", msg.TotalStreams, msg.TotalMessages)

	// Check for orphaned scrollback files
	sessionIDs := make(map[string]bool, len(diag.Sessions))
	for _, s := range diag.Sessions {
		sessionIDs[s.ID] = true
	}

	var orphanedCount int
	var orphanedBytes int64
	entries, err := os.ReadDir(paths.LogDir)
	if err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".log") {
				continue
			}
			id := strings.TrimSuffix(e.Name(), ".log")
			if !sessionIDs[id] {
				orphanedCount++
				if info, err := e.Info(); err == nil {
					orphanedBytes += info.Size()
				}
			}
		}
	}

	if orphanedCount > 0 {
		dc.warn("storage", "%d orphaned scrollback file(s) (%s)", orphanedCount, formatBytes(orphanedBytes))
		if doctorAutofix {
			removed := 0
			for _, e := range entries {
				if e.IsDir() || !strings.HasSuffix(e.Name(), ".log") {
					continue
				}
				id := strings.TrimSuffix(e.Name(), ".log")
				if !sessionIDs[id] {
					if os.Remove(filepath.Join(paths.LogDir, e.Name())) == nil {
						removed++
					}
				}
			}
			dc.hint("Removed %d orphaned scrollback file(s)", removed)
		} else {
			dc.hint("Use --autofix to remove")
		}
	}

	// Check for orphaned worktree directories
	orphanedWorktrees := dc.findOrphanedWorktrees(sessionIDs)
	if len(orphanedWorktrees) > 0 {
		var totalSize int64
		for _, wt := range orphanedWorktrees {
			totalSize += wt.size
		}
		dc.warn("storage", "%d orphaned worktree dir(s) (%s)", len(orphanedWorktrees), formatBytes(totalSize))
		for _, wt := range orphanedWorktrees {
			dc.hint("%s (%s)", wt.path, formatBytes(wt.size))
		}
		if doctorAutofix {
			removed := 0
			for _, wt := range orphanedWorktrees {
				if os.RemoveAll(wt.path) == nil {
					removed++
				}
			}
			dc.hint("Removed %d orphaned worktree dir(s)", removed)
		} else {
			dc.hint("Use --autofix to remove")
		}
	}
}

type orphanedWorktree struct {
	path string
	size int64
}

func (dc *doctorContext) findOrphanedWorktrees(sessionIDs map[string]bool) []orphanedWorktree {
	var orphaned []orphanedWorktree

	// Worktrees live at <DataDir>/worktrees/<repoName>/<repoHash>/<sessionID>
	worktreesDir := filepath.Join(paths.DataDir, "worktrees")
	repos, err := os.ReadDir(worktreesDir)
	if err != nil {
		return nil
	}
	for _, repo := range repos {
		if !repo.IsDir() {
			continue
		}
		repoDir := filepath.Join(worktreesDir, repo.Name())
		hashes, err := os.ReadDir(repoDir)
		if err != nil {
			continue
		}
		for _, hash := range hashes {
			if !hash.IsDir() {
				continue
			}
			hashDir := filepath.Join(repoDir, hash.Name())
			sessions, err := os.ReadDir(hashDir)
			if err != nil {
				continue
			}
			for _, sess := range sessions {
				if !sess.IsDir() {
					continue
				}
				if !sessionIDs[sess.Name()] {
					sessDir := filepath.Join(hashDir, sess.Name())
					size, _ := dirSize(sessDir)
					orphaned = append(orphaned, orphanedWorktree{path: sessDir, size: size})
				}
			}
		}
	}

	// Scratch dirs live at <DataDir>/scratch/<sessionID>
	scratchDir := filepath.Join(paths.DataDir, "scratch")
	scratches, err := os.ReadDir(scratchDir)
	if err == nil {
		for _, s := range scratches {
			if !s.IsDir() {
				continue
			}
			if !sessionIDs[s.Name()] {
				sDir := filepath.Join(scratchDir, s.Name())
				size, _ := dirSize(sDir)
				orphaned = append(orphaned, orphanedWorktree{path: sDir, size: size})
			}
		}
	}

	return orphaned
}

func formatBytes(b int64) string {
	switch {
	case b >= 1024*1024*1024:
		return fmt.Sprintf("%.1f GB", float64(b)/(1024*1024*1024))
	case b >= 1024*1024:
		return fmt.Sprintf("%.1f MB", float64(b)/(1024*1024))
	case b >= 1024:
		return fmt.Sprintf("%.1f KB", float64(b)/1024)
	default:
		return fmt.Sprintf("%d B", b)
	}
}

func dirSize(path string) (int64, error) {
	var total int64
	err := filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total, err
}

func truncateFileKeepTail(path string, keepBytes int64) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if int64(len(data)) <= keepBytes {
		return nil
	}
	tail := data[int64(len(data))-keepBytes:]
	return os.WriteFile(path, tail, 0o600)
}

func init() {
	rootCmd.AddCommand(doctorCmd)
	doctorCmd.Flags().BoolVar(&doctorAutofix, "autofix", false, "auto-fix issues")
}
