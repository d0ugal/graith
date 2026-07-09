package cli

import (
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/d0ugal/graith/internal/approvals"
	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/daemon"
	"github.com/d0ugal/graith/internal/git"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/d0ugal/graith/internal/sandbox"
	"github.com/d0ugal/graith/internal/version"
	"github.com/spf13/cobra"
)

var (
	doctorAutofix bool
	doctorDisk    bool
)

// nonoInstallHint is the install guidance shown when the nono sandbox backend
// can't enforce. It deliberately avoids the `curl … | sh` piped-shell pattern
// the project moved away from in commit 0fa84fa / #697 — recommending a
// piped remote shell from a security-focused tool would undercut that
// hardening (issue #795). Point at brew and the pinned, attestation-verified
// release download instead.
const nonoInstallHint = "Install: brew install nono  (or download the pinned release from https://github.com/nolabs-ai/nono/releases and verify it with: gh attestation verify <tarball> --repo nolabs-ai/nono)"

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

	// suggestDisk records that a cheap check found an artifact whose disk usage
	// might be worth measuring (orphaned files/worktrees, a legacy dir). When
	// set and --disk was not passed, doctor recommends re-running with --disk.
	suggestDisk bool
}

func newDoctorContext() *doctorContext {
	return &doctorContext{ok: true}
}

func (dc *doctorContext) passf(section, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	dc.checks = append(dc.checks, doctorCheck{Section: section, Level: "ok", Message: msg})
	out.Printf("  ✓ %s\n", msg)
}

func (dc *doctorContext) warnf(section, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	dc.checks = append(dc.checks, doctorCheck{Section: section, Level: "warn", Message: msg})
	out.Printf("  ○ %s\n", msg)
}

func (dc *doctorContext) failf(section, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	dc.checks = append(dc.checks, doctorCheck{Section: section, Level: "fail", Message: msg})
	out.Printf("  ✗ %s\n", msg)

	dc.ok = false
}

func (dc *doctorContext) hintf(format string, args ...any) {
	out.Printf("    → "+format+"\n", args...)
}

func (dc *doctorContext) section(name string) {
	out.Printf("\n%s\n", name)
}

var doctorCmd = &cobra.Command{
	Use:     "doctor",
	Aliases: []string{"doc"},
	Short:   "Health checks and diagnostics",
	RunE: func(cmd *cobra.Command, args []string) error {
		dc := newDoctorContext()
		report := &doctorReport{CLIVersion: version.Version}

		out.Printf("Checking graith health...\n")

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
			out.Printf("\nAll checks passed.\n")
		} else {
			count := 0

			for _, c := range dc.checks {
				if c.Level == "fail" {
					count++
				}
			}

			out.Printf("\n%d issue(s) found.\n", count)
		}

		if dc.suggestDisk && !doctorDisk {
			out.Printf("\nRun 'gr doctor --disk' to measure on-disk sizes of the items above.\n")
		}

		if !dc.ok {
			return fmt.Errorf("issues found")
		}

		return nil
	},
}

func (dc *doctorContext) checkVersion(report *doctorReport) string {
	dc.section("Version")
	dc.passf("version", "CLI version: %s (%s)", version.Version, version.CommitSHA)

	var daemonVersion string

	if _, err := os.Stat(paths.SocketPath); err == nil {
		conn, err := net.DialTimeout("unix", paths.SocketPath, 500*time.Millisecond)
		if err != nil {
			dc.failf("version", "Socket exists but daemon not responding: %s", paths.SocketPath)

			if doctorAutofix {
				_ = os.Remove(paths.SocketPath)

				dc.hintf("Removed stale socket")
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
					dc.failf("version", "Version mismatch: CLI=%s, daemon=%s", version.Version, daemonVersion)
					dc.hintf("Run: gr daemon restart")
				} else if daemonVersion != "" {
					dc.passf("version", "Daemon version: %s", daemonVersion)
				}
			}

			_ = conn.Close()
		}
	}

	updateResult := version.CheckForUpdate(paths.DataDir)
	if updateResult != nil {
		dc.failf("version", "Update available: %s → %s", updateResult.CurrentVersion, updateResult.LatestVersion)
		dc.hintf("Run: brew upgrade graith")
	} else if version.Version != "dev" {
		dc.passf("version", "Up to date (%s)", version.Version)
	}

	return daemonVersion
}

func (dc *doctorContext) checkEnvironment() {
	dc.section("Environment")

	// Resolve the same file the CLI/daemon load — honouring --config and the
	// legacy macOS fallback — so the report and the unknown-key check inspect
	// the config that's actually in effect, not just the default XDG path. The
	// profile/paths errors here can't occur: root has already resolved both
	// (LoadOrDefault) before any command runs.
	configPath, configExists, _ := config.ResolveConfigPath(cfgFile)
	if configExists {
		dc.passf("environment", "Config file: %s", configPath)
		dc.checkConfigKeys(configPath)
	} else {
		dc.warnf("environment", "No config file (using defaults): %s", configPath)
		dc.hintf("Run: gr config reset")
	}

	dc.passf("environment", "Data dir: %s%s", paths.DataDir, dirSizeSuffix(paths.DataDir))

	if info, err := os.Stat(paths.DaemonLog); err == nil {
		size := info.Size()
		if size > 10*1024*1024 {
			dc.warnf("environment", "Daemon log: %s (%s)", paths.DaemonLog, formatBytes(size))

			if doctorAutofix {
				if err := truncateFileKeepTail(paths.DaemonLog, 1024*1024); err == nil {
					dc.hintf("Truncated daemon log to ~1 MB")
				}
			} else {
				dc.hintf("Use --autofix to truncate")
			}
		} else {
			dc.passf("environment", "Daemon log: %s (%s)", paths.DaemonLog, formatBytes(size))
		}
	} else {
		dc.passf("environment", "Daemon log: %s", paths.DaemonLog)
	}

	if info, err := os.Stat(paths.StateFile); err == nil {
		dc.passf("environment", "State file: %s (%s)", paths.StateFile, formatBytes(info.Size()))
	} else {
		dc.passf("environment", "State file: %s", paths.StateFile)
	}

	if info, err := os.Stat(paths.MessagesDB); err == nil {
		dc.passf("environment", "Messages DB: %s (%s)", paths.MessagesDB, formatBytes(info.Size()))
	} else {
		dc.passf("environment", "Messages DB: %s", paths.MessagesDB)
	}

	if paths.Profile != "" {
		dc.passf("environment", "Profile: %s", paths.Profile)
	}

	if cfg.Sandbox.Enabled {
		dc.checkSandboxBackend()
		dc.checkSandboxPaths()
	} else {
		dc.warnf("environment", "Sandbox disabled")
	}

	dc.checkApprovalsBackend()

	switch {
	case cfg.AgentPrompt == "":
		dc.warnf("environment", "Agent prompt is empty (agents will not receive graith context)")
	case cfg.AgentPrompt != config.DefaultAgentPrompt():
		dc.passf("environment", "Agent prompt: customized")
	default:
		dc.passf("environment", "Agent prompt: default")
	}
}

// checkConfigKeys warns about config keys graith doesn't recognise — typos or
// options from a newer graith than this binary. It warns (never fails) because
// the runtime load is intentionally lenient: silently ignoring unknown keys is
// what preserves forward compatibility, so doctor is the place to surface "this
// key isn't doing anything" without breaking the run. See issue #720.
func (dc *doctorContext) checkConfigKeys(configPath string) {
	unknown, err := config.UnknownKeys(configPath)
	if err != nil {
		// A parse/read failure here would already have failed the daemon's own
		// config load; don't double-report it as a doctor finding.
		return
	}

	if len(unknown) == 0 {
		dc.passf("environment", "Config keys: all recognised")
		return
	}

	for _, u := range unknown {
		table := u.Table
		if table == "" {
			table = "top level"
		}

		if u.Suggestion != "" {
			dc.warnf("environment", "Unknown config key [%s] %q — did you mean %q? (ignored)", table, u.Name, u.Suggestion)
		} else {
			dc.warnf("environment", "Unknown config key [%s] %q — ignored (typo? unsupported in this version?)", table, u.Name)
		}
	}
}

// checkSandboxBackend reports whether the configured sandbox backend can
// enforce on this host. Backend must be chosen explicitly — an unset backend
// with the sandbox enabled is a fail (matches the daemon's fail-closed rule).
func (dc *doctorContext) checkSandboxBackend() {
	backend := cfg.Sandbox.Backend
	if backend == "" {
		dc.failf("environment", "Sandbox enabled but no backend selected")
		dc.hintf("Set [sandbox] backend = %q (macOS) or %q (Linux/macOS)", sandbox.BackendSafehouse, sandbox.BackendNono)

		return
	}

	req := sandbox.Requirements{Network: cfg.Sandbox.Network.IsSet()}

	avail, err := sandbox.CheckAvailability(backend, cfg.Sandbox.Command, req)
	if err != nil {
		dc.failf("environment", "Sandbox backend invalid: %v", err)

		return
	}

	switch {
	case !avail.CanEnforce:
		dc.failf("environment", "Sandbox enabled (backend %q) but cannot enforce: %s", backend, avail.Detail)

		switch backend {
		case sandbox.BackendSafehouse:
			dc.hintf("Install: brew install eugene1g/safehouse/agent-safehouse")
		case sandbox.BackendNono:
			dc.hintf("%s", nonoInstallHint)
			dc.hintf("nono requires Linux kernel 5.13+ (Landlock) or macOS; minimum nono version %s", sandbox.MinNonoVersion)
		}
	case avail.Degraded:
		dc.warnf("environment", "Sandbox enabled (backend %q, degraded): %s", backend, avail.Detail)
	default:
		dc.passf("environment", "Sandbox enabled (backend %q available): %s", backend, avail.Detail)
	}

	if cfg.Sandbox.Network.IsSet() {
		switch {
		case cfg.Sandbox.Network.Block:
			dc.passf("environment", "Sandbox network policy: outbound blocked (network.block)")
		case len(cfg.Sandbox.Network.AllowDomains) > 0:
			dc.passf("environment", "Sandbox network policy: proxy allowlist of %d domain(s)", len(cfg.Sandbox.Network.AllowDomains))
		}
	}
}

// checkApprovalsBackend reports whether the configured approvals backend can
// enforce with the current config. This mirrors the daemon's fail-closed
// validateApprovalsBackend check, which rejects an unenforceable backend at
// session-create — a rejection that otherwise surfaces only as a bare "Crashed
// exit 1" session with zero scrollback and a reason buried in daemon.log (see
// issue #738). Surfacing it here means the reason is visible from a channel a
// user can read, including from inside a sandboxed session.
func (dc *doctorContext) checkApprovalsBackend() {
	backend, deprecation, err := cfg.Approvals.ResolveBackend()
	if err != nil {
		dc.failf("environment", "Approvals backend invalid: %v", err)

		return
	}

	if deprecation != "" {
		dc.warnf("environment", "Approvals config deprecated: %s", deprecation)
	}

	// The prompt backend (the default) always defers to the human and needs no
	// external dependency, so it can never fail closed.
	if backend == "" || backend == approvals.BackendPrompt {
		dc.passf("environment", "Approvals backend: prompt (manual)")

		return
	}

	be, err := approvals.BackendByName(backend)
	if err != nil {
		dc.failf("environment", "Approvals backend invalid: %v", err)

		return
	}

	acfg := approvals.Config{
		Backend:       backend,
		Command:       cfg.Approvals.Command,
		BuiltinConfig: config.ExpandPathRelative(cfg.Approvals.Builtin.Config, approvalsConfigDir()),
	}

	// Mirror the daemon's approvalsBackendConfig: render inline
	// [approvals.builtin] rules to localmost JSON so an inline-only config is
	// judged enforceable here exactly as it is at session-create, rather than
	// being reported as a missing external config.
	if cfg.Approvals.Builtin.HasInline() {
		inline, err := cfg.Approvals.Builtin.InlineJSON()
		if err != nil {
			dc.failf("environment", "Approvals inline rules invalid: %v", err)

			return
		}

		acfg.BuiltinInline = inline
	}

	av := be.Availability(acfg)
	if !av.CanEnforce {
		dc.failf("environment", "Approvals backend %q cannot enforce: %s", backend, av.Detail)
		dc.hintf("Sessions will fail to start until this is fixed; set [approvals] backend = \"prompt\" or correct the backend config")

		return
	}

	dc.passf("environment", "Approvals backend %q available", backend)
}

// agentInstalled reports whether an agent's command is resolvable on PATH.
// An empty command (or one not found) means the agent can't launch, so its
// per-agent sandbox dirs are irrelevant.
func agentInstalled(command string) bool {
	if command == "" {
		return false
	}

	_, err := exec.LookPath(command)

	return err == nil
}

func (dc *doctorContext) checkSandboxPaths() {
	allReadDirs := make(map[string][]string)
	allWriteDirs := make(map[string][]string)
	allReadFiles := make(map[string][]string)
	allWriteFiles := make(map[string][]string)

	add := func(m map[string][]string, paths []string, source string) {
		for _, p := range paths {
			exp := config.ExpandPath(p)
			m[exp] = append(m[exp], source)
		}
	}

	add(allReadDirs, cfg.Sandbox.ReadDirs, "global")
	add(allWriteDirs, cfg.Sandbox.WriteDirs, "global")
	add(allReadFiles, cfg.Sandbox.ReadFiles, "global")
	add(allWriteFiles, cfg.Sandbox.WriteFiles, "global")

	for name, agent := range cfg.Agents {
		// Per-agent sandbox dirs only matter when the agent can actually
		// launch. Skip the checks for agents whose command isn't resolvable
		// on PATH — otherwise a single installed agent (e.g. "claude")
		// produces a wall of spurious warnings for the built-in defaults of
		// agents the user will never run. Paths shared with "global" or an
		// installed agent are still checked, since they're added separately.
		if !agentInstalled(agent.Command) {
			continue
		}

		add(allReadDirs, agent.Sandbox.ReadDirs, name)
		add(allWriteDirs, agent.Sandbox.WriteDirs, name)
		add(allReadFiles, agent.Sandbox.ReadFiles, name)
		add(allWriteFiles, agent.Sandbox.WriteFiles, name)
	}

	missing := 0

	check := func(m map[string][]string, kind string) {
		for p, sources := range m {
			if strings.ContainsAny(p, "*?[") {
				continue
			}

			if _, err := os.Stat(p); err != nil {
				dc.warnf("environment", "Sandbox %s does not exist: %s (configured in: %s)", kind, p, strings.Join(sources, ", "))

				missing++
			}
		}
	}

	// write_files grants are deliberately NOT existence-checked at runtime: they
	// are routinely files the agent creates itself (e.g. Claude's
	// ~/.claude.json.lock). Mirror expandFilePaths in daemon.go, which keeps a
	// missing file grant but warns only when its *parent directory* is absent
	// (nono can't create the file without a grantable parent). Warning on the
	// file itself would flag the recommended config as unhealthy (issue #794).
	checkWriteFiles := func(m map[string][]string) {
		for p, sources := range m {
			if strings.ContainsAny(p, "*?[") {
				continue
			}

			parent := filepath.Dir(p)
			if _, err := os.Stat(parent); err != nil {
				dc.warnf("environment", "Sandbox write file parent dir does not exist: %s (for %s, configured in: %s)", parent, p, strings.Join(sources, ", "))

				missing++
			}
		}
	}

	check(allReadDirs, "read dir")
	check(allWriteDirs, "write dir")
	// read_files keeps the stricter file-existence check even though runtime
	// expandFilePaths also retains a missing read grant: a read grant is almost
	// always for a file that must already exist to be useful (e.g. an agent's
	// login file), so a missing one is worth surfacing. write_files are the
	// files the agent creates itself, hence the parent-dir check below.
	check(allReadFiles, "read file")
	checkWriteFiles(allWriteFiles)

	total := len(allReadDirs) + len(allWriteDirs) + len(allReadFiles) + len(allWriteFiles)
	if missing == 0 && total > 0 {
		// "grants usable" rather than "paths exist": a write file whose parent
		// dir exists is healthy even though the file itself may not exist yet.
		dc.passf("environment", "All sandbox grants usable (%d read dir, %d write dir, %d read file, %d write file)",
			len(allReadDirs), len(allWriteDirs), len(allReadFiles), len(allWriteFiles))
	}
}

func (dc *doctorContext) checkDaemon(daemonVersion string) *protocol.DiagnosticsMsg {
	dc.section("Daemon")

	if _, err := os.Stat(paths.SocketPath); err != nil {
		dc.warnf("daemon", "Not running (will auto-start on first command)")
		return nil
	}

	// Use a raw dial with deadline instead of client.Connect to avoid
	// triggering auto-upgrade/restart as a side effect of diagnostics.
	conn, err := net.DialTimeout("unix", paths.SocketPath, 2*time.Second)
	if err != nil {
		dc.failf("daemon", "Cannot connect to daemon: %v", err)
		dc.checkStalePID()

		return nil
	}
	defer func() { _ = conn.Close() }()

	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	reader := protocol.NewFrameReader(conn)
	writer := protocol.NewFrameWriter(conn)

	hs := client.BuildHandshake(paths, 0, 0, "")
	hs.ClientID = "doctor-diag"

	hsData, _ := protocol.EncodeControl("handshake", hs)
	if err := writer.WriteFrame(protocol.ChannelControl, hsData); err != nil {
		dc.failf("daemon", "Failed to send handshake: %v", err)
		return nil
	}

	frame, err := reader.ReadFrame()
	if err != nil || frame.Channel != protocol.ChannelControl {
		dc.failf("daemon", "Failed to read handshake response")
		return nil
	}

	diagData, _ := protocol.EncodeControl("diagnostics", struct{}{})
	if err := writer.WriteFrame(protocol.ChannelControl, diagData); err != nil {
		dc.failf("daemon", "Failed to request diagnostics: %v", err)
		return nil
	}

	frame, err = reader.ReadFrame()
	if err != nil {
		dc.warnf("daemon", "Daemon does not support diagnostics (upgrade daemon)")
		dc.hintf("Run: gr daemon restart")

		return nil
	}

	if frame.Channel != protocol.ChannelControl {
		dc.failf("daemon", "Unexpected response from daemon")
		return nil
	}

	resp, err := protocol.DecodeControl(frame.Payload)
	if err != nil {
		dc.failf("daemon", "Failed to decode response: %v", err)
		return nil
	}

	if resp.Type == "error" {
		dc.warnf("daemon", "Daemon does not support diagnostics (upgrade daemon)")
		dc.hintf("Run: gr daemon restart")

		return nil
	}

	var diag protocol.DiagnosticsMsg
	if err := protocol.DecodePayload(resp, &diag); err != nil {
		dc.failf("daemon", "Failed to decode diagnostics: %v", err)
		return nil
	}

	dc.passf("daemon", "Running (PID %d, uptime %s)", diag.DaemonPID, diag.DaemonUptime)

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

	if !daemon.IsGraithDaemon(pid) {
		dc.failf("daemon", "PID file is stale (PID %d is not a graith daemon)", pid)

		if doctorAutofix {
			_ = os.Remove(paths.PIDFile)

			dc.hintf("Removed stale PID file")
		} else {
			dc.hintf("Use --autofix to remove stale PID file")
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
			dc.failf("sessions", "%q (%s): PID %d not alive but status is running", s.Name, s.ID, s.PID)
			dc.hintf("Run: gr daemon restart")

			issues++
		}

		if s.Status == "running" && s.PID > 0 && s.PIDAlive && s.HasPTY != nil && !*s.HasPTY {
			dc.failf("sessions", "%q (%s): PID %d alive but not managed by daemon (orphaned after crash)", s.Name, s.ID, s.PID)
			dc.hintf("Run: gr stop %s  (kills orphaned process group)", s.Name)

			issues++
		}

		if s.Status == "errored" && s.PID > 0 {
			dc.warnf("sessions", "%q (%s): errored with PID %d still recorded — may need manual cleanup", s.Name, s.ID, s.PID)
			dc.hintf("Run: kill -TERM -%d  (kills process group)", s.PID)

			issues++
		}

		if s.WorktreePath != "" && !s.WorktreeExists {
			dc.failf("sessions", "%q (%s): worktree path does not exist", s.Name, s.ID)
			dc.hintf("Run: gr delete %s", s.Name)

			issues++
		}

		if s.ConfigStale {
			dc.warnf("sessions", "%q (%s): config has drifted since creation", s.Name, s.ID)
			dc.hintf("Restart session to pick up new config")

			issues++
		}

		if s.Saturated {
			dc.warnf("sessions", "%q (%s): scrollback saturated (%s)", s.Name, s.ID, formatBytes(s.ScrollbackMax))

			issues++
		}
	}

	for _, s := range diag.Sessions {
		if !s.HasToken {
			dc.warnf("sessions", "%q (%s): missing auth token — session may need restart to receive token", s.Name, s.ID)
			dc.hintf("Run: gr restart %s", s.Name)

			issues++
		}
	}

	if !cfg.Sandbox.Enabled {
		running := 0

		for _, s := range diag.Sessions {
			if s.Status == "running" {
				running++
			}
		}

		if running > 1 {
			dc.warnf("sessions", "Sandbox is disabled with %d running sessions — agents can read state.json and impersonate other sessions", running)
			dc.hintf("Enable sandbox for session isolation: set sandbox.enabled = true in config")

			issues++
		}
	}

	if issues == 0 {
		dc.passf("sessions", "No issues found")
	}
}

func (dc *doctorContext) checkStorage(diag *protocol.DiagnosticsMsg) {
	dc.section("Storage")

	sb := diag.Scrollback
	if sb.SaturatedCount > 0 {
		dc.warnf("storage", "Scrollback: %d files, %s total (%d saturated)", sb.TotalFiles, formatBytes(sb.TotalBytes), sb.SaturatedCount)
	} else {
		dc.passf("storage", "Scrollback: %d files, %s total", sb.TotalFiles, formatBytes(sb.TotalBytes))
	}

	msg := diag.Messages
	dc.passf("storage", "Messages: %d streams, %d messages", msg.TotalStreams, msg.TotalMessages)

	dc.checkTmpDir()

	// Check for orphaned scrollback files
	sessionIDs := make(map[string]bool, len(diag.Sessions))
	for _, s := range diag.Sessions {
		sessionIDs[s.ID] = true
	}

	var (
		orphanedCount int
		orphanedBytes int64
	)

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
		dc.warnf("storage", "%d orphaned scrollback file(s) (%s)", orphanedCount, formatBytes(orphanedBytes))

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

			dc.hintf("Removed %d orphaned scrollback file(s)", removed)
		} else {
			dc.hintf("Use --autofix to remove")
		}
	}

	// Check for orphaned worktree directories
	orphanedWorktrees := dc.findOrphanedWorktrees(sessionIDs)
	if len(orphanedWorktrees) > 0 {
		var totalSize int64

		dirtyCount := 0

		for _, wt := range orphanedWorktrees {
			totalSize += wt.size
			if wt.hasDirtyFiles {
				dirtyCount++
			}
		}

		if doctorDisk {
			dc.warnf("storage", "%d orphaned worktree dir(s) (%s)", len(orphanedWorktrees), formatBytes(totalSize))
		} else {
			dc.warnf("storage", "%d orphaned worktree dir(s)", len(orphanedWorktrees))
			dc.suggestDisk = true
		}

		for _, wt := range orphanedWorktrees {
			extra := ""
			if wt.hasDirtyFiles {
				extra = " [has uncommitted changes]"
			}

			if doctorDisk {
				dc.hintf("%s (%s)%s", wt.path, formatBytes(wt.size), extra)
			} else {
				dc.hintf("%s%s", wt.path, extra)
			}
		}

		if doctorAutofix {
			removed := 0
			skipped := 0

			for _, wt := range orphanedWorktrees {
				if wt.hasDirtyFiles {
					skipped++
					continue
				}

				var repoRoot string
				if wt.isGitWorktree {
					repoRoot, _ = git.RepoRootFromWorktree(wt.path)
				}

				if os.RemoveAll(wt.path) == nil {
					removed++

					if repoRoot != "" {
						_ = git.PruneWorktrees(repoRoot)
					}
				}
			}

			dc.hintf("Removed %d orphaned worktree dir(s)", removed)

			if skipped > 0 {
				dc.hintf("Skipped %d with uncommitted changes (inspect manually)", skipped)
			}
		} else {
			if dirtyCount > 0 {
				dc.hintf("Use --autofix to remove (%d with uncommitted changes will be skipped)", dirtyCount)
			} else {
				dc.hintf("Use --autofix to remove")
			}
		}
	}
}

func (dc *doctorContext) checkTmpDir() {
	tmpDir := paths.TmpDir

	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		dc.passf("storage", "Tmp dir: %s (empty)", tmpDir)
		return
	}

	var (
		totalSize int64
		repoCount int
	)

	for _, repo := range entries {
		if !repo.IsDir() {
			continue
		}

		repoDir := filepath.Join(tmpDir, repo.Name())

		hashes, err := os.ReadDir(repoDir)
		if err != nil {
			continue
		}

		for _, hash := range hashes {
			if !hash.IsDir() {
				continue
			}

			repoCount++

			// The per-repo size walk is the expensive part; the repo count is a
			// cheap ReadDir. Only sum sizes when --disk asked for them.
			if doctorDisk {
				size, _ := dirSize(filepath.Join(repoDir, hash.Name()))
				totalSize += size
			}
		}
	}

	switch {
	case repoCount == 0:
		dc.passf("storage", "Tmp dir: %s (empty)", tmpDir)
	case doctorDisk:
		dc.passf("storage", "Tmp dir: %s (%d repo(s), %s)", tmpDir, repoCount, formatBytes(totalSize))
	default:
		dc.passf("storage", "Tmp dir: %s (%d repo(s))", tmpDir, repoCount)
		dc.suggestDisk = true
	}

	legacyShareDir := filepath.Join(filepath.Dir(tmpDir), "share")
	if info, err := os.Stat(legacyShareDir); err == nil && info.IsDir() {
		dc.warnf("storage", "Legacy share dir exists: %s%s", legacyShareDir, dirSizeSuffix(legacyShareDir))
		dc.suggestDisk = true

		if doctorAutofix {
			if os.RemoveAll(legacyShareDir) == nil {
				dc.hintf("Removed legacy share dir")
			}
		} else {
			dc.hintf("Renamed to tmp/ in v0.39.0. Use --autofix to remove")
		}
	}
}

type orphanedWorktree struct {
	path          string
	size          int64
	isGitWorktree bool
	hasDirtyFiles bool
}

const orphanMinAge = 5 * time.Minute

func (dc *doctorContext) findOrphanedWorktrees(sessionIDs map[string]bool) []orphanedWorktree {
	var orphaned []orphanedWorktree

	now := time.Now()

	// Worktrees live at <DataDir>/worktrees/<repoName>/<repoHash>/<sessionID>
	worktreesDir := filepath.Join(paths.DataDir, "worktrees")

	repos, err := os.ReadDir(worktreesDir)
	if err == nil {
		orphaned = append(orphaned, findOrphanedInWorktrees(repos, worktreesDir, sessionIDs, now)...)
	}

	// Scratch dirs live at <DataDir>/scratch/<sessionID>
	scratchDir := filepath.Join(paths.DataDir, "scratch")

	scratches, err := os.ReadDir(scratchDir)
	if err == nil {
		for _, s := range scratches {
			if !s.IsDir() {
				continue
			}

			if sessionIDs[s.Name()] {
				continue
			}

			sDir := filepath.Join(scratchDir, s.Name())
			if info, err := s.Info(); err == nil && now.Sub(info.ModTime()) < orphanMinAge {
				continue
			}

			orphaned = append(orphaned, orphanedWorktree{path: sDir, size: orphanDirSize(sDir)})
		}
	}

	return orphaned
}

func findOrphanedInWorktrees(repos []os.DirEntry, worktreesDir string, sessionIDs map[string]bool, now time.Time) []orphanedWorktree {
	var orphaned []orphanedWorktree

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

				if sessionIDs[sess.Name()] {
					continue
				}

				sessDir := filepath.Join(hashDir, sess.Name())
				if info, err := sess.Info(); err == nil && now.Sub(info.ModTime()) < orphanMinAge {
					continue
				}

				wt := orphanedWorktree{path: sessDir, size: orphanDirSize(sessDir)}

				if git.IsInsideGitRepo(sessDir) {
					wt.isGitWorktree = true
					if dirty, err := git.HasUncommittedChanges(sessDir); err == nil && dirty {
						wt.hasDirtyFiles = true
					}
				}

				orphaned = append(orphaned, wt)
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

// dirSizeSuffix returns a " (<size>)" suffix describing a directory's on-disk
// size, or "" when disk scanning is disabled. Computing a directory size means
// walking the whole tree, which is fast for small dirs but can take tens of
// seconds on a large data dir (worktrees full of node_modules and .git
// objects). It's purely informational, so it's gated behind --disk to keep the
// default `gr doctor` snappy.
func dirSizeSuffix(path string) string {
	if !doctorDisk {
		return ""
	}

	size, err := dirSize(path)
	if err != nil {
		return ""
	}

	return " (" + formatBytes(size) + ")"
}

// orphanDirSize returns the on-disk size of an orphaned dir, or 0 when --disk
// was not passed. Sizing walks the whole subtree, so it's skipped by default;
// orphan detection itself (a cheap ReadDir) always runs.
func orphanDirSize(path string) int64 {
	if !doctorDisk {
		return 0
	}

	size, _ := dirSize(path)

	return size
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

	return os.WriteFile(path, tail, 0o600) //nolint:gosec // G703: path is graith's own trusted DaemonLog path, not user-controlled
}

// registerDoctorCmd registers this command on rootCmd. Called from registerCommands.
func registerDoctorCmd() {
	rootCmd.AddCommand(doctorCmd)
	doctorCmd.Flags().BoolVar(&doctorAutofix, "autofix", false, "auto-fix issues")
	doctorCmd.Flags().BoolVar(&doctorDisk, "disk", false, "measure on-disk sizes (walks the data dir; can be slow on large installs)")
}
