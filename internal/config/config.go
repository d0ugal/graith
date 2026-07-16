package config

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/adrg/xdg"
	"github.com/d0ugal/graith/internal/approvals/localmost"
	"github.com/d0ugal/graith/internal/tools"
	"github.com/pelletier/go-toml/v2"
)

//go:embed default_config.toml
var defaultConfigTOML []byte

type Config struct {
	DefaultAgent     string             `toml:"default_agent"`
	GitHubUsername   string             `toml:"github_username"`
	BranchPrefix     string             `toml:"branch_prefix"`
	DataDir          string             `toml:"data_dir"`
	FetchOnCreate    bool               `toml:"fetch_on_create"`
	AgentPrompt      string             `toml:"agent_prompt"`
	AllowedRepoPaths []string           `toml:"allowed_repo_paths"`
	Repos            []RepoConfig       `toml:"repos"`
	StatusBar        StatusBar          `toml:"status_bar"`
	Keybindings      Keybindings        `toml:"keybindings"`
	Notifications    Notifications      `toml:"notifications"`
	Messages         Messages           `toml:"messages"`
	Delete           Delete             `toml:"delete"`
	GC               GCConfig           `toml:"gc"`
	Todo             TodoConfig         `toml:"todo"`
	Sandbox          SandboxConfig      `toml:"sandbox"`
	Approvals        Approvals          `toml:"approvals"`
	Status           StatusConfig       `toml:"status"`
	GitPull          GitPullConfig      `toml:"git_pull"`
	Launch           LaunchConfig       `toml:"launch"`
	PRWatch          PRWatchConfig      `toml:"pr_watch"`
	MCPServers       []MCPServerConfig  `toml:"mcp_servers"`
	Overlay          Overlay            `toml:"overlay"`
	Orchestrator     OrchestratorConfig `toml:"orchestrator"`
	Remote           RemoteConfig       `toml:"remote"`
	Input            InputConfig        `toml:"input"`
	Agents           map[string]Agent   `toml:"agents"`
	Triggers         []TriggerConfig    `toml:"trigger"`          // [[trigger]] array
	TriggersRuntime  TriggersRuntime    `toml:"triggers"`         // [triggers] table (daemon-wide settings)
	Headless         HeadlessConfig     `toml:"headless"`         // [headless] table (issue #1075)
	Updates          UpdatesConfig      `toml:"updates"`          // [updates] table (issue #1253)
	Detection        DetectionConfig    `toml:"detection"`        // [detection] table (issue #1241)
	ConfigReload     ConfigReload       `toml:"config"`           // [config] table (issue #1237)
	Tools            ToolsConfig        `toml:"tools"`            // [tools] table (issue #1238)
	Git              GitConfig          `toml:"git"`              // [git] table (issue #1238)
	Connection       ConnectionConfig   `toml:"connection"`       // [connection] table (issue #1242)
	TokenAccounting  TokenAccounting    `toml:"token_accounting"` // [token_accounting] table (issue #1244)
	ResourceMonitor  ResourceMonitor    `toml:"resource_monitor"` // [resource_monitor] table (issue #1244)
	Migration        MigrationConfig    `toml:"migration"`        // [migration] table (issue #1250)
	Transcript       TranscriptConfig   `toml:"transcript"`       // [transcript] table (issue #1250)
	Limits           LimitsConfig       `toml:"limits"`           // [limits] table (issue #1252)
	Lifecycle        LifecycleConfig    `toml:"lifecycle"`        // [lifecycle] table (issue #1243)
	Terminal         TerminalConfig     `toml:"terminal"`         // [terminal] table (issue #1254)

	// Warnings collects non-fatal configuration problems detected at load time
	// (e.g. conflicting keybindings). They are surfaced to the user but do not
	// prevent startup. Not serialised. See issue #1233.
	Warnings []string `toml:"-"`
}

// ConfigReloadDebounceDefault is the quiet period the config-file watcher waits
// after the last write before reloading, used when [config] reload_debounce is
// unset. It coalesces an editor's write-truncate-write burst into one reload.
const ConfigReloadDebounceDefault = 200 * time.Millisecond

// ConfigReload is the [config] block: settings governing how graith handles its
// own configuration file. Currently just the hot-reload debounce, moved out of a
// bare literal in the watcher so `gr config show` reflects it (issue #1237).
type ConfigReload struct {
	// ReloadDebounce is the quiet period the file watcher waits after the last
	// write before reloading. Empty, unparseable, or non-positive uses the
	// default (ConfigReloadDebounceDefault).
	ReloadDebounce string `toml:"reload_debounce"`
}

// ReloadDebounceDuration resolves the config-reload debounce. Empty, unparseable,
// or non-positive falls back to the default so a typo never busy-loops the
// watcher (Validate rejects a set-but-invalid value at load; this is the runtime
// fail-safe).
func (c ConfigReload) ReloadDebounceDuration() time.Duration {
	if strings.TrimSpace(c.ReloadDebounce) == "" {
		return ConfigReloadDebounceDefault
	}

	d, err := ParseDurationWithDays(c.ReloadDebounce)
	if err != nil || d <= 0 {
		return ConfigReloadDebounceDefault
	}

	return d
}

// UpdatesConfig is the [updates] block controlling the GitHub release check
// (issue #1253). It makes the previously hard-coded checker configurable for
// downstream forks, packaged/offline deployments, and users who don't want
// network update checks. Enabled defaults to true (set in the embedded default
// config) to preserve the historical opt-out behaviour; the remaining fields
// fall back to the version package defaults when empty.
type UpdatesConfig struct {
	// Enabled turns the update check on. Defaults to true via the embedded
	// default config; set false to disable all update-check network I/O.
	Enabled bool `toml:"enabled"`
	// Repository is the "owner/repo" whose latest release is queried. Empty uses
	// the canonical d0ugal/graith repository.
	Repository string `toml:"repository"`
	// Interval is how often the check refreshes (cached between checks). Empty
	// uses the 1h default; must parse as a duration.
	Interval string `toml:"interval"`
	// Timeout bounds the release HTTP request. Empty uses the 5s default; must
	// parse as a duration.
	Timeout string `toml:"timeout"`
}

// IntervalDuration returns the configured cache cadence, or 0 when unset/invalid
// so the version package applies its own default.
func (u UpdatesConfig) IntervalDuration() time.Duration {
	if u.Interval == "" {
		return 0
	}

	d, err := ParseDurationWithDays(u.Interval)
	if err != nil || d <= 0 {
		return 0
	}

	return d
}

// TimeoutDuration returns the configured HTTP timeout, or 0 when unset/invalid
// so the version package applies its own default.
func (u UpdatesConfig) TimeoutDuration() time.Duration {
	if u.Timeout == "" {
		return 0
	}

	d, err := ParseDurationWithDays(u.Timeout)
	if err != nil || d <= 0 {
		return 0
	}

	return d
}

// ToolsConfig is the [tools] block overriding the external executables graith
// shells out to (issue #1238). Each field may be a bare command name resolved
// on PATH ("git", "hub") or an absolute/relative path to a specific binary
// ("/run/current-system/sw/bin/git"). An empty field keeps graith's built-in
// default (see tools.Defaults). This unblocks Nix/custom-PATH installs, wrapper
// binaries, and alternate shells. Only explicit overrides are validated at
// startup; unset defaults retain plain PATH-lookup semantics.
type ToolsConfig struct {
	// Git is the git executable (default "git").
	Git string `toml:"git"`
	// GH is the GitHub CLI executable (default "gh").
	GH string `toml:"gh"`
	// Shell runs notification and trigger commands as `<shell> -c <cmd>`
	// (default "sh").
	Shell string `toml:"shell"`
	// OSAScript is the macOS osascript executable used for desktop
	// notifications (default "osascript").
	OSAScript string `toml:"osascript"`
	// PS is the process-listing executable (default "/bin/ps").
	PS string `toml:"ps"`
	// Lsof is the open-files listing executable (default "/usr/sbin/lsof").
	Lsof string `toml:"lsof"`
}

// normalizeToolPath resolves a single configured tool value to a stable form
// (issue #1293). A bare command name (no path separator) keeps PATH-lookup
// semantics and is returned unchanged; a value that names a path — relative or
// absolute, or one starting with ~/ — is expanded and, when relative, resolved
// against baseDir (the directory holding config.toml) so the SAME absolute path
// is used both for startup validation and for every execution site. Without
// this a relative path like "./bin/git-wrapper" validated against the daemon cwd
// would later be re-evaluated against a git command's exec.Cmd.Dir and fail.
func normalizeToolPath(val, baseDir string) string {
	if val == "" {
		return ""
	}

	// Bare command name: resolved on PATH, must not be rewritten into a path.
	if !hasToolPathSeparator(val) && !strings.HasPrefix(val, "~/") {
		return val
	}

	return ExpandPathRelative(val, baseDir)
}

// hasToolPathSeparator reports whether val contains a path separator, matching
// the tools package's own bare-name-vs-path test so normalization and validation
// agree on which values are paths.
func hasToolPathSeparator(val string) bool {
	return strings.ContainsRune(val, '/') || strings.ContainsRune(val, os.PathSeparator)
}

// NormalizeRelative returns a copy of t with every path-valued tool resolved to
// a stable absolute path against baseDir (issue #1293). Bare names and already
// absolute paths are preserved. Load calls this once so Validate and Resolved
// (hence tools.Configure) observe the identical resolved path.
func (t ToolsConfig) NormalizeRelative(baseDir string) ToolsConfig {
	t.Git = normalizeToolPath(t.Git, baseDir)
	t.GH = normalizeToolPath(t.GH, baseDir)
	t.Shell = normalizeToolPath(t.Shell, baseDir)
	t.OSAScript = normalizeToolPath(t.OSAScript, baseDir)
	t.PS = normalizeToolPath(t.PS, baseDir)
	t.Lsof = normalizeToolPath(t.Lsof, baseDir)

	return t
}

// Resolved converts the config block into the tools package's Config. Empty
// fields are left empty here; tools.Configure fills them from tools.Defaults so
// there is a single source of default values.
func (t ToolsConfig) Resolved() tools.Config {
	return tools.Config{
		Git:       t.Git,
		GH:        t.GH,
		Shell:     t.Shell,
		OSAScript: t.OSAScript,
		PS:        t.PS,
		Lsof:      t.Lsof,
	}
}

// Validate checks that every explicitly-set tool override resolves (an absolute
// path exists and is executable; a bare name is found on PATH). Unset fields are
// skipped so defaults keep PATH-lookup semantics.
func (t ToolsConfig) Validate() error {
	return tools.Validate(t.Resolved())
}

// GitConfig is the [git] block tuning the timeouts graith applies to the git
// operations it runs during session lifecycle (issue #1238). Slower
// repositories, large fetches, and high-latency remotes can legitimately exceed
// the built-in 2m fetch / 2m merge / 15s username bounds. An empty field keeps
// the built-in default. Note this is distinct from [git_pull], which configures
// the background maintenance-pull loop.
type GitConfig struct {
	// FetchTimeout bounds a single `git fetch` (default "2m").
	FetchTimeout string `toml:"fetch_timeout"`
	// MergeTimeout bounds a single fast-forward merge in the git-pull loop
	// (default "2m").
	MergeTimeout string `toml:"merge_timeout"`
	// UsernameTimeout bounds GitHub-username discovery, which may invoke `gh`
	// (default "15s").
	UsernameTimeout string `toml:"username_timeout"`
}

// FetchTimeoutDuration returns the configured git-fetch timeout, defaulting to
// 2m when unset or unparseable (a bad value is rejected at load by Validate).
func (g GitConfig) FetchTimeoutDuration() time.Duration {
	return parseDurationOr(g.FetchTimeout, 2*time.Minute)
}

// MergeTimeoutDuration returns the configured merge timeout, defaulting to 2m.
func (g GitConfig) MergeTimeoutDuration() time.Duration {
	return parseDurationOr(g.MergeTimeout, 2*time.Minute)
}

// UsernameTimeoutDuration returns the configured username-discovery timeout,
// defaulting to 15s.
func (g GitConfig) UsernameTimeoutDuration() time.Duration {
	return parseDurationOr(g.UsernameTimeout, 15*time.Second)
}

// ConnectionConfig is the [connection] block tuning the deadlines and retry
// cadence the stateless `gr` client applies when talking to a daemon (issue
// #1242). Previously these were hard-coded (local dial 500ms, handshake/start
// 5s, startup re-probe 50ms, attach reconnect 10s/250ms, remote dial/handshake
// 10s/15s, remote pairing wait 11m). Slow machines, high-latency links, and
// remote daemons on constrained networks can legitimately exceed the built-in
// bounds, so each is overridable. Every value is a duration; an empty,
// unparseable, or non-positive entry keeps the built-in default (a bad value is
// rejected at config load by Validate). These are read once at CLI startup and
// installed into the client, so a change takes effect on the next `gr`
// invocation.
type ConnectionConfig struct {
	// DialTimeout bounds a single Unix-socket dial to the local daemon
	// (default "500ms").
	DialTimeout string `toml:"dial_timeout"`
	// HandshakeTimeout bounds the local-daemon handshake exchange, so a stale
	// or wedged socket can't hang a command forever (default "5s").
	HandshakeTimeout string `toml:"handshake_timeout"`
	// StartTimeout bounds how long EnsureDaemon waits for a freshly spawned
	// daemon to begin answering handshakes (default "5s").
	StartTimeout string `toml:"start_timeout"`
	// StartPollInterval is how often EnsureDaemon re-probes the socket while
	// waiting for a spawned daemon to come up (default "50ms").
	StartPollInterval string `toml:"start_poll_interval"`
	// ReconnectTimeout bounds the attach disconnect-recovery retry before the
	// client gives up reattaching (default "10s").
	ReconnectTimeout string `toml:"reconnect_timeout"`
	// ReconnectInterval is how often the attach recovery loop re-probes the
	// daemon while reconnecting (default "250ms").
	ReconnectInterval string `toml:"reconnect_interval"`
	// RemoteDialTimeout bounds the TCP dial to a paired remote daemon
	// (default "10s").
	RemoteDialTimeout string `toml:"remote_dial_timeout"`
	// RemoteHandshakeTimeout bounds the remote handshake plus
	// proof-of-possession exchange (default "15s").
	RemoteHandshakeTimeout string `toml:"remote_handshake_timeout"`
	// RemotePairingTimeout bounds how long the CLI waits for the remote human to
	// approve `gr pair`, and should sit just past the daemon's pending-pairing
	// TTL (default "11m").
	RemotePairingTimeout string `toml:"remote_pairing_timeout"`
}

// Connection timing defaults. Each mirrors the fixed value that governed the
// behaviour before issue #1242 made the policy configurable.
const (
	ConnectionDialTimeoutDefault            = 500 * time.Millisecond
	ConnectionHandshakeTimeoutDefault       = 5 * time.Second
	ConnectionStartTimeoutDefault           = 5 * time.Second
	ConnectionStartPollIntervalDefault      = 50 * time.Millisecond
	ConnectionReconnectTimeoutDefault       = 10 * time.Second
	ConnectionReconnectIntervalDefault      = 250 * time.Millisecond
	ConnectionRemoteDialTimeoutDefault      = 10 * time.Second
	ConnectionRemoteHandshakeTimeoutDefault = 15 * time.Second
	ConnectionRemotePairingTimeoutDefault   = 11 * time.Minute
)

// DialTimeoutDuration returns the local-daemon dial timeout, or the default when
// unset, unparseable, or non-positive (a zero/negative timeout would abort the
// dial immediately).
func (c ConnectionConfig) DialTimeoutDuration() time.Duration {
	return positiveDurationOrDefault(c.DialTimeout, ConnectionDialTimeoutDefault)
}

// HandshakeTimeoutDuration returns the local-daemon handshake timeout, or the
// default when unset, unparseable, or non-positive.
func (c ConnectionConfig) HandshakeTimeoutDuration() time.Duration {
	return positiveDurationOrDefault(c.HandshakeTimeout, ConnectionHandshakeTimeoutDefault)
}

// StartTimeoutDuration returns the daemon-startup wait, or the default when
// unset, unparseable, or non-positive.
func (c ConnectionConfig) StartTimeoutDuration() time.Duration {
	return positiveDurationOrDefault(c.StartTimeout, ConnectionStartTimeoutDefault)
}

// StartPollIntervalDuration returns the daemon-startup re-probe interval, or the
// default when unset, unparseable, or non-positive (a zero interval would
// busy-loop).
func (c ConnectionConfig) StartPollIntervalDuration() time.Duration {
	return positiveDurationOrDefault(c.StartPollInterval, ConnectionStartPollIntervalDefault)
}

// ReconnectTimeoutDuration returns the attach reconnect deadline, or the default
// when unset, unparseable, or non-positive.
func (c ConnectionConfig) ReconnectTimeoutDuration() time.Duration {
	return positiveDurationOrDefault(c.ReconnectTimeout, ConnectionReconnectTimeoutDefault)
}

// ReconnectIntervalDuration returns the attach reconnect re-probe interval, or
// the default when unset, unparseable, or non-positive (a zero interval would
// busy-loop).
func (c ConnectionConfig) ReconnectIntervalDuration() time.Duration {
	return positiveDurationOrDefault(c.ReconnectInterval, ConnectionReconnectIntervalDefault)
}

// RemoteDialTimeoutDuration returns the remote TCP dial timeout, or the default
// when unset, unparseable, or non-positive.
func (c ConnectionConfig) RemoteDialTimeoutDuration() time.Duration {
	return positiveDurationOrDefault(c.RemoteDialTimeout, ConnectionRemoteDialTimeoutDefault)
}

// RemoteHandshakeTimeoutDuration returns the remote handshake/PoP timeout, or
// the default when unset, unparseable, or non-positive.
func (c ConnectionConfig) RemoteHandshakeTimeoutDuration() time.Duration {
	return positiveDurationOrDefault(c.RemoteHandshakeTimeout, ConnectionRemoteHandshakeTimeoutDefault)
}

// RemotePairingTimeoutDuration returns the remote pairing-approval wait, or the
// default when unset, unparseable, or non-positive.
func (c ConnectionConfig) RemotePairingTimeoutDuration() time.Duration {
	return positiveDurationOrDefault(c.RemotePairingTimeout, ConnectionRemotePairingTimeoutDefault)
}

// HeadlessConfig is the [headless] block gating headless stream-json sessions
// (issue #1075). Headless is inert unless Experimental is true — the control
// protocol it uses is an SDK-internal contract, so v1 is opt-in and
// experimental. Default, when Experimental is on, decides whether new sessions
// go headless without an explicit --headless.
//
// The remaining fields make the headless driver's processing limits tunable
// (issue #1250). Each is optional: an empty/zero/non-positive value falls back
// to the matching default constant, preserving historical behaviour.
type HeadlessConfig struct {
	Experimental bool `toml:"experimental"`
	Default      bool `toml:"default"`
	// MaxLineBytes bounds a single stream-json line read from the agent's stdout.
	// Large tool outputs or base64 images exceed the 64KiB default scanner token,
	// so the driver raises the cap. 0/negative uses HeadlessMaxLineBytesDefault.
	MaxLineBytes int `toml:"max_line_bytes"`
	// ControlTimeout bounds how long a synchronous control request waits for its
	// matching control_response before failing. Empty/unparseable/non-positive
	// uses HeadlessControlTimeoutDefault.
	ControlTimeout string `toml:"control_timeout"`
	// InterruptTimeout bounds the interrupt control round-trip; it is much shorter
	// than ControlTimeout because a caller interrupting an agent wants a prompt
	// fall-through to SIGINT. Empty/unparseable/non-positive uses
	// HeadlessInterruptTimeoutDefault.
	InterruptTimeout string `toml:"interrupt_timeout"`
	// PreviewBytes bounds how much scrollback tail the overlay preview and
	// screen_preview control message render. 0/negative uses
	// HeadlessPreviewBytesDefault.
	PreviewBytes int `toml:"preview_bytes"`
}

// Headless processing-limit defaults. Each mirrors the fixed constant that
// governed the behaviour before issue #1250 made the limits configurable.
const (
	HeadlessMaxLineBytesDefault     = 16 * 1024 * 1024
	HeadlessControlTimeoutDefault   = 30 * time.Second
	HeadlessInterruptTimeoutDefault = 5 * time.Second
	HeadlessPreviewBytesDefault     = 16 * 1024
)

// MaxLineBytesOrDefault returns the stream-json line cap, or the default when
// unset or non-positive.
func (h HeadlessConfig) MaxLineBytesOrDefault() int {
	return positiveIntOrDefault(h.MaxLineBytes, HeadlessMaxLineBytesDefault)
}

// ControlTimeoutDuration returns the control-request timeout, or the default
// when unset, unparseable, or non-positive.
func (h HeadlessConfig) ControlTimeoutDuration() time.Duration {
	return positiveDurationOrDefault(h.ControlTimeout, HeadlessControlTimeoutDefault)
}

// InterruptTimeoutDuration returns the interrupt round-trip timeout, or the
// default when unset, unparseable, or non-positive.
func (h HeadlessConfig) InterruptTimeoutDuration() time.Duration {
	return positiveDurationOrDefault(h.InterruptTimeout, HeadlessInterruptTimeoutDefault)
}

// PreviewBytesOrDefault returns the preview tail cap, or the default when unset
// or non-positive.
func (h HeadlessConfig) PreviewBytesOrDefault() int {
	return positiveIntOrDefault(h.PreviewBytes, HeadlessPreviewBytesDefault)
}

// MigrationHealthWindowDefault is how long Migrate waits to confirm the target
// agent survived startup before declaring the migration successful, used when
// [migration] health_window is unset (issue #1250).
const MigrationHealthWindowDefault = 1500 * time.Millisecond

// MigrationConfig is the [migration] block tuning the cross-agent conversation
// migration (issue #1250). HealthWindow is optional: empty, unparseable, or
// non-positive falls back to MigrationHealthWindowDefault.
type MigrationConfig struct {
	// HealthWindow is how long Migrate waits to confirm the target agent survived
	// startup before declaring the migration successful. Empty/unparseable/
	// non-positive uses MigrationHealthWindowDefault.
	HealthWindow string `toml:"health_window"`
}

// HealthWindowDuration returns the migration startup-health window, or the
// default when unset, unparseable, or non-positive.
func (m MigrationConfig) HealthWindowDuration() time.Duration {
	return positiveDurationOrDefault(m.HealthWindow, MigrationHealthWindowDefault)
}

// Transcript processing-limit defaults. Each mirrors the fixed constant that
// governed the transcript reader/renderer before issue #1250.
const (
	TranscriptMaxContextBytesDefault    = 256 * 1024
	TranscriptMaxToolOutputBytesDefault = 4 * 1024
	TranscriptMaxLineBytesDefault       = 16 * 1024 * 1024
	TranscriptMaxMetadataBytesDefault   = 4 * 1024 * 1024
)

// TranscriptConfig is the [transcript] block tuning the on-disk agent-transcript
// reader and renderer used by migration and fork (issue #1250). Every field is
// optional: a zero/non-positive value falls back to the matching default.
type TranscriptConfig struct {
	// MaxContextBytes is the approximate size budget for the rendered migration/
	// fork context document; older turns are elided to fit. 0/negative uses
	// TranscriptMaxContextBytesDefault.
	MaxContextBytes int `toml:"max_context_bytes"`
	// MaxToolOutputBytes caps each rendered tool-output block. 0/negative uses
	// TranscriptMaxToolOutputBytesDefault.
	MaxToolOutputBytes int `toml:"max_tool_output_bytes"`
	// MaxLineBytes is the scanner buffer cap for a single transcript line while
	// reading turns or summing usage (large tool outputs / base64 exceed the
	// 64KiB default). 0/negative uses TranscriptMaxLineBytesDefault.
	MaxLineBytes int `toml:"max_line_bytes"`
	// MaxMetadataLineBytes is the scanner buffer cap for the small metadata-only
	// scans (Codex rollout cwd / session-id lookup). 0/negative uses
	// TranscriptMaxMetadataBytesDefault.
	MaxMetadataLineBytes int `toml:"max_metadata_line_bytes"`
}

// MaxContextBytesOrDefault returns the rendered-context byte budget, or the
// default when unset or non-positive.
func (t TranscriptConfig) MaxContextBytesOrDefault() int {
	return positiveIntOrDefault(t.MaxContextBytes, TranscriptMaxContextBytesDefault)
}

// MaxToolOutputBytesOrDefault returns the per-tool-output cap, or the default
// when unset or non-positive.
func (t TranscriptConfig) MaxToolOutputBytesOrDefault() int {
	return positiveIntOrDefault(t.MaxToolOutputBytes, TranscriptMaxToolOutputBytesDefault)
}

// MaxLineBytesOrDefault returns the transcript-line scanner cap, or the default
// when unset or non-positive.
func (t TranscriptConfig) MaxLineBytesOrDefault() int {
	return positiveIntOrDefault(t.MaxLineBytes, TranscriptMaxLineBytesDefault)
}

// MaxMetadataLineBytesOrDefault returns the metadata-scan scanner cap, or the
// default when unset or non-positive.
func (t TranscriptConfig) MaxMetadataLineBytesOrDefault() int {
	return positiveIntOrDefault(t.MaxMetadataLineBytes, TranscriptMaxMetadataBytesDefault)
}

// positiveIntOrDefault returns def when n is zero or negative; otherwise n. Used
// for byte-size limits where a zero/negative value has no sensible meaning.
func positiveIntOrDefault(n, def int) int {
	if n <= 0 {
		return def
	}

	return n
}

type Overlay struct {
	ShortcutKeys string `toml:"shortcut_keys"`
}

// InputConfig is the optional [input] block controlling terminal input
// gestures in the attach passthrough loop.
type InputConfig struct {
	// DragArrowKeys enables touch/hold-and-drag arrow keys:
	// press-and-hold the left mouse button then drag to emit discrete arrow-key
	// presses to the focused pane. Off by default because it repurposes
	// left-drag (which terminals otherwise use for text selection). Mouse-wheel
	// scrolling is always passed through unchanged.
	DragArrowKeys bool `toml:"drag_arrow_keys"`
	// DragArrowThreshold is the number of cells of drag movement that produces
	// one arrow-key press. Values below 1 fall back to the default.
	DragArrowThreshold int `toml:"drag_arrow_threshold"`
}

// DefaultRemotePort is the TCP port the tailnet control listener binds when
// [remote] port is unset, and the default the `gr remote pair` client dials.
// It is the single source of truth for the port on the Go side; the embedded
// default_config.toml carries the same value (kept in lockstep by a test) and
// the Swift clients mirror it via GraithTransport.defaultRemotePort.
const DefaultRemotePort = 4823

// RemoteConfig is the optional, off-by-default [remote] block that exposes a
// tailnet-facing control listener (see the native-app design doc §A.4/§B). It
// is fail-closed: when Enabled, an invalid block is a hard config-load error
// (static validation only — runtime listener provisioning failures, e.g. a
// missing tailnet IP or cert, are handled by the remote listener, not here).
type RemoteConfig struct {
	// Enabled turns the remote listener on. Off by default; when false the rest
	// of the block is not validated so a disabled block never blocks startup.
	Enabled bool `toml:"enabled"`
	// Mode selects the transport: "tsnet" (embedded Tailscale via tsnet) or
	// "interface" (bind the host's existing tailnet interface IP).
	Mode string `toml:"mode"`
	// Hostname is the tsnet node name / MagicDNS label (tsnet mode).
	Hostname string `toml:"hostname"`
	// Port is the TCP port the listener binds.
	Port int `toml:"port"`
	// AuthKeyFile is the path to a tsnet auth key (tsnet mode only).
	AuthKeyFile string `toml:"auth_key_file"`
	// Tags are the tsnet ACL tags applied to the node (tsnet mode only).
	Tags []string `toml:"tags"`
	// AllowTailnetUsers is the WhoIs allowlist (Gate 1). Entries are either a
	// tailnet user email or a "tag:"-prefixed tag. A bare "tag:" entry opts
	// tagged nodes in; with no tag entry, tagged nodes are disallowed.
	AllowTailnetUsers []string `toml:"allow_tailnet_users"`
	// RequirePairing requires per-device pairing (Gate 2) for human-level
	// rights. Defaults to true; false is UNSAFE (trusts the tailnet identity
	// alone) and is restricted to read-only access — see the design doc §B.2.
	RequirePairing bool `toml:"require_pairing"`
	// PairRequestRate is the anti-flood limit on pending pair requests, written
	// "<n>/<unit>" (e.g. "5/min"); units are sec, min, or hour. Empty falls back
	// to the pair_fallback_count/pair_fallback_window rate below.
	PairRequestRate string `toml:"pair_request_rate"`
	// MaxPendingPairings caps how many unapproved pair requests may be
	// outstanding at once (anti-flood). 0 uses the default
	// (RemoteMaxPendingPairingsDefault); values outside [1,
	// RemoteMaxPendingPairingsMax] are a hard config error.
	MaxPendingPairings int `toml:"max_pending_pairings"`
	// PendingPairingTTL is how long an unapproved pair request lives before it
	// expires and can no longer be approved. Empty uses the default
	// (RemotePendingPairingTTLDefault); a parsed value outside
	// [RemotePendingPairingTTLMin, RemotePendingPairingTTLMax] is a hard error.
	PendingPairingTTL string `toml:"pending_pairing_ttl"`
	// PairFallbackCount is the request count of the rate limit applied when
	// pair_request_rate is unset. 0 uses the default
	// (RemotePairFallbackCountDefault); values outside [1,
	// RemotePairFallbackCountMax] are a hard config error.
	PairFallbackCount int `toml:"pair_fallback_count"`
	// PairFallbackWindow is the window of the rate limit applied when
	// pair_request_rate is unset. Empty uses the default
	// (RemotePairFallbackWindowDefault); a parsed value outside
	// [RemotePairFallbackWindowMin, RemotePairFallbackWindowMax] is a hard error.
	PairFallbackWindow string `toml:"pair_fallback_window"`
}

// Pairing policy bounds. Defaults preserve the historically-fixed values (16
// pending, 10m TTL, 5/min fallback rate); the ceilings/floors keep an operator
// override from disabling anti-flood protection or pinning requests forever.
const (
	RemoteMaxPendingPairingsDefault = 16
	RemoteMaxPendingPairingsMax     = 1024
	RemotePendingPairingTTLDefault  = 10 * time.Minute
	RemotePendingPairingTTLMin      = time.Minute
	RemotePendingPairingTTLMax      = 24 * time.Hour
	RemotePairFallbackCountDefault  = 5
	RemotePairFallbackCountMax      = 1000
	RemotePairFallbackWindowDefault = time.Minute
	RemotePairFallbackWindowMin     = time.Second
	RemotePairFallbackWindowMax     = 24 * time.Hour
)

// MaxPendingPairingsOrDefault returns the configured pending-pairing cap,
// applying the default when unset and clamping to the safe bounds so a caller
// that skipped Validate can never act on an unsafe value.
func (r RemoteConfig) MaxPendingPairingsOrDefault() int {
	n := r.MaxPendingPairings
	if n < 1 {
		return RemoteMaxPendingPairingsDefault
	}

	if n > RemoteMaxPendingPairingsMax {
		return RemoteMaxPendingPairingsMax
	}

	return n
}

// PendingPairingTTLDuration returns the configured pending-pairing TTL, applying
// the default when unset/unparseable and clamping to the safe bounds.
func (r RemoteConfig) PendingPairingTTLDuration() time.Duration {
	if strings.TrimSpace(r.PendingPairingTTL) == "" {
		return RemotePendingPairingTTLDefault
	}

	d, err := ParseDurationWithDays(r.PendingPairingTTL)
	if err != nil {
		return RemotePendingPairingTTLDefault
	}

	switch {
	case d < RemotePendingPairingTTLMin:
		return RemotePendingPairingTTLMin
	case d > RemotePendingPairingTTLMax:
		return RemotePendingPairingTTLMax
	default:
		return d
	}
}

// PairFallbackRate returns the rate limit applied when pair_request_rate is
// unset, applying defaults and clamping each component to its safe bounds.
func (r RemoteConfig) PairFallbackRate() PairRate {
	count := r.PairFallbackCount

	switch {
	case count < 1:
		count = RemotePairFallbackCountDefault
	case count > RemotePairFallbackCountMax:
		count = RemotePairFallbackCountMax
	}

	per := RemotePairFallbackWindowDefault

	if strings.TrimSpace(r.PairFallbackWindow) != "" {
		if d, err := ParseDurationWithDays(r.PairFallbackWindow); err == nil {
			switch {
			case d < RemotePairFallbackWindowMin:
				per = RemotePairFallbackWindowMin
			case d > RemotePairFallbackWindowMax:
				per = RemotePairFallbackWindowMax
			default:
				per = d
			}
		}
	}

	return PairRate{Count: count, Per: per}
}

// PairRate is a parsed pair_request_rate: Count events per Per duration.
type PairRate struct {
	Count int
	Per   time.Duration
}

// ParsePairRequestRate parses a "<n>/<unit>" rate such as "5/min". The unit is
// one of sec/min/hour (with the aliases second/minute/hour). The count must be
// a positive integer. Any other shape is a hard error (fail-closed).
func ParsePairRequestRate(s string) (PairRate, error) {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 {
		return PairRate{}, fmt.Errorf("[remote] pair_request_rate %q is invalid (want \"<n>/<unit>\" like \"5/min\")", s)
	}

	n, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || n <= 0 {
		return PairRate{}, fmt.Errorf("[remote] pair_request_rate %q: count must be a positive integer", s)
	}

	var per time.Duration

	switch strings.ToLower(strings.TrimSpace(parts[1])) {
	case "sec", "second", "seconds", "s":
		per = time.Second
	case "min", "minute", "minutes", "m":
		per = time.Minute
	case "hour", "hours", "hr", "h":
		per = time.Hour
	default:
		return PairRate{}, fmt.Errorf("[remote] pair_request_rate %q: unit must be sec, min, or hour", s)
	}

	return PairRate{Count: n, Per: per}, nil
}

// AllowsTaggedNodes reports whether any allow_tailnet_users entry opts tagged
// nodes in (a "tag:"-prefixed entry). With no such entry, tagged nodes — which
// WhoIs resolves with no user — are disallowed by default.
func (r RemoteConfig) AllowsTaggedNodes() bool {
	for _, u := range r.AllowTailnetUsers {
		if strings.HasPrefix(strings.TrimSpace(u), "tag:") {
			return true
		}
	}

	return false
}

// Validate checks the [remote] block for static contradictions. Rules are only
// enforced when Enabled — a disabled block (even with otherwise-invalid values)
// always loads. It is fail-closed: an invalid enabled block is a hard error.
func (r RemoteConfig) Validate() error {
	if !r.Enabled {
		return nil
	}

	switch r.Mode {
	case "tsnet", "interface":
	default:
		return fmt.Errorf("[remote] mode %q is invalid (want \"tsnet\" or \"interface\")", r.Mode)
	}

	if r.Port <= 0 || r.Port > 65535 {
		return fmt.Errorf("[remote] port %d is invalid (want 1-65535)", r.Port)
	}

	if r.Mode == "interface" {
		if strings.TrimSpace(r.AuthKeyFile) != "" {
			return errors.New("[remote] auth_key_file is a tsnet-only field and cannot be set in interface mode")
		}

		if len(r.Tags) > 0 {
			return errors.New("[remote] tags is a tsnet-only field and cannot be set in interface mode")
		}
	}

	if strings.TrimSpace(r.PairRequestRate) != "" {
		if _, err := ParsePairRequestRate(r.PairRequestRate); err != nil {
			return err
		}
	}

	if r.MaxPendingPairings != 0 && (r.MaxPendingPairings < 1 || r.MaxPendingPairings > RemoteMaxPendingPairingsMax) {
		return fmt.Errorf("[remote] max_pending_pairings %d is invalid (want 1-%d, or 0 for the default)", r.MaxPendingPairings, RemoteMaxPendingPairingsMax)
	}

	if err := validateBoundedDuration("[remote] pending_pairing_ttl", r.PendingPairingTTL, RemotePendingPairingTTLMin, RemotePendingPairingTTLMax); err != nil {
		return err
	}

	if r.PairFallbackCount != 0 && (r.PairFallbackCount < 1 || r.PairFallbackCount > RemotePairFallbackCountMax) {
		return fmt.Errorf("[remote] pair_fallback_count %d is invalid (want 1-%d, or 0 for the default)", r.PairFallbackCount, RemotePairFallbackCountMax)
	}

	if err := validateBoundedDuration("[remote] pair_fallback_window", r.PairFallbackWindow, RemotePairFallbackWindowMin, RemotePairFallbackWindowMax); err != nil {
		return err
	}

	return nil
}

// validatePositiveDurationField appends an error when val is set but does not
// parse or is non-positive. An empty val is accepted (means "use the default").
// Shared by the lifecycle/launch validation so a set-but-invalid cadence or wait
// fails at load rather than silently falling back to the accessor default.
func validatePositiveDurationField(errs *[]error, name, val string) {
	if strings.TrimSpace(val) == "" {
		return
	}

	if d, err := ParseDurationWithDays(val); err != nil {
		*errs = append(*errs, fmt.Errorf("%s %q: %w", name, val, err))
	} else if d <= 0 {
		*errs = append(*errs, fmt.Errorf("%s %q: must be greater than zero", name, val))
	}
}

// validateBoundedDuration reports a hard error if s is set but does not parse or
// falls outside [min, max]. An empty s is accepted (means "use the default").
func validateBoundedDuration(field, s string, minDur, maxDur time.Duration) error {
	if strings.TrimSpace(s) == "" {
		return nil
	}

	d, err := ParseDurationWithDays(s)
	if err != nil {
		return fmt.Errorf("%s %q is invalid (want a duration like \"10m\")", field, s)
	}

	if d < minDur || d > maxDur {
		return fmt.Errorf("%s %q is out of bounds (want %s-%s)", field, s, minDur, maxDur)
	}

	return nil
}

type OrchestratorConfig struct {
	Enabled     bool                      `toml:"enabled"`
	Agent       string                    `toml:"agent"`
	Model       string                    `toml:"model"`
	IdleTimeout string                    `toml:"idle_timeout"`
	Prompt      string                    `toml:"prompt"`
	PromptFile  string                    `toml:"prompt_file"`
	Sandbox     OrchestratorSandboxConfig `toml:"sandbox"`
	Restart     OrchestratorRestartConfig `toml:"restart"`
}

// OrchestratorRestartConfig tunes how the daemon auto-restarts the orchestrator
// after it exits unexpectedly (crash/watchdog, not a user/idle/shutdown stop).
//
// Two backoff modes are supported. When Schedule is non-empty it wins: it is the
// explicit list of per-attempt delays, and the final entry repeats for every
// attempt beyond its length. When Schedule is empty a geometric backoff is
// computed as InitialBackoff × Multiplier^level, capped at MaxBackoff.
type OrchestratorRestartConfig struct {
	// InitialBackoff is the first restart delay in geometric mode (Schedule empty).
	// Empty uses OrchestratorInitialBackoffDefault.
	InitialBackoff string `toml:"initial_backoff"`
	// MaxBackoff caps the restart delay in geometric mode. Empty uses
	// OrchestratorMaxBackoffDefault.
	MaxBackoff string `toml:"max_backoff"`
	// Multiplier grows the delay each attempt in geometric mode. Values <= 1 fall
	// back to OrchestratorMultiplierDefault.
	Multiplier float64 `toml:"multiplier"`
	// Schedule is an explicit list of per-attempt delays. When set it overrides
	// the geometric knobs; the last entry repeats for attempts beyond its length.
	Schedule []string `toml:"schedule"`
	// StableReset is how long a run must last before its exit resets the backoff
	// level to 0. Empty uses OrchestratorStableResetDefault.
	StableReset string `toml:"stable_reset"`
	// FreshStartThreshold is the number of consecutive restarts after which the
	// orchestrator is relaunched fresh (new agent session id). Values < 1 fall
	// back to OrchestratorFreshStartThresholdDefault.
	FreshStartThreshold int `toml:"fresh_start_threshold"`
}

// Orchestrator restart defaults. The default schedule preserves graith's
// historical backoff curve exactly; the geometric defaults apply only when a
// user clears Schedule to opt into computed backoff.
const (
	OrchestratorInitialBackoffDefault      = 2 * time.Second
	OrchestratorMaxBackoffDefault          = 300 * time.Second
	OrchestratorMultiplierDefault          = 2.0
	OrchestratorStableResetDefault         = 60 * time.Second
	OrchestratorFreshStartThresholdDefault = 3
)

var orchestratorDefaultSchedule = []time.Duration{
	2 * time.Second,
	4 * time.Second,
	8 * time.Second,
	16 * time.Second,
	32 * time.Second,
	60 * time.Second,
	300 * time.Second,
}

// parsedSchedule parses the explicit Schedule entries, dropping any that fail to
// parse or are non-positive. Validate rejects those values on load; filtering
// here is the defensive fallback for directly-constructed configs so a restart
// can never be scheduled without a positive delay. It returns nil when Schedule
// is empty or nothing valid survives, signalling geometric mode to callers.
func (r OrchestratorRestartConfig) parsedSchedule() []time.Duration {
	if len(r.Schedule) == 0 {
		return nil
	}

	out := make([]time.Duration, 0, len(r.Schedule))

	for _, s := range r.Schedule {
		d, err := ParseDurationWithDays(s)
		if err != nil || d <= 0 {
			continue
		}

		out = append(out, d)
	}

	if len(out) == 0 {
		return nil
	}

	return out
}

// DelayForLevel returns the restart delay for a given (0-based) backoff level.
// An explicit Schedule wins, with its final entry repeating past its length;
// otherwise the delay is computed geometrically. With no restart config at all
// this reproduces graith's historical schedule (2s,4s,8s,16s,32s,60s,300s).
func (r OrchestratorRestartConfig) DelayForLevel(level int) time.Duration {
	if level < 0 {
		level = 0
	}

	if sched := r.parsedSchedule(); sched != nil {
		if level >= len(sched) {
			level = len(sched) - 1
		}

		return sched[level]
	}

	// Nothing configured at all: preserve the historical schedule exactly rather
	// than the (subtly different) geometric curve.
	if r.InitialBackoff == "" && r.MaxBackoff == "" && r.Multiplier == 0 {
		idx := level
		if idx >= len(orchestratorDefaultSchedule) {
			idx = len(orchestratorDefaultSchedule) - 1
		}

		return orchestratorDefaultSchedule[idx]
	}

	initial := positiveDurationOrDefault(r.InitialBackoff, OrchestratorInitialBackoffDefault)
	maxDelay := positiveDurationOrDefault(r.MaxBackoff, OrchestratorMaxBackoffDefault)

	mult := r.Multiplier
	if mult <= 1 {
		mult = OrchestratorMultiplierDefault
	}

	delay := float64(initial)
	for i := 0; i < level; i++ {
		delay *= mult
		if delay >= float64(maxDelay) {
			return maxDelay
		}
	}

	if d := time.Duration(delay); d < maxDelay {
		return d
	}

	return maxDelay
}

// StableResetDuration is how long a run must last before its exit resets the
// backoff level. Empty, unparseable, or non-positive uses
// OrchestratorStableResetDefault.
func (r OrchestratorRestartConfig) StableResetDuration() time.Duration {
	return positiveDurationOrDefault(r.StableReset, OrchestratorStableResetDefault)
}

// FreshStartThresholdOrDefault returns the consecutive-restart count that
// triggers a fresh start. Non-positive values fall back to the default.
func (r OrchestratorRestartConfig) FreshStartThresholdOrDefault() int {
	if r.FreshStartThreshold < 1 {
		return OrchestratorFreshStartThresholdDefault
	}

	return r.FreshStartThreshold
}

// durationOrDefault parses a duration string, returning def when it is empty,
// unparseable, or negative. A parsed zero is preserved for the fields that use
// it as an explicit disable sentinel.
func durationOrDefault(s string, def time.Duration) time.Duration {
	if s == "" {
		return def
	}

	d, err := ParseDurationWithDays(s)
	if err != nil || d < 0 {
		return def
	}

	return d
}

type OrchestratorSandboxConfig struct {
	ReadDirs   []string `toml:"read_dirs"`
	WriteDirs  []string `toml:"write_dirs"`
	ReadFiles  []string `toml:"read_files"`
	WriteFiles []string `toml:"write_files"`
}

func (o OrchestratorConfig) IdleTimeoutDuration() time.Duration {
	if o.IdleTimeout == "" {
		return 30 * time.Minute
	}

	d, err := ParseDurationWithDays(o.IdleTimeout)
	if err != nil {
		return 30 * time.Minute
	}

	return d
}

// AgentName resolves the agent type the orchestrator session runs as. An
// explicit [orchestrator] agent wins; otherwise it inherits the top-level
// default_agent (passed in by the caller, which has access to the full
// config), falling back to "claude" only when neither is set.
func (o OrchestratorConfig) AgentName(defaultAgent string) string {
	if o.Agent != "" {
		return o.Agent
	}

	if defaultAgent != "" {
		return defaultAgent
	}

	return "claude"
}

// LaunchConfig bounds concurrent agent-session startup and recovers sessions
// that stall during launch (issue #1092). Bursts of `gr new` otherwise let many
// heavyweight agent runtimes initialise at once, and the tail can stall for
// minutes or hang forever at ~9MB RSS (sandbox wrapper only, agent never loaded).
type LaunchConfig struct {
	// MaxConcurrent bounds how many agent spawns may be in their startup window
	// at once. Values < 1 fall back to the default (LaunchMaxConcurrentDefault).
	MaxConcurrent int `toml:"max_concurrent"`
	// StartupTimeout is how long a session may stay running with no output before
	// the startup watchdog kills and restarts it fresh. "0" disables the
	// watchdog; empty uses the default (LaunchStartupTimeoutDefault).
	StartupTimeout string `toml:"startup_timeout"`
	// SettleTimeout caps how long a launch holds its throttle slot waiting for
	// the session's first output before releasing it anyway. Empty uses the
	// default (LaunchSettleTimeoutDefault); "0" releases immediately after spawn.
	SettleTimeout string `toml:"settle_timeout"`
	// MaxRestarts caps how many consecutive startup-watchdog restarts a single
	// session may receive before it is marked errored instead of restarted again,
	// preventing a restart storm for a fundamentally-broken session (#1092). The
	// counter resets once the session produces output. Values < 1 fall back to the
	// default (LaunchMaxRestartsDefault). To turn the watchdog off entirely, set
	// startup_timeout to "0" rather than dropping this to zero.
	MaxRestarts int `toml:"max_restarts"`
	// WatchdogInterval is how often the startup watchdog scans for stuck sessions.
	// Empty, unparseable, or non-positive uses the default
	// (LaunchWatchdogIntervalDefault); a zero cadence would busy-loop. Read once
	// when the watchdog loop starts, so a change takes effect on the next daemon
	// (re)start.
	WatchdogInterval string `toml:"watchdog_interval"`
	// SlotPollInterval is how often a held throttle slot polls a freshly-spawned
	// session for its first output before releasing. Empty, unparseable, or
	// non-positive uses the default (LaunchSlotPollIntervalDefault); a zero cadence
	// would busy-loop.
	SlotPollInterval string `toml:"slot_poll_interval"`
}

// Launch tuning defaults. MaxConcurrent defaults to 3 because the #1092
// evidence showed ~4 concurrent startups completing fine while the 5th stalled.
const (
	LaunchMaxConcurrentDefault    = 3
	LaunchStartupTimeoutDefault   = 3 * time.Minute
	LaunchSettleTimeoutDefault    = 10 * time.Second
	LaunchMaxRestartsDefault      = 3
	LaunchWatchdogIntervalDefault = 15 * time.Second
	LaunchSlotPollIntervalDefault = 100 * time.Millisecond
)

// MaxConcurrentOrDefault returns the configured concurrency, clamped to a
// sensible minimum. A non-positive value means "use the default".
func (l LaunchConfig) MaxConcurrentOrDefault() int {
	if l.MaxConcurrent < 1 {
		return LaunchMaxConcurrentDefault
	}

	return l.MaxConcurrent
}

// StartupTimeoutDuration returns the watchdog threshold. Empty uses the default;
// an explicit "0" (or any non-positive parse) disables the watchdog.
func (l LaunchConfig) StartupTimeoutDuration() time.Duration {
	if l.StartupTimeout == "" {
		return LaunchStartupTimeoutDefault
	}

	d, err := ParseDurationWithDays(l.StartupTimeout)
	if err != nil || d < 0 {
		return LaunchStartupTimeoutDefault
	}

	return d
}

// SettleTimeoutDuration returns how long a slot waits for first output. Empty
// uses the default; an explicit "0" releases the slot as soon as the spawn
// returns.
func (l LaunchConfig) SettleTimeoutDuration() time.Duration {
	if l.SettleTimeout == "" {
		return LaunchSettleTimeoutDefault
	}

	d, err := ParseDurationWithDays(l.SettleTimeout)
	if err != nil || d < 0 {
		return LaunchSettleTimeoutDefault
	}

	return d
}

// MaxRestartsOrDefault returns the stuck-launch restart budget, or the default
// when < 1 (mirrors MaxConcurrentOrDefault). The watchdog is disabled via
// startup_timeout = "0", not by zeroing this.
func (l LaunchConfig) MaxRestartsOrDefault() int {
	if l.MaxRestarts < 1 {
		return LaunchMaxRestartsDefault
	}

	return l.MaxRestarts
}

// WatchdogIntervalDuration returns the watchdog scan cadence, or the default
// when unset, unparseable, or non-positive (a zero cadence would busy-loop).
func (l LaunchConfig) WatchdogIntervalDuration() time.Duration {
	return positiveDurationOrDefault(l.WatchdogInterval, LaunchWatchdogIntervalDefault)
}

// SlotPollIntervalDuration returns the settle poll cadence, or the default when
// unset, unparseable, or non-positive (a zero cadence would busy-loop).
func (l LaunchConfig) SlotPollIntervalDuration() time.Duration {
	return positiveDurationOrDefault(l.SlotPollInterval, LaunchSlotPollIntervalDefault)
}

// LifecycleConfig is the [lifecycle] block gathering the session-lifecycle and
// PTY policy that was previously spread as fixed constants and bare literals
// across the daemon, headless, and pty packages (issue #1243): the
// convert-to-interactive signal-escalation waits, the headless interrupt
// round-trip, mass-exit detection, the process-teardown grace, adopted-PTY
// babysit timing, scrollback hydration, terminal-input pacing, the default
// launch geometry, and the per-session log cap.
//
// The signal-escalation ORDER (interrupt → SIGTERM → SIGKILL) stays a code
// invariant; only the wait durations between steps are tunable here. Every field
// is optional: an empty value uses the matching default constant, preserving the
// historical behaviour. Explicit unparseable or non-positive durations are
// rejected by Config.Validate; the accessors retain defensive defaults for
// programmatically constructed config. Geometry and log/
// hydration limits apply only to sessions launched (or adopted) after the change;
// running sessions keep the geometry and caps they started with.
type LifecycleConfig struct {
	// ConvertSettleTimeout bounds how long ConvertToInteractive waits for an
	// interrupted headless process to settle and exit before escalating to
	// SIGTERM. Unset uses the default; an explicitly non-positive value is invalid.
	ConvertSettleTimeout string `toml:"convert_settle_timeout"`
	// ConvertKillTimeout bounds the SIGTERM step before the final SIGKILL. Unset
	// uses the default; an explicitly non-positive value is invalid.
	ConvertKillTimeout string `toml:"convert_kill_timeout"`
	// ConvertForceKillTimeout bounds the final wait after SIGKILL so a process
	// whose Done() never closes can't stall the convert forever. Unset uses the
	// default; an explicitly non-positive value is invalid.
	ConvertForceKillTimeout string `toml:"convert_force_kill_timeout"`
	// MassExitWindow is the rolling window over which many near-simultaneous
	// session exits are counted as a likely external signal (OOM killer/jetsam).
	// Unset uses the default; an explicitly non-positive value is invalid.
	MassExitWindow string `toml:"mass_exit_window"`
	// MassExitThreshold is how many exits within MassExitWindow trigger the
	// mass-exit warning. Values < 1 fall back to the default (MassExitThresholdDefault).
	MassExitThreshold int `toml:"mass_exit_threshold"`
	// ProcessKillGrace is how long killProcessGroup waits after SIGTERM before
	// sending SIGKILL to a session's process group or live driver. A second
	// grace-bounded wait after SIGKILL prevents teardown from hanging. Unset uses
	// the default; an explicitly non-positive value is invalid.
	ProcessKillGrace string `toml:"process_kill_grace"`
	// AdoptedTimeout is the safety deadline the adopted-PTY babysit loop applies
	// when it cannot verify process identity by start time. Unset uses the default;
	// an explicitly non-positive value is invalid. Applies to sessions adopted after
	// the change (daemon upgrade).
	AdoptedTimeout string `toml:"adopted_timeout"`
	// AdoptedPollInterval is how often the adopted-PTY babysit loop polls for
	// process exit. Unset uses the default; an explicitly non-positive value is
	// invalid because a zero cadence would busy-loop.
	AdoptedPollInterval string `toml:"adopted_poll_interval"`
	// ScrollbackHydrationBytes is how many bytes of the scrollback tail are
	// replayed into an adopted session's virtual screen at adopt time. Values < 0
	// fall back to the default (ScrollbackHydrationBytesDefault); "0" disables
	// hydration.
	ScrollbackHydrationBytes int `toml:"scrollback_hydration_bytes"`
	// InputDelay is the pause between writing text and the submit carriage return
	// in WriteInputAndSubmit, so a TUI doesn't treat text+CR as a paste. Unset uses
	// the default; an explicitly non-positive value is invalid because a zero
	// pause would defeat the paste guard. Successful reloads apply it immediately
	// to live interactive sessions.
	InputDelay string `toml:"input_delay"`
	// DefaultCols / DefaultRows are the terminal geometry used by daemon launch
	// paths (watchdog restart, orchestrator, scenarios, triggers, adoption) when
	// no client geometry is available. Values < 1 fall back to the defaults
	// (DefaultColsDefault / DefaultRowsDefault). Applies to sessions launched
	// after the change; an attaching client resizes to its real geometry.
	DefaultCols int `toml:"default_cols"`
	DefaultRows int `toml:"default_rows"`
	// MaxLogBytes caps the per-session scrollback log file. Values < 0 fall back
	// to the default (MaxLogBytesDefault); "0" means unlimited. Applies to sessions
	// launched (or adopted) after the change.
	MaxLogBytes int64 `toml:"max_log_bytes"`
}

// Lifecycle policy defaults. Each mirrors the fixed constant or bare literal
// that governed the behaviour before issue #1243 made the policy configurable.
const (
	ConvertSettleTimeoutDefault     = 5 * time.Second
	ConvertKillTimeoutDefault       = 3 * time.Second
	ConvertForceKillTimeoutDefault  = 3 * time.Second
	MassExitWindowDefault           = 2 * time.Second
	MassExitThresholdDefault        = 5
	ProcessKillGraceDefault         = 5 * time.Second
	AdoptedTimeoutDefault           = 24 * time.Hour
	AdoptedPollIntervalDefault      = time.Second
	ScrollbackHydrationBytesDefault = 128 * 1024
	InputDelayDefault               = 50 * time.Millisecond
	DefaultColsDefault              = 80
	DefaultRowsDefault              = 24
	MaxLogBytesDefault              = 100 * 1024 * 1024
)

// ConvertSettleTimeoutDuration returns the interrupt→settle wait, or the default
// when unset, unparseable, or non-positive.
func (l LifecycleConfig) ConvertSettleTimeoutDuration() time.Duration {
	return positiveDurationOrDefault(l.ConvertSettleTimeout, ConvertSettleTimeoutDefault)
}

// ConvertKillTimeoutDuration returns the SIGTERM-step wait, or the default when
// unset, unparseable, or non-positive.
func (l LifecycleConfig) ConvertKillTimeoutDuration() time.Duration {
	return positiveDurationOrDefault(l.ConvertKillTimeout, ConvertKillTimeoutDefault)
}

// ConvertForceKillTimeoutDuration returns the post-SIGKILL wait, or the default
// when unset, unparseable, or non-positive.
func (l LifecycleConfig) ConvertForceKillTimeoutDuration() time.Duration {
	return positiveDurationOrDefault(l.ConvertForceKillTimeout, ConvertForceKillTimeoutDefault)
}

// MassExitWindowDuration returns the mass-exit detection window, or the default
// when unset, unparseable, or non-positive.
func (l LifecycleConfig) MassExitWindowDuration() time.Duration {
	return positiveDurationOrDefault(l.MassExitWindow, MassExitWindowDefault)
}

// MassExitThresholdOrDefault returns the mass-exit exit-count threshold, or the
// default when < 1 (a zero threshold has no meaningful trigger).
func (l LifecycleConfig) MassExitThresholdOrDefault() int {
	if l.MassExitThreshold < 1 {
		return MassExitThresholdDefault
	}

	return l.MassExitThreshold
}

// ProcessKillGraceDuration returns the SIGTERM→SIGKILL grace. Unset uses the
// default; invalid/non-positive input also falls back defensively for callers
// that construct Config programmatically (Config.Validate rejects it on load).
func (l LifecycleConfig) ProcessKillGraceDuration() time.Duration {
	return positiveDurationOrDefault(l.ProcessKillGrace, ProcessKillGraceDefault)
}

// AdoptedTimeoutDuration returns the adopted-PTY safety deadline, or the default
// when unset, unparseable, or non-positive.
func (l LifecycleConfig) AdoptedTimeoutDuration() time.Duration {
	return positiveDurationOrDefault(l.AdoptedTimeout, AdoptedTimeoutDefault)
}

// AdoptedPollIntervalDuration returns the adopted-PTY poll cadence, or the
// default when unset, unparseable, or non-positive (a zero cadence would busy-loop).
func (l LifecycleConfig) AdoptedPollIntervalDuration() time.Duration {
	return positiveDurationOrDefault(l.AdoptedPollInterval, AdoptedPollIntervalDefault)
}

// ScrollbackHydrationBytesOrDefault returns the adopt-time hydration size. A
// value < 0 means "use the default"; "0" is honoured (disable hydration).
func (l LifecycleConfig) ScrollbackHydrationBytesOrDefault() int {
	if l.ScrollbackHydrationBytes < 0 {
		return ScrollbackHydrationBytesDefault
	}

	return l.ScrollbackHydrationBytes
}

// InputDelayDuration returns the type-then-submit pause, or the default when
// unset, unparseable, or non-positive (a zero pause would defeat the paste guard).
func (l LifecycleConfig) InputDelayDuration() time.Duration {
	return positiveDurationOrDefault(l.InputDelay, InputDelayDefault)
}

// DefaultColsOrDefault returns the default launch column count, or the default
// when < 1.
func (l LifecycleConfig) DefaultColsOrDefault() uint16 {
	if l.DefaultCols < 1 {
		return DefaultColsDefault
	}

	return uint16(l.DefaultCols) //nolint:gosec // G115: validated < 1 above; a config typo far exceeding uint16 is clamped at load by Validate.
}

// DefaultRowsOrDefault returns the default launch row count, or the default when < 1.
func (l LifecycleConfig) DefaultRowsOrDefault() uint16 {
	if l.DefaultRows < 1 {
		return DefaultRowsDefault
	}

	return uint16(l.DefaultRows) //nolint:gosec // G115: validated < 1 above; a config typo far exceeding uint16 is clamped at load by Validate.
}

// MaxLogBytesOrDefault returns the per-session log cap. A value < 0 means "use
// the default"; "0" is honoured (unlimited, as the scrollback writer treats a
// non-positive cap as no limit).
func (l LifecycleConfig) MaxLogBytesOrDefault() int64 {
	if l.MaxLogBytes < 0 {
		return MaxLogBytesDefault
	}

	return l.MaxLogBytes
}

// DetectionConfig is the [detection] block gathering the agent-detection and
// status-classification timing policy that was previously spread as fixed
// constants across the daemon and detector packages (issue #1241). Every field
// is optional: an empty or unparseable value falls back to the matching
// default constant, preserving the historical behaviour.
type DetectionConfig struct {
	// ScanInterval is how often the detection loop scans PTY scrollback to
	// classify low-risk agent status (active/ready). Empty uses the default
	// (DetectionScanIntervalDefault).
	ScanInterval string `toml:"scan_interval"`
	// FetchInterval is how often the detection loop refreshes remote tracking
	// refs (`git fetch`) so the diverged-from-base count stays fresh. Empty uses
	// the default (DetectionFetchIntervalDefault).
	FetchInterval string `toml:"fetch_interval"`
	// FetchTimeout bounds a single per-repo `git fetch` so a slow or hung remote
	// can't stall the fetch pass for other sessions. Empty uses the default
	// (DetectionFetchTimeoutDefault).
	FetchTimeout string `toml:"fetch_timeout"`
	// SilentThreshold is how long a running session's PTY may produce zero
	// output before the daemon warns it is silent (issue #1087). Empty uses the
	// default (DetectionSilentThresholdDefault).
	SilentThreshold string `toml:"silent_threshold"`
	// AdoptedGrace is the window after daemon-upgrade PTY adoption during which
	// an unknown detection result falls back to the previous status instead of
	// clobbering it. Empty uses the default (DetectionAdoptedGraceDefault).
	AdoptedGrace string `toml:"adopted_grace"`
	// RecentOutputWindow is the age below which recent PTY output alone implies
	// the agent is active when pattern matching is inconclusive. Empty uses the
	// default (DetectionRecentOutputWindowDefault).
	RecentOutputWindow string `toml:"recent_output_window"`
	// HookStartWindow is how long a SessionStart hook report stays authoritative
	// over PTY scraping. Empty uses the default (DetectionHookStartWindowDefault).
	HookStartWindow string `toml:"hook_start_window"`
	// HookActivityWindow is how long a tool-use hook report (UserPromptSubmit,
	// PreToolUse, PostToolUse) stays authoritative. Empty uses the default
	// (DetectionHookActivityWindowDefault).
	HookActivityWindow string `toml:"hook_activity_window"`
	// HookTerminalWindow is how long a terminal hook report (ready/approval:
	// Stop, idle_prompt, permission_prompt, PermissionRequest) stays
	// authoritative. Empty uses the default (DetectionHookTerminalWindowDefault).
	HookTerminalWindow string `toml:"hook_terminal_window"`
}

// Detection timing defaults. Each mirrors the fixed constant that governed the
// behaviour before issue #1241 made the policy configurable.
const (
	DetectionScanIntervalDefault       = 500 * time.Millisecond
	DetectionFetchIntervalDefault      = 5 * time.Minute
	DetectionFetchTimeoutDefault       = 30 * time.Second
	DetectionSilentThresholdDefault    = 20 * time.Second
	DetectionAdoptedGraceDefault       = 60 * time.Second
	DetectionRecentOutputWindowDefault = 3 * time.Second
	DetectionHookStartWindowDefault    = 5 * time.Second
	DetectionHookActivityWindowDefault = 30 * time.Second
	DetectionHookTerminalWindowDefault = 30 * time.Minute
)

// ScanIntervalDuration returns the PTY scan cadence, or the default when unset,
// unparseable, or non-positive (a zero/negative scan interval would busy-loop).
func (d DetectionConfig) ScanIntervalDuration() time.Duration {
	return positiveDurationOrDefault(d.ScanInterval, DetectionScanIntervalDefault)
}

// FetchIntervalDuration returns the remote-fetch cadence, or the default when
// unset, unparseable, or non-positive.
func (d DetectionConfig) FetchIntervalDuration() time.Duration {
	return positiveDurationOrDefault(d.FetchInterval, DetectionFetchIntervalDefault)
}

// FetchTimeoutDuration returns the per-repo fetch timeout, or the default when
// unset, unparseable, or non-positive.
func (d DetectionConfig) FetchTimeoutDuration() time.Duration {
	return positiveDurationOrDefault(d.FetchTimeout, DetectionFetchTimeoutDefault)
}

// SilentThresholdDuration returns the zero-output warning window. Empty or
// unparseable uses the default; a non-positive value keeps the default because
// a session is never past a "zero" threshold in a meaningful way.
func (d DetectionConfig) SilentThresholdDuration() time.Duration {
	return positiveDurationOrDefault(d.SilentThreshold, DetectionSilentThresholdDefault)
}

// AdoptedGraceDuration returns the adopted-session fallback window, or the
// default when unset or unparseable. A "0" disables the fallback.
func (d DetectionConfig) AdoptedGraceDuration() time.Duration {
	return durationOrDefault(d.AdoptedGrace, DetectionAdoptedGraceDefault)
}

// RecentOutputWindowDuration returns the recent-output-implies-active window,
// or the default when unset or unparseable. A "0" disables the fallback.
func (d DetectionConfig) RecentOutputWindowDuration() time.Duration {
	return durationOrDefault(d.RecentOutputWindow, DetectionRecentOutputWindowDefault)
}

// HookStartWindowDuration returns the SessionStart hook-authority window, or
// the default when unset, unparseable, or non-positive.
func (d DetectionConfig) HookStartWindowDuration() time.Duration {
	return positiveDurationOrDefault(d.HookStartWindow, DetectionHookStartWindowDefault)
}

// HookActivityWindowDuration returns the tool-use hook-authority window, or the
// default when unset, unparseable, or non-positive.
func (d DetectionConfig) HookActivityWindowDuration() time.Duration {
	return positiveDurationOrDefault(d.HookActivityWindow, DetectionHookActivityWindowDefault)
}

// HookTerminalWindowDuration returns the ready/approval hook-authority window,
// or the default when unset, unparseable, or non-positive.
func (d DetectionConfig) HookTerminalWindowDuration() time.Duration {
	return positiveDurationOrDefault(d.HookTerminalWindow, DetectionHookTerminalWindowDefault)
}

// positiveDurationOrDefault parses a duration string, returning def when it is
// empty, unparseable, or non-positive. Used for windows where a zero or
// negative value has no sensible meaning (cadences, timeouts, authority spans).
func positiveDurationOrDefault(s string, def time.Duration) time.Duration {
	if s == "" {
		return def
	}

	d, err := ParseDurationWithDays(s)
	if err != nil || d <= 0 {
		return def
	}

	return d
}

// TokenAccounting is the [token_accounting] block controlling the daemon's
// per-session token-usage loop, which periodically re-derives token totals from
// each supported session's on-disk transcript (issue #1244). The values were
// previously fixed constants in internal/daemon/tokens.go; every field is
// optional and falls back to the matching default constant, preserving the
// historical behaviour.
type TokenAccounting struct {
	// PollInterval is the cadence at which the loop re-derives per-session token
	// usage from transcripts. Empty, unparseable, or non-positive uses the
	// default (TokenPollIntervalDefault); a zero cadence would busy-loop.
	PollInterval string `toml:"poll_interval"`
	// StartupDelay is the short first-tick delay after a daemon (re)start so
	// `gr tokens` isn't blank for a full interval. Empty or unparseable uses the
	// default (TokenStartupDelayDefault); an explicit "0" polls immediately.
	StartupDelay string `toml:"startup_delay"`
	// BatchSize bounds how many sessions are (re)parsed per tick so a large fleet
	// with big transcripts can't stall the loop. Values < 1 fall back to the
	// default (TokenBatchSizeDefault).
	BatchSize int `toml:"batch_size"`
}

// Token-accounting defaults mirror the fixed constants that governed the loop
// before issue #1244 made the policy configurable.
const (
	TokenPollIntervalDefault = 30 * time.Second
	TokenStartupDelayDefault = 5 * time.Second
	TokenBatchSizeDefault    = 8
)

// PollIntervalDuration returns the token-poll cadence, or the default when
// unset, unparseable, or non-positive (a zero cadence would busy-loop).
func (t TokenAccounting) PollIntervalDuration() time.Duration {
	return positiveDurationOrDefault(t.PollInterval, TokenPollIntervalDefault)
}

// StartupDelayDuration returns the first-tick delay, or the default when unset
// or unparseable. An explicit "0" is honoured (poll immediately after start).
func (t TokenAccounting) StartupDelayDuration() time.Duration {
	return durationOrDefault(t.StartupDelay, TokenStartupDelayDefault)
}

// BatchSizeOrDefault returns the per-tick parse cap, clamped to a sensible
// minimum. A non-positive value means "use the default".
func (t TokenAccounting) BatchSizeOrDefault() int {
	if t.BatchSize < 1 {
		return TokenBatchSizeDefault
	}

	return t.BatchSize
}

// ResourceMonitor is the [resource_monitor] block controlling the daemon's
// per-session resource-sampling loop, which snapshots each live session's
// process-group RSS/CPU/FD usage (issue #1244). The values were previously
// fixed constants in internal/daemon/resource_monitor.go; every field is
// optional and falls back to the matching default constant.
type ResourceMonitor struct {
	// SampleInterval is the cadence at which each session's process group is
	// snapshotted (and the per-session spacing that keeps a launch-burst kick
	// from replacing an established session's history). Empty, unparseable, or
	// non-positive uses the default (ResourceSampleIntervalDefault).
	SampleInterval string `toml:"sample_interval"`
	// SampleHistory is how many recent samples are retained per session (the
	// window shown in an abnormal-exit report). Values < 1 fall back to the
	// default (ResourceSampleHistoryDefault).
	SampleHistory int `toml:"sample_history"`
}

// Resource-monitor defaults mirror the fixed constants that governed the loop
// before issue #1244 made the policy configurable.
const (
	ResourceSampleIntervalDefault = 30 * time.Second
	ResourceSampleHistoryDefault  = 5
)

// SampleIntervalDuration returns the sampling cadence, or the default when
// unset, unparseable, or non-positive (a zero cadence would busy-loop).
func (r ResourceMonitor) SampleIntervalDuration() time.Duration {
	return positiveDurationOrDefault(r.SampleInterval, ResourceSampleIntervalDefault)
}

// SampleHistoryOrDefault returns the retained-sample count, clamped to a
// sensible minimum. A non-positive value means "use the default".
func (r ResourceMonitor) SampleHistoryOrDefault() int {
	if r.SampleHistory < 1 {
		return ResourceSampleHistoryDefault
	}

	return r.SampleHistory
}

// LimitsConfig is the [limits] block gathering the user-visible output, log,
// wait, and display truncation caps that were previously duplicated as
// unrelated Go constants and literals across the daemon, CLI, and MCP manager
// (issue #1252). Unifying them means changing one place updates every surface.
// Every field is optional: a value < 1 falls back to the matching default
// constant, preserving the historical behaviour. Units are stated in each field
// name (lines, bytes, runes) so a single number is unambiguous.
type LimitsConfig struct {
	// LogLines is the default number of trailing output lines shown when a
	// `lines`/`-n` count is not given: `gr logs`, `gr mcp logs`, the scrollback
	// replayed to a client on attach, and the MCP log reader all share it. Values
	// < 1 fall back to the default (LimitsLogLinesDefault).
	LogLines int `toml:"log_lines"`
	// WaitScanLines bounds how much existing scrollback `gr wait --contains`
	// scans for an already-present match before it starts following live output.
	// Values < 1 fall back to the default (LimitsWaitScanLinesDefault).
	WaitScanLines int `toml:"wait_scan_lines"`
	// WaitBufferBytes bounds the retained partial line in the live `gr wait`
	// matcher so a long stream without a newline can't grow the buffer without
	// limit. Values < 1 fall back to the default (LimitsWaitBufferBytesDefault).
	WaitBufferBytes int `toml:"wait_buffer_bytes"`
	// MCPLogReadBytes bounds how many trailing bytes of an MCP server log file
	// are read before splitting into lines, keeping a huge log from being loaded
	// whole. Values < 1 fall back to the default (LimitsMCPLogReadBytesDefault).
	MCPLogReadBytes int `toml:"mcp_log_read_bytes"`
	// ApprovalDisplayBytes caps the tool input shown in the approval overlay and
	// broadcast to attached clients (the full input is still what backends
	// evaluate). Values < 1 fall back to the default
	// (LimitsApprovalDisplayBytesDefault).
	ApprovalDisplayBytes int `toml:"approval_display_bytes"`
	// LastMessageRunes bounds the agent's final Stop message the status hook
	// forwards to the daemon, so a large final output never becomes an unbounded
	// control frame. Counted in runes (never splits a multi-byte character).
	// Values < 1 fall back to the default (LimitsLastMessageRunesDefault).
	LastMessageRunes int `toml:"last_message_runes"`
	// InboxPreviewBytes bounds the unread-inbox preview injected into a session's
	// SessionStart hook context. Values < 1 fall back to the default
	// (LimitsInboxPreviewBytesDefault).
	InboxPreviewBytes int `toml:"inbox_preview_bytes"`
}

// Limits defaults mirror the fixed constants and literals that governed each
// surface before issue #1252 unified them.
const (
	LimitsLogLinesDefault             = 300
	LimitsWaitScanLinesDefault        = 500
	LimitsWaitBufferBytesDefault      = 64 * 1024
	LimitsMCPLogReadBytesDefault      = 1 << 20 // 1 MiB
	LimitsApprovalDisplayBytesDefault = 500
	LimitsLastMessageRunesDefault     = 2000
	LimitsInboxPreviewBytesDefault    = 1000
)

// LogLinesOrDefault returns the default log-tail line count, clamped to a
// sensible minimum. A value < 1 means "use the default".
func (l LimitsConfig) LogLinesOrDefault() int {
	if l.LogLines < 1 {
		return LimitsLogLinesDefault
	}

	return l.LogLines
}

// WaitScanLinesOrDefault returns the `gr wait` scrollback-scan line count,
// clamped to a sensible minimum. A value < 1 means "use the default".
func (l LimitsConfig) WaitScanLinesOrDefault() int {
	if l.WaitScanLines < 1 {
		return LimitsWaitScanLinesDefault
	}

	return l.WaitScanLines
}

// WaitBufferBytesOrDefault returns the `gr wait` matcher partial-line cap,
// clamped to a sensible minimum. A value < 1 means "use the default".
func (l LimitsConfig) WaitBufferBytesOrDefault() int {
	if l.WaitBufferBytes < 1 {
		return LimitsWaitBufferBytesDefault
	}

	return l.WaitBufferBytes
}

// MCPLogReadBytesOrDefault returns the MCP log read cap, clamped to a sensible
// minimum. A value < 1 means "use the default".
func (l LimitsConfig) MCPLogReadBytesOrDefault() int {
	if l.MCPLogReadBytes < 1 {
		return LimitsMCPLogReadBytesDefault
	}

	return l.MCPLogReadBytes
}

// ApprovalDisplayBytesOrDefault returns the approval-overlay display cap,
// clamped to a sensible minimum. A value < 1 means "use the default".
func (l LimitsConfig) ApprovalDisplayBytesOrDefault() int {
	if l.ApprovalDisplayBytes < 1 {
		return LimitsApprovalDisplayBytesDefault
	}

	return l.ApprovalDisplayBytes
}

// LastMessageRunesOrDefault returns the hook last-message rune cap, clamped to
// a sensible minimum. A value < 1 means "use the default".
func (l LimitsConfig) LastMessageRunesOrDefault() int {
	if l.LastMessageRunes < 1 {
		return LimitsLastMessageRunesDefault
	}

	return l.LastMessageRunes
}

// InboxPreviewBytesOrDefault returns the inbox-preview byte cap, clamped to a
// sensible minimum. A value < 1 means "use the default".
func (l LimitsConfig) InboxPreviewBytesOrDefault() int {
	if l.InboxPreviewBytes < 1 {
		return LimitsInboxPreviewBytesDefault
	}

	return l.InboxPreviewBytes
}

// TerminalConfig is the [terminal] block: user-tunable interactive-TUI
// presentation preferences that were previously fixed literals in the client
// (issue #1254) — how often the picker/dashboard/status bar refresh, and how
// wide a `gr status` summary may grow in the picker before truncation.
//
// Session-lifecycle presentation (the fallback terminal geometry and the
// per-session scrollback cap) is deliberately NOT here: it lives in the
// [lifecycle] block (issue #1243, default_cols/default_rows/max_log_bytes),
// which owns the daemon's PTY seed. Layout invariants (the picker's column
// arithmetic, wrap widths, the minimum name column, and the GUI's frame rate)
// are also excluded — they must match render logic and stay as documented
// constants. Every field is optional and falls back to its default constant.
type TerminalConfig struct {
	// RefreshInterval is the cadence at which the session picker, the dashboard,
	// and an attached status bar re-poll the daemon for fresh session state.
	// Empty, unparseable, or non-positive uses the default
	// (TerminalRefreshIntervalDefault); a zero cadence would busy-loop.
	RefreshInterval string `toml:"refresh_interval"`
	// SummaryWidth is the maximum visible width (in cells) of a `gr status`
	// summary shown against a session in the picker before it is truncated with
	// an ellipsis. Values < 1 fall back to the default (TerminalSummaryWidth).
	SummaryWidth int `toml:"summary_width"`
}

// Terminal presentation defaults mirror the fixed literals that governed the
// behaviour before issue #1254 made the policy configurable.
const (
	TerminalRefreshIntervalDefault = 2 * time.Second
	TerminalSummaryWidth           = 40
)

// RefreshIntervalDuration returns the TUI refresh cadence, or the default when
// unset, unparseable, or non-positive (a zero cadence would busy-loop).
func (t TerminalConfig) RefreshIntervalDuration() time.Duration {
	return positiveDurationOrDefault(t.RefreshInterval, TerminalRefreshIntervalDefault)
}

// SummaryWidthValue returns the picker summary truncation width, or the default
// when the configured value is non-positive.
func (t TerminalConfig) SummaryWidthValue() int {
	if t.SummaryWidth < 1 {
		return TerminalSummaryWidth
	}

	return t.SummaryWidth
}

type GitPullConfig struct {
	Enabled  bool   `toml:"enabled"`
	Interval string `toml:"interval"`
}

func (g GitPullConfig) IntervalDuration() time.Duration {
	if g.Interval == "" {
		return time.Hour
	}

	d, err := ParseDurationWithDays(g.Interval)
	if err != nil {
		return time.Hour
	}

	return d
}

// PRWatchConfig controls the PR & CI awareness loop, which resolves each
// session's GitHub PR via the gh CLI, polls its CI checks and review comments,
// and notifies the owning session's inbox on meaningful transitions.
//
// Every notify_* sub-option defaults on: enabling pr_watch is meant to be a
// single switch (enabled = true) that turns on all notifications, and users
// selectively disable the classes they don't want. The classes are still gated
// separately because they carry different authority — a CI failure is a machine
// verdict (safe to act on), while a review comment or decision is human intent
// that may not be actionable — so each can be turned off independently.
//
// Comments come in two distinct kinds, each with its own gate:
//   - NotifyReviewComments covers inline code-review comments (the
//     pulls/{n}/comments surface) — feedback anchored to a file and line.
//   - NotifyPRComments covers regular conversation comments on the PR thread
//     (the issues/{n}/comments surface) — issue-style comments not tied to a
//     line of code.
//
// They are separate signals: a reviewer leaving inline nits and someone
// dropping a "ship it" on the conversation thread differ, and a user may want
// one without the other.
//
// For backward compatibility, notify_pr_comments used to be folded into
// notify_review_comments; see applyPRWatchCommentCompat, which keeps an older
// config that only set notify_review_comments delivering conversation comments.
type PRWatchConfig struct {
	Enabled               bool   `toml:"enabled"`
	NotifyCIFailures      bool   `toml:"notify_ci_failures"`
	NotifyMergeConflicts  bool   `toml:"notify_merge_conflicts"`
	NotifyReviewComments  bool   `toml:"notify_review_comments"`
	NotifyPRComments      bool   `toml:"notify_pr_comments"`
	NotifyReviewDecisions bool   `toml:"notify_review_decisions"`
	NotifyPRLifecycle     bool   `toml:"notify_pr_lifecycle"`
	NotifyCIRecovery      bool   `toml:"notify_ci_recovery"`
	PollPending           string `toml:"poll_pending"`
	PollTerminal          string `toml:"poll_terminal"`
	PollMerged            string `toml:"poll_merged"`
	MaxNotificationsPerPR int    `toml:"max_notifications_per_pr"`
	Debounce              string `toml:"debounce"`
	// CommentAuthorAllowlist trusts individual comment authors by login,
	// case-insensitively and matched against the full "<name>[bot]" string. It is
	// the ONLY way to trust a bot or GitHub App (their author_association is
	// unreliable — a bot can carry NONE or CONTRIBUTOR), and also covers named
	// humans. Defaults empty; discovery is via the orchestrator trust prompt.
	CommentAuthorAllowlist []string `toml:"comment_author_allowlist"`
	// TrustedAuthorAssociations is the set of GitHub author_association values
	// treated as trusted. Defaults to OWNER/MEMBER/COLLABORATOR when unset (the
	// "has write access to, or is a member of the org that owns, the repo" tier);
	// CONTRIBUTOR is deliberately excluded. Values are normalised to upper-case
	// via TrustedAssociationSet.
	TrustedAuthorAssociations []string `toml:"trusted_author_associations"`
	// NotifyUntrustedAuthors, when true, sends a one-time metadata-only message to
	// the orchestrator the first time a comment from a not-yet-trusted author is
	// seen, so the human can decide whether to allowlist them. It NEVER carries
	// the untrusted comment body. False disables the prompt entirely (silent drop,
	// still logged).
	NotifyUntrustedAuthors bool `toml:"notify_untrusted_authors"`
	// Advanced holds the low-level watcher-tuning knobs (loop cadence, batch size,
	// caches, rate limits, ref-watch timing, gh timeout). Every field is optional
	// and falls back to a sensible default via the accessors below, so a config
	// that omits [pr_watch.advanced] entirely behaves exactly as before. Expose
	// these only for operators who need to trade off load, latency, retention, and
	// prompt-injection surface — the defaults suit ordinary use.
	Advanced PRWatchAdvancedConfig `toml:"advanced"`
}

// PRWatchAdvancedConfig carries the advanced tuning for the PR/CI watch loop and
// its git-ref accelerator. These were formerly hard-coded policy literals in the
// daemon; they are surfaced here so an operator can tune load, latency, retention,
// and the untrusted-author prompt-injection surface without a rebuild. Every field
// is optional: an unset (zero) value resolves to the documented default through the
// PRWatchConfig accessors, so leaving [pr_watch.advanced] out is a no-op.
type PRWatchAdvancedConfig struct {
	// BaseTick is the base poll-loop cadence (per-session gating paces the actual
	// gh calls below it). Default 15s. Applied when the watch loop starts.
	BaseTick string `toml:"base_tick"`
	// BatchSize caps how many sessions are polled per tick, bounding gh load on a
	// large fleet. Default 3.
	BatchSize int `toml:"batch_size"`
	// NoPRNegativeCache is how long a branch with no PR is left before re-resolving
	// on the ordinary timer. Default 5m.
	NoPRNegativeCache string `toml:"no_pr_negative_cache"`
	// CommentBodyMaxBytes truncates each delivered PR-comment body to this many
	// bytes, bounding notification size. Default 1024.
	CommentBodyMaxBytes int `toml:"comment_body_max_bytes"`
	// NotificationRateLimit / NotificationRateWindow are the per-session rolling
	// anti-thrash backstop: at most this many notifications per window to one
	// session. Defaults 5 per 30m.
	NotificationRateLimit  int    `toml:"notification_rate_limit"`
	NotificationRateWindow string `toml:"notification_rate_window"`
	// UntrustedAuthorPromptRate / UntrustedAuthorPromptWindow bound the untrusted
	// comment-author trust prompt to the orchestrator (a security surface — a busy
	// public PR churning drive-by commenters must not flood it). Defaults 5 per 30m.
	UntrustedAuthorPromptRate   int    `toml:"untrusted_author_prompt_rate"`
	UntrustedAuthorPromptWindow string `toml:"untrusted_author_prompt_window"`
	// MaxPromptedAuthors bounds the persisted set of already-surfaced untrusted
	// authors so it can't grow without limit. Default 5000.
	MaxPromptedAuthors int `toml:"max_prompted_authors"`
	// KickCooldown is the minimum interval between git-ref-triggered immediate polls
	// of one session (belt-and-braces over the ref-watch debounce). Default 3s.
	KickCooldown string `toml:"kick_cooldown"`
	// KickChannelSize is the buffered kick-channel capacity; a full channel drops
	// the (best-effort) kick. Default 64. Applied when the watch state is built.
	KickChannelSize int `toml:"kick_channel_size"`
	// KickedNoPRBackoff is the short re-poll delay after a kicked poll finds no PR
	// yet (a push is usually moments before `gh pr create`), instead of parking on
	// the full negative cache. Default 20s.
	KickedNoPRBackoff string `toml:"kicked_no_pr_backoff"`
	// RefReconcileInterval is how often the git-ref watcher set is reconciled
	// against live sessions. Default 2s. Applied when the ref-watch loop starts.
	RefReconcileInterval string `toml:"ref_reconcile_interval"`
	// RefDebounce coalesces the burst of ref/reflog writes one push/commit/checkout
	// produces into a single kick. Default 750ms.
	RefDebounce string `toml:"ref_debounce"`
	// GHTimeout is the per-command timeout for the daemon's `gh` invocations, so a
	// hung gh can never stall the loop. Default 5s.
	GHTimeout string `toml:"gh_timeout"`
}

// DefaultTrustedAssociations is the trusted author_association set used when
// pr_watch.trusted_author_associations is unset. It is the "has write access to,
// or is a member of the org that owns, the repo" tier; CONTRIBUTOR is excluded
// deliberately (on a public repo it means only "merged a commit once", and bots
// can carry it — see the author-trust design doc).
var DefaultTrustedAssociations = []string{"OWNER", "MEMBER", "COLLABORATOR"}

// TrustedAssociationSet returns the resolved set of trusted author_association
// values as an upper-cased lookup set. A configured list is normalised to
// upper-case (GitHub returns the enum upper-cased, but config is hand-written)
// and empty/whitespace entries are dropped.
//
// The nil vs present-but-empty distinction is load-bearing and fails CLOSED
// (issue #1039):
//
//   - A NIL slice means "unset" (the Go zero value, or a config built without
//     defaults) and falls back to DefaultTrustedAssociations. Load() seeds the
//     field from default_config.toml, so an unset key in a real config resolves
//     to the default three; nil here covers direct struct construction.
//   - A PRESENT-but-empty slice (trusted_author_associations = []) is an
//     explicit "trust no association" — allowlist-only mode — and is honoured
//     as an empty set. go-toml/v2 decodes `= []` to a non-nil empty slice, so it
//     is distinguishable from an absent key, and we must NOT silently widen it
//     back to the default (that would fail open on an operator asking to lock
//     the gate down).
func (p PRWatchConfig) TrustedAssociationSet() map[string]bool {
	if p.TrustedAuthorAssociations == nil {
		set := make(map[string]bool, len(DefaultTrustedAssociations))
		for _, a := range DefaultTrustedAssociations {
			set[a] = true
		}

		return set
	}

	set := make(map[string]bool, len(p.TrustedAuthorAssociations))

	for _, a := range p.TrustedAuthorAssociations {
		a = strings.ToUpper(strings.TrimSpace(a))
		if a != "" {
			set[a] = true
		}
	}

	return set
}

func parseDurationOr(s string, fallback time.Duration) time.Duration {
	if s == "" {
		return fallback
	}

	d, err := ParseDurationWithDays(s)
	if err != nil {
		return fallback
	}

	return d
}

// PollPendingDuration is the poll interval while a PR has pending/in-progress checks.
func (p PRWatchConfig) PollPendingDuration() time.Duration {
	return parseDurationOr(p.PollPending, 30*time.Second)
}

// PollTerminalDuration is the poll interval once all checks are terminal (PR still open).
func (p PRWatchConfig) PollTerminalDuration() time.Duration {
	return parseDurationOr(p.PollTerminal, 5*time.Minute)
}

// PollMergedDuration is the sweep interval for merged/closed PRs.
func (p PRWatchConfig) PollMergedDuration() time.Duration {
	return parseDurationOr(p.PollMerged, 15*time.Minute)
}

// DebounceDuration is the minimum cooldown between notifications to one session.
func (p PRWatchConfig) DebounceDuration() time.Duration {
	return parseDurationOr(p.Debounce, 2*time.Minute)
}

// MaxNotifications returns the per-head-SHA notification cap, defaulting to 10.
func (p PRWatchConfig) MaxNotifications() int {
	if p.MaxNotificationsPerPR <= 0 {
		return 10
	}

	return p.MaxNotificationsPerPR
}

// The accessors below resolve the [pr_watch.advanced] tuning knobs, each falling
// back to the daemon's historical default when unset, non-positive, or invalid.
// The embedded default_config.toml materializes the same values so config
// inspection and runtime behavior cannot drift; these fallbacks protect callers
// that construct Config values directly.

// BaseTickDuration is the base poll-loop cadence. Default 15s. A zero or
// negative value falls back to the default so the poll loop can never construct
// a non-positive time.NewTicker (issue #1285).
func (p PRWatchConfig) BaseTickDuration() time.Duration {
	return positiveDurationOrDefault(p.Advanced.BaseTick, 15*time.Second)
}

// BatchSize caps sessions polled per tick. Default 3.
func (p PRWatchConfig) BatchSize() int {
	if p.Advanced.BatchSize <= 0 {
		return 3
	}

	return p.Advanced.BatchSize
}

// NoPRNegativeCacheDuration is the no-PR re-resolve interval. Default 5m.
func (p PRWatchConfig) NoPRNegativeCacheDuration() time.Duration {
	return positiveDurationOrDefault(p.Advanced.NoPRNegativeCache, 5*time.Minute)
}

// CommentBodyMaxBytes is the per-comment body truncation cap. Default 1024.
func (p PRWatchConfig) CommentBodyMaxBytes() int {
	if p.Advanced.CommentBodyMaxBytes <= 0 {
		return 1024
	}

	return p.Advanced.CommentBodyMaxBytes
}

// NotificationRateLimit is the per-session rolling notification cap. Default 5.
func (p PRWatchConfig) NotificationRateLimit() int {
	if p.Advanced.NotificationRateLimit <= 0 {
		return 5
	}

	return p.Advanced.NotificationRateLimit
}

// NotificationRateWindowDuration is the per-session rate-limit window. Default 30m.
func (p PRWatchConfig) NotificationRateWindowDuration() time.Duration {
	return positiveDurationOrDefault(p.Advanced.NotificationRateWindow, 30*time.Minute)
}

// UntrustedAuthorPromptRate caps untrusted-author trust prompts per window. Default 5.
func (p PRWatchConfig) UntrustedAuthorPromptRate() int {
	if p.Advanced.UntrustedAuthorPromptRate <= 0 {
		return 5
	}

	return p.Advanced.UntrustedAuthorPromptRate
}

// UntrustedAuthorPromptWindowDuration is the trust-prompt rate window. Default 30m.
func (p PRWatchConfig) UntrustedAuthorPromptWindowDuration() time.Duration {
	return positiveDurationOrDefault(p.Advanced.UntrustedAuthorPromptWindow, 30*time.Minute)
}

// MaxPromptedAuthors bounds the persisted surfaced-authors set. Default 5000.
func (p PRWatchConfig) MaxPromptedAuthors() int {
	if p.Advanced.MaxPromptedAuthors <= 0 {
		return 5000
	}

	return p.Advanced.MaxPromptedAuthors
}

// KickCooldownDuration is the min interval between git-ref-triggered polls of one
// session. Default 3s.
func (p PRWatchConfig) KickCooldownDuration() time.Duration {
	return positiveDurationOrDefault(p.Advanced.KickCooldown, 3*time.Second)
}

// KickChannelSize is the buffered kick-channel capacity. Default 64.
func (p PRWatchConfig) KickChannelSize() int {
	if p.Advanced.KickChannelSize <= 0 {
		return 64
	}

	return p.Advanced.KickChannelSize
}

// KickedNoPRBackoffDuration is the short re-poll delay after a kicked no-PR miss.
// Default 20s.
func (p PRWatchConfig) KickedNoPRBackoffDuration() time.Duration {
	return positiveDurationOrDefault(p.Advanced.KickedNoPRBackoff, 20*time.Second)
}

// RefReconcileIntervalDuration is the git-ref watcher reconcile cadence. Default
// 2s. A zero or negative value falls back to the default so the reconcile loop
// can never construct a non-positive time.NewTicker (issue #1285).
func (p PRWatchConfig) RefReconcileIntervalDuration() time.Duration {
	return positiveDurationOrDefault(p.Advanced.RefReconcileInterval, 2*time.Second)
}

// RefDebounceDuration coalesces a burst of ref writes into one kick. Default 750ms.
func (p PRWatchConfig) RefDebounceDuration() time.Duration {
	return positiveDurationOrDefault(p.Advanced.RefDebounce, 750*time.Millisecond)
}

// GHTimeoutDuration is the per-`gh`-command timeout. Default 5s.
func (p PRWatchConfig) GHTimeoutDuration() time.Duration {
	return positiveDurationOrDefault(p.Advanced.GHTimeout, 5*time.Second)
}

type RepoConfig struct {
	Path            string   `toml:"path"`
	AllowConcurrent bool     `toml:"allow_concurrent"`
	Singleton       bool     `toml:"singleton"`
	Includes        []string `toml:"includes"`
}

type StatusBar struct {
	Enabled  bool   `toml:"enabled"`
	Position string `toml:"position"`
}

// Messages is the [messages] block. It governs the message-log subsystem: the
// cleanup retention (MaxAge/MaxPerStream) plus the operational limits made
// configurable by issue #1249 — conversation paging bounds, the jail listing
// cap, the pub/sub subscriber buffer, and the SQLite busy/operation timeout.
//
// SubscriberBuffer and BusyTimeout are fixed at store-open time, so a change to
// either takes effect only on daemon restart. The conversation paging bounds
// and the jail cap are read per-request, so they apply on reload.
type Messages struct {
	MaxAge       string `toml:"max_age"`
	MaxPerStream int    `toml:"max_per_stream"`
	// ConversationPageSize is the page size applied when a msg_conversation
	// request supplies a non-positive limit. Values < 1 fall back to the default
	// (MessagesConversationPageSizeDefault). Reloadable.
	ConversationPageSize int `toml:"conversation_page_size"`
	// ConversationMaxLimit is the hard cap on how many messages a single
	// msg_conversation request may sort, bounding a local perf/DoS footgun. Values
	// < 1 fall back to the default (MessagesConversationMaxLimitDefault); a value
	// above MessagesConversationMaxLimitCeiling is rejected at load. Reloadable.
	ConversationMaxLimit int `toml:"conversation_max_limit"`
	// JailListLimit caps how many quarantined comments a jail listing returns
	// (newest first), so the query can't force an unbounded allocation. Values < 1
	// fall back to the default (MessagesJailListLimitDefault); a value above
	// MessagesJailListLimitCeiling is rejected at load. Reloadable.
	JailListLimit int `toml:"jail_list_limit"`
	// SubscriberBuffer is the per-subscriber pub/sub channel capacity. A slow
	// reader that fills its buffer drops further messages until it drains (the
	// stored log stays authoritative), so this is a load-tuning knob for
	// installations with bursty fan-out. Values < 1 fall back to the default
	// (MessagesSubscriberBufferDefault); a value above
	// MessagesSubscriberBufferCeiling is rejected at load. Restart-only.
	SubscriberBuffer int `toml:"subscriber_buffer"`
	// BusyTimeout is the SQLite busy_timeout for the messages database — how long
	// a contended operation waits for the lock before erroring. It is graith's
	// database operation deadline. Empty, unparseable, or non-positive uses the
	// default (MessagesBusyTimeoutDefault); a value above MessagesBusyTimeoutCeiling
	// is rejected at load. Restart-only.
	BusyTimeout string `toml:"busy_timeout"`
}

// Messages operational-limit defaults, mirroring the fixed literals that
// governed the message log before issue #1249 made them configurable.
const (
	MessagesConversationPageSizeDefault = 500
	MessagesConversationMaxLimitDefault = 2000
	MessagesJailListLimitDefault        = 2000
	MessagesSubscriberBufferDefault     = 64
	MessagesBusyTimeoutDefault          = 5 * time.Second
)

// Hard safety ceilings for the [messages] operational limits. Config may tune a
// value up to (but not past) its ceiling; Validate rejects anything above it so
// a typo can't request an absurd allocation, unbounded sort, or an effectively
// infinite lock wait.
const (
	MessagesConversationMaxLimitCeiling = 100_000
	MessagesJailListLimitCeiling        = 100_000
	MessagesSubscriberBufferCeiling     = 65_536
	MessagesBusyTimeoutCeiling          = 5 * time.Minute
)

// DefaultDeleteRetention is the soft-delete retention window used when
// [delete] retention is unset.
const DefaultDeleteRetention = 24 * time.Hour

// Purge-loop scheduling defaults, used when the matching [delete] key is unset.
// The window they sweep is measured in hours, so the cadence is deliberately
// coarse: purging a little late is harmless, and only the frozen ExpiresAt (not
// this timing) decides whether a session is recoverable.
const (
	// DefaultPurgeStartupDelay is how long after startup the first purge sweep
	// runs, catching windows that expired while the daemon was down without
	// racing the rest of daemon initialisation.
	DefaultPurgeStartupDelay = 30 * time.Second
	// DefaultPurgeInterval is how often the purge sweep runs after the first.
	DefaultPurgeInterval = 10 * time.Minute
)

// Delete configures the soft-delete behaviour of `gr delete`. When retention
// is a positive duration, `gr delete` marks a session deleted and keeps its
// worktree/state for the window; the daemon purges it after the window
// elapses. A retention of "0" disables soft delete: `gr delete` is then
// rejected (with a message pointing at `gr purge`), since delete must never
// destroy — `gr purge` remains the way to hard-delete immediately.
//
// PurgeStartupDelay and PurgeInterval tune ONLY the sweep cadence, never
// whether a session is recoverable: a session is purged only once its frozen
// ExpiresAt (DeletedAt + retention) has passed, so no timing value can turn
// soft delete into an immediate hard delete.
type Delete struct {
	Retention         string `toml:"retention"`
	PurgeStartupDelay string `toml:"purge_startup_delay"`
	PurgeInterval     string `toml:"purge_interval"`
}

// RetentionDuration resolves the configured soft-delete retention window. An
// unset value defaults to DefaultDeleteRetention (24h); "0" (or any zero
// duration) disables soft delete. An unparseable value falls back to the
// default so a typo never silently turns off recovery.
func (d Delete) RetentionDuration() time.Duration {
	if d.Retention == "" {
		return DefaultDeleteRetention
	}

	parsed, err := ParseDurationWithDays(d.Retention)
	if err != nil {
		return DefaultDeleteRetention
	}

	return parsed
}

// PurgeStartupDelayDuration resolves the delay before the first purge sweep.
// Unset, unparseable, or non-positive values fall back to the default so a typo
// never silently changes startup behaviour (Validate rejects a bad value at
// load; this is the runtime fail-safe).
func (d Delete) PurgeStartupDelayDuration() time.Duration {
	if d.PurgeStartupDelay == "" {
		return DefaultPurgeStartupDelay
	}

	parsed, err := ParseDurationWithDays(d.PurgeStartupDelay)
	if err != nil || parsed <= 0 {
		return DefaultPurgeStartupDelay
	}

	return parsed
}

// PurgeIntervalDuration resolves the steady-state interval between purge
// sweeps. Unset, unparseable, or non-positive values fall back to the default.
func (d Delete) PurgeIntervalDuration() time.Duration {
	if d.PurgeInterval == "" {
		return DefaultPurgeInterval
	}

	parsed, err := ParseDurationWithDays(d.PurgeInterval)
	if err != nil || parsed <= 0 {
		return DefaultPurgeInterval
	}

	return parsed
}

// DefaultGCOrphanMinAge is the minimum age an orphaned worktree/scratch
// directory must have before GC will remove it, used when [gc] orphan_min_age
// is unset. Directories are created early in a session's lifecycle (during
// StatusCreating, before the session is committed to state), so a young
// directory may belong to an in-flight create that GC would otherwise race and
// destroy — the floor is a safety margin, not a cosmetic delay.
const DefaultGCOrphanMinAge = 5 * time.Minute

// GCConfig is the [gc] block. It tunes orphan garbage collection — the sweep
// (via `gr gc`) that reclaims worktree and scratch directories left behind by
// sessions no longer in state.
type GCConfig struct {
	OrphanMinAge string `toml:"orphan_min_age"`
}

// OrphanMinAgeDuration resolves the orphan minimum age. Unset or unparseable
// falls back to the default; a negative value also falls back (a bad value must
// not widen GC to newly-created directories). "0" is honoured: an operator who
// explicitly opts out of the age floor gets immediate GC eligibility.
func (g GCConfig) OrphanMinAgeDuration() time.Duration {
	if g.OrphanMinAge == "" {
		return DefaultGCOrphanMinAge
	}

	parsed, err := ParseDurationWithDays(g.OrphanMinAge)
	if err != nil || parsed < 0 {
		return DefaultGCOrphanMinAge
	}

	return parsed
}

func (m Messages) MaxAgeDuration() time.Duration {
	if m.MaxAge == "" {
		return 0
	}

	d, _ := ParseDurationWithDays(m.MaxAge)

	return d
}

// ConversationMaxLimitOrDefault returns the hard cap on a single conversation
// sort. A non-positive value means "use the default".
func (m Messages) ConversationMaxLimitOrDefault() int {
	if m.ConversationMaxLimit < 1 {
		return MessagesConversationMaxLimitDefault
	}

	return m.ConversationMaxLimit
}

// ConversationPageSizeOrDefault returns the default conversation page size (used
// when a request supplies a non-positive limit). A non-positive configured value
// means "use the default"; the result is additionally clamped to the effective
// max limit so a misconfigured page size can never exceed the hard cap.
func (m Messages) ConversationPageSizeOrDefault() int {
	page := m.ConversationPageSize
	if page < 1 {
		page = MessagesConversationPageSizeDefault
	}

	if maxLimit := m.ConversationMaxLimitOrDefault(); page > maxLimit {
		page = maxLimit
	}

	return page
}

// ClampConversationLimit normalizes a client-supplied conversation limit: a
// non-positive limit becomes the configured page size, and any limit above the
// configured maximum is capped at it.
func (m Messages) ClampConversationLimit(limit int) int {
	if limit <= 0 {
		return m.ConversationPageSizeOrDefault()
	}

	if maxLimit := m.ConversationMaxLimitOrDefault(); limit > maxLimit {
		return maxLimit
	}

	return limit
}

// JailListLimitOrDefault returns the jail listing row cap. A non-positive value
// means "use the default".
func (m Messages) JailListLimitOrDefault() int {
	if m.JailListLimit < 1 {
		return MessagesJailListLimitDefault
	}

	return m.JailListLimit
}

// SubscriberBufferOrDefault returns the per-subscriber channel capacity. A
// non-positive value means "use the default".
func (m Messages) SubscriberBufferOrDefault() int {
	if m.SubscriberBuffer < 1 {
		return MessagesSubscriberBufferDefault
	}

	return m.SubscriberBuffer
}

// BusyTimeoutDuration returns the messages-database SQLite busy_timeout, or the
// default when unset, unparseable, or non-positive (a zero/negative timeout
// would make a contended write fail immediately instead of waiting).
func (m Messages) BusyTimeoutDuration() time.Duration {
	return positiveDurationOrDefault(m.BusyTimeout, MessagesBusyTimeoutDefault)
}

// Emit-events modes for the task-list subsystem.
const (
	TodoEmitScenario = "scenario" // emit only for scenario-scoped lists (default)
	TodoEmitAll      = "all"      // emit for every scope
	TodoEmitOff      = "off"      // never emit
)

// DefaultTodoClaimLease is the default claim-lease window: an in-progress item
// whose owner has made no progress for this long is auto-reopened. 0 disables.
const DefaultTodoClaimLease = 30 * time.Minute

// TodoConfig is the [todo] block. It governs the first-class todo subsystem
// (issue #591): event emission on state change, the claim lease that reclaims
// stranded in-progress items, and the retention window that sweeps done items.
type TodoConfig struct {
	EmitEvents string `toml:"emit_events"` // "scenario" (default) | "all" | "off"
	ClaimLease string `toml:"claim_lease"` // Go duration; "" = 30m default; "0" disables
	Retention  string `toml:"retention"`   // Go duration; "" or "0" = keep done items forever
	// MaxTitle is the maximum todo title length in bytes. Values < 1 fall back to
	// the default (TodoMaxTitleDefault); a value above TodoMaxTitleCeiling — the
	// hard limit baked into the database CHECK constraint — is rejected at load.
	// Config may only tighten below the ceiling. Reloadable.
	MaxTitle int `toml:"max_title"`
	// MaxNote is the maximum todo note length in bytes. Values < 1 fall back to
	// the default (TodoMaxNoteDefault); a value above TodoMaxNoteCeiling — the
	// hard database CHECK ceiling — is rejected at load. Reloadable.
	MaxNote int `toml:"max_note"`
	// ListLimit caps how many items a single List/ListAll returns, so an in-scope
	// caller can't force an unbounded allocation or a long store-mutex hold. Values
	// < 1 fall back to the default (TodoListLimitDefault); a value above
	// TodoListLimitCeiling is rejected at load. Restart-only (fixed at store open).
	ListLimit int `toml:"list_limit"`
	// SweepInterval is how often the lease/retention sweep loop runs. Empty,
	// unparseable, or non-positive uses the default (TodoSweepIntervalDefault); a
	// zero cadence would busy-loop. Restart-only (the loop ticker is built once).
	SweepInterval string `toml:"sweep_interval"`
	// BusyTimeout is the SQLite busy_timeout for the todos database. The claim
	// contract ("loser gets zero rows") relies on a contended writer waiting rather
	// than erroring with SQLITE_BUSY, so this is load-bearing. Empty, unparseable,
	// or non-positive uses the default (TodoBusyTimeoutDefault); a value above
	// TodoBusyTimeoutCeiling is rejected at load. Restart-only.
	BusyTimeout string `toml:"busy_timeout"`
}

// Task-list ([todo]) operational-limit defaults, mirroring the fixed literals
// that governed the store before issue #1249 made them configurable.
const (
	TodoMaxTitleDefault      = 500
	TodoMaxNoteDefault       = 2000
	TodoListLimitDefault     = 2000
	TodoSweepIntervalDefault = time.Minute
	TodoBusyTimeoutDefault   = 5 * time.Second
)

// Hard safety ceilings for the [todo] operational limits. TodoMaxTitleCeiling
// and TodoMaxNoteCeiling equal the database CHECK constraints baked into the
// schema at creation — config may tighten below them but never past them, so a
// configured limit can never exceed what the database will accept. The others
// bound allocation and lock-wait time.
const (
	TodoMaxTitleCeiling    = 500
	TodoMaxNoteCeiling     = 2000
	TodoListLimitCeiling   = 100_000
	TodoBusyTimeoutCeiling = 5 * time.Minute
)

// EmitMode resolves the emit-events mode, defaulting to "scenario".
func (t TodoConfig) EmitMode() string {
	switch t.EmitEvents {
	case TodoEmitAll, TodoEmitOff:
		return t.EmitEvents
	default:
		return TodoEmitScenario
	}
}

// ClaimLeaseDuration resolves the claim-lease window. Unset (or, as a fail-safe,
// unparseable — though Validate rejects that at startup) defaults to 30m; an
// explicit "0" disables the lease sweep.
func (t TodoConfig) ClaimLeaseDuration() time.Duration {
	if t.ClaimLease == "" {
		return DefaultTodoClaimLease
	}

	d, err := ParseDurationWithDays(t.ClaimLease)
	if err != nil {
		return DefaultTodoClaimLease
	}

	return d
}

// RetentionDuration resolves the done-item retention window. Unset or zero
// keeps done items indefinitely (returns 0).
func (t TodoConfig) RetentionDuration() time.Duration {
	if t.Retention == "" {
		return 0
	}

	d, _ := ParseDurationWithDays(t.Retention)

	return d
}

// MaxTitleOrDefault returns the maximum title length. A non-positive value means
// "use the default".
func (t TodoConfig) MaxTitleOrDefault() int {
	if t.MaxTitle < 1 {
		return TodoMaxTitleDefault
	}

	return t.MaxTitle
}

// MaxNoteOrDefault returns the maximum note length. A non-positive value means
// "use the default".
func (t TodoConfig) MaxNoteOrDefault() int {
	if t.MaxNote < 1 {
		return TodoMaxNoteDefault
	}

	return t.MaxNote
}

// ListLimitOrDefault returns the List/ListAll row cap. A non-positive value
// means "use the default".
func (t TodoConfig) ListLimitOrDefault() int {
	if t.ListLimit < 1 {
		return TodoListLimitDefault
	}

	return t.ListLimit
}

// SweepIntervalDuration returns the lease/retention sweep cadence, or the
// default when unset, unparseable, or non-positive (a zero cadence would
// busy-loop the sweep timer).
func (t TodoConfig) SweepIntervalDuration() time.Duration {
	return positiveDurationOrDefault(t.SweepInterval, TodoSweepIntervalDefault)
}

// BusyTimeoutDuration returns the todos-database SQLite busy_timeout, or the
// default when unset, unparseable, or non-positive (a zero/negative timeout
// would break the claim contract by failing a contended writer immediately).
func (t TodoConfig) BusyTimeoutDuration() time.Duration {
	return positiveDurationOrDefault(t.BusyTimeout, TodoBusyTimeoutDefault)
}

func ParseDurationWithDays(s string) (time.Duration, error) {
	var total time.Duration

	if i := strings.Index(s, "d"); i > 0 {
		var days int
		if _, err := fmt.Sscanf(s[:i+1], "%dd", &days); err == nil {
			total = time.Duration(days) * 24 * time.Hour
			s = s[i+1:]
		}
	}

	if s != "" {
		d, err := time.ParseDuration(s)
		if err != nil {
			return 0, fmt.Errorf("invalid duration: %w", err)
		}

		total += d
	}

	if total < 0 {
		return 0, errors.New("negative duration not allowed")
	}

	return total, nil
}

type Keybindings struct {
	Prefix              string `toml:"prefix"`
	NewSession          string `toml:"new_session"`
	ForkSession         string `toml:"fork_session"`
	DeleteSession       string `toml:"delete_session"`
	Detach              string `toml:"detach"`
	SessionList         string `toml:"session_list"`
	NextSession         string `toml:"next_session"`
	PrevSession         string `toml:"prev_session"`
	LastSession         string `toml:"last_session"`
	ResumeSession       string `toml:"resume_session"`
	RenameSession       string `toml:"rename_session"`
	Search              string `toml:"search"`
	ScrollMode          string `toml:"scroll_mode"`
	Shell               string `toml:"shell"`
	OrchestratorSession string `toml:"orchestrator_session"`
	// Prefix-action commands issued from an attached session (prefix key then
	// one of these). Previously hard-coded as m/a/r (issue #1233).
	Messages       string `toml:"messages"`
	Approvals      string `toml:"approvals"`
	RestartSession string `toml:"restart_session"`
	// Overlay holds the keys used inside the full-screen terminal overlays
	// (dashboard, approval prompt, message viewer, scroll pager). See #1233.
	Overlay OverlayKeybindings `toml:"overlay"`
}

func (k Keybindings) passthroughActions() []struct{ label, key string } {
	return []struct{ label, key string }{
		{"detach", k.Detach},
		{"session_list", k.SessionList},
		{"shell", k.Shell},
		{"next_session", k.NextSession},
		{"prev_session", k.PrevSession},
		{"last_session", k.LastSession},
		{"new_session", k.NewSession},
		{"fork_session", k.ForkSession},
		{"orchestrator_session", k.OrchestratorSession},
		{"rename_session", k.RenameSession},
		{"scroll_mode", k.ScrollMode},
		{"messages", k.Messages},
		{"approvals", k.Approvals},
		{"restart_session", k.RestartSession},
	}
}

// parsePassthroughByte accepts the documented action-key shape: empty disables
// an action; otherwise exactly one printable ASCII byte is required. Keeping
// this byte-oriented is deliberate because the attached terminal loop consumes
// raw bytes, not runes or Bubble Tea key names.
func parsePassthroughByte(raw string) (byte, bool) {
	if len(raw) != 1 || raw[0] < 0x20 || raw[0] >= 0x7f {
		return 0, false
	}

	return raw[0], true
}

func parsePrefixByte(raw string) (byte, bool) {
	s := strings.TrimSpace(strings.ToLower(raw))
	if s == "" {
		return 0x02, true // empty keeps the historical ctrl+b default
	}

	if strings.HasPrefix(s, "ctrl+") && len(s) == 6 {
		ch := s[5]
		if ch >= 'a' && ch <= 'z' {
			return ch - 'a' + 1, true
		}
	}

	return parsePassthroughByte(s)
}

// Validate rejects passthrough bindings that the byte-oriented runtime cannot
// represent faithfully. Empty action values remain valid and explicitly
// disable that action; malformed, multi-character, multibyte, control, and NUL
// values fail at config load rather than being silently reduced to s[0].
func (k Keybindings) Validate() error {
	var errs []error

	if _, ok := parsePrefixByte(k.Prefix); !ok {
		errs = append(errs, fmt.Errorf("keybindings.prefix %q: must be ctrl+a through ctrl+z or exactly one printable ASCII byte", k.Prefix))
	}

	for _, binding := range k.passthroughActions() {
		if binding.key == "" {
			continue
		}

		if _, ok := parsePassthroughByte(binding.key); !ok {
			errs = append(errs, fmt.Errorf("keybindings.%s %q: must be empty (disabled) or exactly one printable ASCII byte", binding.label, binding.key))
		}
	}

	return errors.Join(errs...)
}

// Conflicts reports collisions on the parsed bytes consumed by the attach
// loop, including an action that collides with a single-byte prefix (the prefix
// wins because pressing it twice sends a literal prefix). Picker/overlay keys
// operate in a separate mode and are intentionally not compared here. Invalid
// shapes are handled by Validate; conflicts remain non-fatal load warnings.
func (k Keybindings) Conflicts() []string {
	seen := map[byte][]string{}
	if prefix, ok := parsePrefixByte(k.Prefix); ok {
		seen[prefix] = append(seen[prefix], "prefix")
	}

	for _, binding := range k.passthroughActions() {
		key, ok := parsePassthroughByte(binding.key)
		if !ok {
			continue
		}

		seen[key] = append(seen[key], binding.label)
	}

	// Sort the keys so the warning order is deterministic.
	keys := make([]byte, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}

	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })

	var conflicts []string

	for _, key := range keys {
		labels := seen[key]
		if len(labels) > 1 {
			conflicts = append(conflicts, fmt.Sprintf(
				"keybinding byte %q is bound to multiple prefix commands: %s (prefix and earlier actions take precedence)",
				string(rune(key)), strings.Join(labels, ", ")))
		}
	}

	return conflicts
}

// OverlayKeybindings configures the keys used inside the terminal TUI overlays.
// Every value is a space-separated list of bubbletea key names (single letters,
// "up", "down", "enter", "esc", "pgup", "ctrl+d", ...); pressing any listed key
// triggers the action. Empty fields fall back to the built-in defaults, so a
// partial [keybindings.overlay] table only overrides the keys it names.
type OverlayKeybindings struct {
	// Shared navigation, applied across the overlays.
	Up       string `toml:"up"`
	Down     string `toml:"down"`
	PageUp   string `toml:"page_up"`
	PageDown string `toml:"page_down"`
	Top      string `toml:"top"`
	Bottom   string `toml:"bottom"`
	Confirm  string `toml:"confirm"`
	Cancel   string `toml:"cancel"`
	// Dashboard actions.
	DashboardAttach string `toml:"dashboard_attach"`
	DashboardStop   string `toml:"dashboard_stop"`
	DashboardDelete string `toml:"dashboard_delete"`
	DashboardResume string `toml:"dashboard_resume"`
	// Approval prompt actions.
	ApprovalAllow    string `toml:"approval_allow"`
	ApprovalDeny     string `toml:"approval_deny"`
	ApprovalAllowAll string `toml:"approval_allow_all"`
	// Message viewer actions.
	MessagePin         string `toml:"message_pin"`
	MessageExpandAll   string `toml:"message_expand_all"`
	MessageCollapseAll string `toml:"message_collapse_all"`
	MessageNextConv    string `toml:"message_next_conversation"`
	MessagePrevConv    string `toml:"message_prev_conversation"`
}

type Notifications struct {
	Enabled    bool   `toml:"enabled"`
	OnApproval bool   `toml:"on_approval"`
	OnStopped  bool   `toml:"on_stopped"`
	Command    string `toml:"command"`
	// Backend selects how proactive `gr notify` push notifications are delivered:
	// "macos" (osascript desktop notification; the default when unset) or
	// "command" (run [notifications] command with GRAITH_NOTIFY_* env vars). Other
	// backends (ntfy/pushover/slack) are planned follow-ups and rejected for now.
	Backend string `toml:"backend"`
	// MaxPerHour rate-limits low/normal push notifications over a rolling hour so a
	// misbehaving trigger can't storm the user. <=0 uses DefaultNotifyMaxPerHour.
	// High-priority notifications bypass this limit.
	MaxPerHour int `toml:"max_per_hour"`
	// QuietHoursStart / QuietHoursEnd define a daily window ("HH:MM", 24-hour) in
	// which low/normal push notifications are suppressed. The window may wrap past
	// midnight (start > end, e.g. 22:00-07:00). Both must be set to take effect.
	// High-priority notifications bypass quiet hours.
	QuietHoursStart string `toml:"quiet_hours_start"`
	QuietHoursEnd   string `toml:"quiet_hours_end"`
	// Timing carries the low-level coalescing, dispatch, and inbox-notification
	// timing knobs under [notifications.timing]. Every field is optional and
	// resolves to its documented default through the NotificationTiming accessors,
	// so leaving the table out preserves the historical behaviour (issue #1245).
	Timing NotificationTiming `toml:"timing"`
}

// NotificationTiming gathers the notification timing policy that was previously
// spread as fixed constants across the daemon's push (pushnotify.go) and
// inbox-notification (notify.go) paths (issue #1245). Every field is optional:
// an empty or unparseable value falls back to the matching default constant,
// preserving the historical behaviour.
type NotificationTiming struct {
	// CoalesceWindow is how long an identical (title+message+priority) push
	// notification is dropped as a duplicate, coalescing rapid-fire events. Empty
	// uses the default (NotifyCoalesceWindowDefault); "0" disables coalescing.
	CoalesceWindow string `toml:"coalesce_window"`
	// DispatchTimeout bounds a single backend dispatch (osascript / notifier app /
	// command) so a hung helper can't block the caller. Empty or non-positive uses
	// the default (NotifyDispatchTimeoutDefault).
	DispatchTimeout string `toml:"dispatch_timeout"`
	// InboxIdleTimeout is how long an attached session's PTY must be free of user
	// input before an inbox notification is injected, so it doesn't land mid-type.
	// Empty or non-positive uses the default (NotifyInboxIdleTimeoutDefault).
	InboxIdleTimeout string `toml:"inbox_idle_timeout"`
	// InboxMaxWait caps the total wait for user idle before an inbox notification
	// is injected regardless. Empty or non-positive uses the default
	// (NotifyInboxMaxWaitDefault).
	InboxMaxWait string `toml:"inbox_max_wait"`
	// InboxCooldown is the minimum interval between unread-inbox notifications to
	// one session, throttling repeat nudges. Empty uses the default
	// (NotifyInboxCooldownDefault); "0" disables the cooldown.
	InboxCooldown string `toml:"inbox_cooldown"`
	// InboxDetachedDelay is the settle delay before notifying a session with no
	// attached client (no user-idle signal to wait on). Empty uses the default
	// (NotifyInboxDetachedDelayDefault); "0" notifies immediately.
	InboxDetachedDelay string `toml:"inbox_detached_delay"`
}

// Notification timing defaults. Each mirrors the fixed constant that governed the
// behaviour before issue #1245 made the policy configurable.
const (
	NotifyCoalesceWindowDefault     = 30 * time.Second
	NotifyDispatchTimeoutDefault    = 15 * time.Second
	NotifyInboxIdleTimeoutDefault   = 10 * time.Second
	NotifyInboxMaxWaitDefault       = 2 * time.Minute
	NotifyInboxCooldownDefault      = 30 * time.Second
	NotifyInboxDetachedDelayDefault = 5 * time.Second
)

// CoalesceWindowDuration returns the push-notification coalescing window, or the
// default when unset or unparseable. A "0" disables coalescing.
func (t NotificationTiming) CoalesceWindowDuration() time.Duration {
	return durationOrDefault(t.CoalesceWindow, NotifyCoalesceWindowDefault)
}

// DispatchTimeoutDuration returns the per-backend dispatch timeout, or the
// default when unset, unparseable, or non-positive (a zero timeout would fail
// every dispatch instantly).
func (t NotificationTiming) DispatchTimeoutDuration() time.Duration {
	return positiveDurationOrDefault(t.DispatchTimeout, NotifyDispatchTimeoutDefault)
}

// InboxIdleTimeoutDuration returns the user-idle wait before an inbox
// notification is injected, or the default when unset, unparseable, or
// non-positive.
func (t NotificationTiming) InboxIdleTimeoutDuration() time.Duration {
	return positiveDurationOrDefault(t.InboxIdleTimeout, NotifyInboxIdleTimeoutDefault)
}

// InboxMaxWaitDuration returns the cap on the user-idle wait, or the default
// when unset, unparseable, or non-positive.
func (t NotificationTiming) InboxMaxWaitDuration() time.Duration {
	return positiveDurationOrDefault(t.InboxMaxWait, NotifyInboxMaxWaitDefault)
}

// InboxCooldownDuration returns the minimum interval between unread-inbox
// notifications to one session, or the default when unset or unparseable. A "0"
// disables the cooldown.
func (t NotificationTiming) InboxCooldownDuration() time.Duration {
	return durationOrDefault(t.InboxCooldown, NotifyInboxCooldownDefault)
}

// InboxDetachedDelayDuration returns the settle delay before notifying a
// detached session, or the default when unset or unparseable. A "0" notifies
// immediately.
func (t NotificationTiming) InboxDetachedDelayDuration() time.Duration {
	return durationOrDefault(t.InboxDetachedDelay, NotifyInboxDetachedDelayDefault)
}

// Notification priority levels for `gr notify`.
const (
	NotifyPriorityLow    = "low"
	NotifyPriorityNormal = "normal"
	NotifyPriorityHigh   = "high"
)

// DefaultNotifyMaxPerHour is the rolling-hour cap on low/normal push
// notifications used when [notifications] max_per_hour is unset.
const DefaultNotifyMaxPerHour = 12

// NotifyBackendName returns the effective push-notification backend, defaulting
// to "macos" when unset.
func (n Notifications) NotifyBackendName() string {
	if strings.TrimSpace(n.Backend) == "" {
		return "macos"
	}

	return strings.TrimSpace(n.Backend)
}

// MaxPerHourValue returns the effective rolling-hour push-notification cap,
// defaulting to DefaultNotifyMaxPerHour when unset (<=0).
func (n Notifications) MaxPerHourValue() int {
	if n.MaxPerHour <= 0 {
		return DefaultNotifyMaxPerHour
	}

	return n.MaxPerHour
}

// NormalizeNotifyPriority resolves a user-supplied priority to a canonical
// level, defaulting an empty value to "normal". It reports ok=false for an
// unrecognised value so callers can reject it.
func NormalizeNotifyPriority(p string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case "", NotifyPriorityNormal:
		return NotifyPriorityNormal, true
	case NotifyPriorityLow:
		return NotifyPriorityLow, true
	case NotifyPriorityHigh:
		return NotifyPriorityHigh, true
	default:
		return "", false
	}
}

// QuietHoursConfigured reports whether a quiet-hours window is fully set.
func (n Notifications) QuietHoursConfigured() bool {
	return strings.TrimSpace(n.QuietHoursStart) != "" && strings.TrimSpace(n.QuietHoursEnd) != ""
}

// parseClock parses a 24-hour "H:MM"/"HH:MM" time into minutes-since-midnight.
// It requires exactly two colon-separated all-digit fields with no trailing
// content, so "22:00:59", "09:00abc" and "7:5 " are rejected (fmt.Sscanf would
// have silently accepted a prefix and ignored the rest). Non-zero-padded hours
// (e.g. "9:00") are allowed for convenience.
func parseClock(s string) (int, bool) {
	s = strings.TrimSpace(s)

	hh, mm, ok := strings.Cut(s, ":")
	if !ok {
		return 0, false
	}

	h, err := strconv.Atoi(hh)
	if err != nil {
		return 0, false
	}

	m, err := strconv.Atoi(mm)
	if err != nil {
		return 0, false
	}

	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}

	return h*60 + m, true
}

// InQuietHours reports whether the local time t falls within the configured
// quiet-hours window. It supports a window that wraps past midnight (start >
// end). An unset or unparseable window returns false (fail-open: a typo mutes
// nothing rather than everything — Validate rejects a malformed window at load).
func (n Notifications) InQuietHours(t time.Time) bool {
	if !n.QuietHoursConfigured() {
		return false
	}

	start, ok1 := parseClock(n.QuietHoursStart)
	end, ok2 := parseClock(n.QuietHoursEnd)

	if !ok1 || !ok2 {
		return false
	}

	cur := t.Hour()*60 + t.Minute()

	if start == end {
		// Degenerate zero-length window: never quiet.
		return false
	}

	if start < end {
		return cur >= start && cur < end
	}

	// Wrap-around window (e.g. 22:00-07:00): quiet if before end OR at/after start.
	return cur >= start || cur < end
}

// knownNotifyBackend reports whether name is an implemented push backend.
func knownNotifyBackend(name string) bool {
	switch name {
	case "macos", "command":
		return true
	default:
		return false
	}
}

// Validate checks the [notifications] block for static errors: an unknown push
// backend, a malformed quiet-hours window, or a "command" backend with no
// command set. It fails closed so a typo surfaces at config-load rather than as
// a silent no-op notification.
func (n Notifications) Validate() error {
	backend := n.NotifyBackendName()
	if !knownNotifyBackend(backend) {
		return fmt.Errorf("[notifications] backend %q is not supported (want \"macos\" or \"command\"; ntfy/pushover/slack are not yet implemented)", backend)
	}

	if backend == "command" && strings.TrimSpace(n.Command) == "" {
		return errors.New("[notifications] backend=\"command\" requires a non-empty command")
	}

	if strings.TrimSpace(n.QuietHoursStart) != "" || strings.TrimSpace(n.QuietHoursEnd) != "" {
		if !n.QuietHoursConfigured() {
			return errors.New("[notifications] quiet_hours_start and quiet_hours_end must both be set")
		}

		if _, ok := parseClock(n.QuietHoursStart); !ok {
			return fmt.Errorf("[notifications] quiet_hours_start %q is invalid (want \"HH:MM\", 24-hour)", n.QuietHoursStart)
		}

		if _, ok := parseClock(n.QuietHoursEnd); !ok {
			return fmt.Errorf("[notifications] quiet_hours_end %q is invalid (want \"HH:MM\", 24-hour)", n.QuietHoursEnd)
		}
	}

	return nil
}

type Approvals struct {
	// Enabled controls whether the PreToolUse approve-request gating hook is
	// installed. nil (unset) means disabled: the status/lifecycle hooks are
	// still installed but the approval gate is not, because unattended agents
	// otherwise see their own tool calls as human-rejected and the OS sandbox
	// is the intended guardrail. Set to true to opt back into human approval
	// gating.
	Enabled *bool `toml:"enabled"`
	// Backend selects who makes the automated decision: "" (none — always
	// prompt the human), "command"/"external" (delegate to a command over
	// graith's JSON contract), "localmost" (the real localmost binary over its
	// native protocol), or "builtin" (graith's built-in localmost-compatible
	// engine). It is the canonical selector; Mode is the deprecated predecessor.
	Backend string           `toml:"backend"`
	Mode    string           `toml:"mode"`
	AutoPop bool             `toml:"auto_pop"`
	Timeout string           `toml:"timeout"`
	Command string           `toml:"command"`
	Builtin ApprovalsBuiltin `toml:"builtin"`

	// CommandTimeout bounds a single external "command"/"external" backend
	// invocation; LocalmostTimeout bounds a single "localmost" binary check.
	// Both default to defaultBackendExecTimeout (5s) when unset (see
	// CommandTimeoutDuration/LocalmostTimeoutDuration). Each must be positive,
	// no larger than maxBackendExecTimeout, and strictly shorter than the
	// configured human/headless approval Timeout. Keeping the subprocess phase
	// shorter than that policy prevents a backend from dominating an approval
	// operation — a class of bug that previously caused approval-behaviour
	// glitches (see #244). Validate enforces this hierarchy.
	CommandTimeout   string `toml:"command_timeout"`
	LocalmostTimeout string `toml:"localmost_timeout"`
}

// Backend execution timeout bounds. For interactive approvals, a subprocess may
// run before a separate human queue wait; for headless approvals, the caller's
// context remains the outer bound. The subprocess timeout must be shorter than
// the configured approval policy. defaultBackendExecTimeout preserves the
// historical fixed 5s; maxBackendExecTimeout caps how long a single backend
// invocation may be configured to block.
const (
	defaultBackendExecTimeout = 5 * time.Second
	maxBackendExecTimeout     = 60 * time.Second
)

// ApprovalsBuiltin configures the built-in localmost-compatible engine. Rules
// can be supplied either as a path to an external localmost-format config.json
// (Config), or inline in config.toml via Allow/Deny/AllowSafeXargs/
// AskNoninteractive. The two forms are mutually exclusive (see Approvals.Validate).
type ApprovalsBuiltin struct {
	// Config is the path to a localmost-format config.json (allow/deny rules).
	Config string `toml:"config"`

	// Allow and Deny are the inline allow/deny rulesets. Each element is either
	// a bare rule string ("@arg @*") or a table with per-rule keys
	// (rule/unless/redirect/pipe). They are decoded as []any so both TOML forms
	// — an array of strings and an array of tables ([[approvals.builtin.allow]])
	// — are accepted, then converted to the localmost schema (see InlineJSON).
	Allow []any `toml:"allow"`
	Deny  []any `toml:"deny"`

	// AllowSafeXargs and AskNoninteractive mirror the localmost top-level flags.
	// nil means unset (the engine's default of true applies).
	AllowSafeXargs    *bool `toml:"allowSafeXargs"`
	AskNoninteractive *bool `toml:"askNoninteractive"`
}

// HasInline reports whether any inline ruleset field is set. When true, the
// rules are read from config.toml rather than an external Config file. An empty
// array (allow = []) defines no rules and does not count as inline, so it does
// not spuriously conflict with an external Config path.
func (b ApprovalsBuiltin) HasInline() bool {
	return len(b.Allow) > 0 || len(b.Deny) > 0 || b.AllowSafeXargs != nil || b.AskNoninteractive != nil
}

// builtinRuleKeys is the set of keys a per-rule table may contain, mirroring the
// localmost rule schema (see localmost.Rule). Any other key is a typo (e.g.
// "unles" for "unless") that localmost would silently drop, so we reject it up
// front rather than let an allow rule be silently broadened.
var builtinRuleKeys = map[string]struct{}{
	"rule": {}, "unless": {}, "redirect": {}, "pipe": {},
}

// validateInlineRules checks each element of an inline allow/deny ruleset. A
// rule is either a bare string or a table; a table must carry a non-empty
// "rule" key and no unknown keys. This closes the fail-open where a misspelled
// per-rule key (unless/redirect/pipe) is silently ignored, broadening the rule.
func validateInlineRules(list string, rules []any) error {
	for i, elem := range rules {
		switch v := elem.(type) {
		case string:
			// Bare rule string — always valid shape (localmost validates content).
		case map[string]any:
			if rule, ok := v["rule"].(string); !ok || strings.TrimSpace(rule) == "" {
				return fmt.Errorf("[approvals.builtin] %s rule %d: table form requires a non-empty \"rule\" key", list, i)
			}

			for k := range v {
				if _, ok := builtinRuleKeys[k]; !ok {
					return fmt.Errorf("[approvals.builtin] %s rule %d: unknown key %q (valid: rule, unless, redirect, pipe)", list, i, k)
				}
			}
		default:
			return fmt.Errorf("[approvals.builtin] %s rule %d: must be a string or a table, got %T", list, i, elem)
		}
	}

	return nil
}

// InlineJSON renders the inline ruleset as localmost-format config.json bytes,
// so the existing (tested) localmost parser can compile it. The TOML keys map
// 1:1 to the localmost JSON schema (allow/deny/allowSafeXargs/askNoninteractive,
// and per-rule rule/unless/redirect/pipe), so a plain JSON re-encode suffices.
func (b ApprovalsBuiltin) InlineJSON() ([]byte, error) {
	payload := map[string]any{}

	if b.Allow != nil {
		payload["allow"] = b.Allow
	}

	if b.Deny != nil {
		payload["deny"] = b.Deny
	}

	if b.AllowSafeXargs != nil {
		payload["allowSafeXargs"] = *b.AllowSafeXargs
	}

	if b.AskNoninteractive != nil {
		payload["askNoninteractive"] = *b.AskNoninteractive
	}

	return json.Marshal(payload)
}

// legacyModeBackend maps a deprecated [approvals] mode value to its effective
// backend. The command/external/localmost modes all map to the "command"
// backend — for mode="localmost" this preserves the historical behaviour
// (graith's own JSON contract, NOT the native-protocol "localmost" backend).
// mode="auto" maps straight to the "auto" backend.
func legacyModeBackend(mode string) (string, bool) {
	switch mode {
	case "command", "external", "localmost":
		return "command", true
	case "auto":
		// mode="auto" is the deprecated spelling of backend="auto"; it maps
		// straight through rather than to the "command" backend.
		return "auto", true
	default:
		return "", false
	}
}

func knownApprovalsBackend(name string) bool {
	switch name {
	case "prompt", "command", "external", "localmost", "builtin", "auto":
		return true
	default:
		return false
	}
}

func canonicalApprovalsBackend(name string) string {
	if name == "external" {
		return "command"
	}

	return name
}

// ResolveBackend resolves the effective approvals backend, applying back-compat
// for the deprecated Mode field. It returns the backend name, a non-empty
// deprecation message when a legacy Mode value was used (callers log it once),
// and an error for an unknown backend or a conflicting Mode+Backend pair.
//
// Resolution order:
//  1. If Backend is set, use it. If a legacy Mode is ALSO set and maps to a
//     different backend, that is a hard error (refuse to guess intent).
//  2. Else if Mode is one of command/external/localmost, map it to the
//     "command" backend (historical behaviour) and return a deprecation
//     message. A Mode with no Backend is always a warning, never an error.
//  3. Else, the "prompt" backend (no automation).
func (a Approvals) ResolveBackend() (backend, deprecation string, err error) {
	legacy, isLegacy := legacyModeBackend(a.Mode)

	if a.Backend != "" {
		if !knownApprovalsBackend(a.Backend) {
			return "", "", fmt.Errorf("[approvals] backend %q is not recognised (want one of prompt, command, external, localmost, builtin, auto)", a.Backend)
		}

		if isLegacy && canonicalApprovalsBackend(a.Backend) != legacy {
			return "", "", fmt.Errorf(
				"[approvals] backend=%q conflicts with the deprecated mode=%q; remove mode (mode=%q maps to backend=%q)",
				a.Backend, a.Mode, a.Mode, legacy)
		}

		if isLegacy {
			// mode agrees with backend but is now redundant — nudge removal.
			return a.Backend, fmt.Sprintf(
				"[approvals] mode=%q is deprecated and redundant now that backend=%q is set; remove mode.",
				a.Mode, a.Backend), nil
		}

		return a.Backend, "", nil
	}

	if isLegacy {
		return legacy, legacyDeprecationMessage(a.Mode), nil
	}

	return "prompt", "", nil
}

func legacyDeprecationMessage(mode string) string {
	if mode == "auto" {
		return `[approvals] mode="auto" is deprecated; set backend="auto" instead.`
	}

	if mode == "localmost" {
		return `[approvals] mode="localmost" is deprecated. It maps to backend="command" ` +
			`(graith's JSON contract — unchanged behaviour); set backend="command" to silence this. ` +
			`To instead run the real localmost binary over its native protocol, set backend="localmost".`
	}

	return fmt.Sprintf(`[approvals] mode=%q is deprecated; set backend="command" instead.`, mode)
}

// backendUsesCommand reports whether the [approvals] command key is meaningful
// for the given resolved backend. The command/external backend requires it (the
// approver command invoked over graith's JSON contract); the native localmost
// backend uses it as an optional override for the "localmost" binary path. The
// prompt and builtin backends ignore it entirely.
func backendUsesCommand(backend string) bool {
	switch backend {
	case "command", "external", "localmost":
		return true
	default:
		return false
	}
}

// Validate checks the [approvals] config for static contradictions that would
// otherwise only surface as an opaque fail-closed session crash at create time
// (see #740). It rejects an unknown or conflicting backend/mode (via
// ResolveBackend) and a command key set for a resolved backend that ignores it.
// Backend *availability* (command present, localmost binary on PATH, builtin
// config loadable) is still deferred to session-create by the daemon.
func (a Approvals) Validate() error {
	backend, _, err := a.ResolveBackend()
	if err != nil {
		return err
	}

	if strings.TrimSpace(a.Command) != "" && !backendUsesCommand(backend) {
		return fmt.Errorf(
			"[approvals] command=%q is set but the resolved backend %q ignores it "+
				`(command is only used by backend="command"/"external" as the external approver, `+
				`or by backend="localmost" as the binary override); `+
				`set backend="command" to use it as an external approver, or remove command`,
			a.Command, backend)
	}

	// The builtin engine's rules come from either an external file (Config) or
	// inline TOML — never both. A conflict is a hard error (mirrors mode vs
	// backend) rather than a silent precedence rule.
	if strings.TrimSpace(a.Builtin.Config) != "" && a.Builtin.HasInline() {
		return errors.New("[approvals.builtin] config (external file) and inline rules " +
			"(allow/deny/allowSafeXargs/askNoninteractive) are both set; " +
			"use one or the other")
	}

	// Compile the inline ruleset now so a malformed rule fails at config-load
	// with a clear message instead of an opaque fail-closed session crash. This
	// is a pure static check (no file IO); the external-file path is deferred to
	// session-create availability, mirroring the other backends.
	if a.Builtin.HasInline() {
		// Reject typo'd per-rule keys before compiling: localmost's rule decoder
		// silently ignores unknown JSON fields, so a misspelled unless/redirect/
		// pipe would otherwise pass validation while quietly broadening the rule.
		if err := validateInlineRules("allow", a.Builtin.Allow); err != nil {
			return err
		}

		if err := validateInlineRules("deny", a.Builtin.Deny); err != nil {
			return err
		}

		data, err := a.Builtin.InlineJSON()
		if err != nil {
			return fmt.Errorf("[approvals.builtin] encode inline rules: %w", err)
		}

		if _, err := localmost.Parse(data); err != nil {
			return fmt.Errorf("[approvals.builtin] inline rules are invalid: %w", err)
		}
	}

	if err := a.validateBackendTimeouts(backend); err != nil {
		return err
	}

	return nil
}

// validateBackendTimeouts checks the per-backend execution timeouts for static
// contradictions: a syntactically invalid or non-positive value, a value beyond
// maxBackendExecTimeout, or a value that is not strictly shorter than the
// configured approval Timeout. The last check enforces the deadline hierarchy:
// a backend timeout at or above the main policy is incoherent and can cause the
// approval glitches #244 tracked. Explicitly-set fields are always checked; the
// resolved backend's effective timeout (including the 5s default) is also checked
// so a deliberately tiny [approvals] timeout is caught against the default
// backend bound too.
func (a Approvals) validateBackendTimeouts(backend string) error {
	enclosing := a.TimeoutDuration()

	for _, f := range []struct{ key, raw string }{
		{"command_timeout", a.CommandTimeout},
		{"localmost_timeout", a.LocalmostTimeout},
	} {
		if strings.TrimSpace(f.raw) == "" {
			continue
		}

		d, err := ParseDurationWithDays(f.raw)
		if err != nil {
			return fmt.Errorf("[approvals] %s=%q is not a valid duration: %w", f.key, f.raw, err)
		}

		if d <= 0 {
			return fmt.Errorf("[approvals] %s=%q must be a positive duration", f.key, f.raw)
		}

		if d > maxBackendExecTimeout {
			return fmt.Errorf(
				"[approvals] %s=%q exceeds the maximum backend execution timeout of %s",
				f.key, f.raw, maxBackendExecTimeout)
		}

		if d >= enclosing {
			return fmt.Errorf(
				"[approvals] %s=%q must be shorter than the configured approval timeout=%s "+
					"so backend execution cannot dominate the approval policy (see #244)",
				f.key, f.raw, enclosing)
		}
	}

	// Also guard the resolved backend's effective timeout (which may be the 5s
	// default when the field is unset) against a deliberately tiny enclosing
	// timeout, so the executing hierarchy is coherent even without an explicit
	// per-backend value.
	if execTimeout, ok := a.BackendExecTimeout(backend); ok && execTimeout >= enclosing {
		return fmt.Errorf(
			"[approvals] the effective %s backend execution timeout (%s) must be shorter than "+
				"the configured approval timeout=%s; raise [approvals] timeout or lower the backend timeout (see #244)",
			backend, execTimeout, enclosing)
	}

	return nil
}

// HookEnabled reports whether the approve-request PreToolUse hook should be
// installed. Defaults to false when unset — approval gating is opt-in.
func (a Approvals) HookEnabled() bool {
	return a.Enabled != nil && *a.Enabled
}

type StatusConfig struct {
	TTL string `toml:"ttl"`
}

func (s StatusConfig) TTLDuration() time.Duration {
	if s.TTL == "" {
		return 5 * time.Minute
	}

	d, err := ParseDurationWithDays(s.TTL)
	if err != nil {
		return 5 * time.Minute
	}

	return d
}

func (a Approvals) TimeoutDuration() time.Duration {
	if a.Timeout == "" {
		return 10 * time.Minute
	}

	d, err := ParseDurationWithDays(a.Timeout)
	if err != nil {
		return 10 * time.Minute
	}

	return d
}

// CommandTimeoutDuration is the effective execution timeout for the
// command/external backend, falling back to defaultBackendExecTimeout when
// unset or unparseable. Validate rejects a set-but-invalid value up front, so a
// fallback here only happens for an unset field.
func (a Approvals) CommandTimeoutDuration() time.Duration {
	return backendExecTimeoutOrDefault(a.CommandTimeout)
}

// LocalmostTimeoutDuration is the effective execution timeout for the localmost
// backend, falling back to defaultBackendExecTimeout when unset or unparseable.
func (a Approvals) LocalmostTimeoutDuration() time.Duration {
	return backendExecTimeoutOrDefault(a.LocalmostTimeout)
}

// backendExecTimeoutOrDefault parses a per-backend execution timeout string,
// returning defaultBackendExecTimeout for an empty, unparseable, or non-positive
// value so a misconfigured field degrades to the historical 5s rather than to
// an unbounded (0 => no timeout) subprocess.
func backendExecTimeoutOrDefault(s string) time.Duration {
	if strings.TrimSpace(s) == "" {
		return defaultBackendExecTimeout
	}

	d, err := ParseDurationWithDays(s)
	if err != nil || d <= 0 {
		return defaultBackendExecTimeout
	}

	return d
}

// BackendExecTimeout returns the effective execution timeout for a resolved
// backend name and whether that backend runs a bounded subprocess at all. Only
// the command/external and localmost backends spawn a child process; the others
// (prompt/builtin/auto) decide in-process and have no execution timeout.
func (a Approvals) BackendExecTimeout(backend string) (time.Duration, bool) {
	switch backend {
	case "command", "external":
		return a.CommandTimeoutDuration(), true
	case "localmost":
		return a.LocalmostTimeoutDuration(), true
	default:
		return 0, false
	}
}

// BackendPhaseTimeoutDuration returns the maximum subprocess phase included in
// one interactive approval request. A command/localmost backend may consume its
// full execution timeout and then defer to the human queue, so this phase is
// additive with TimeoutDuration rather than hidden inside it. In-process
// backends have no separately-configured execution phase.
func (a Approvals) BackendPhaseTimeoutDuration() time.Duration {
	backend, _, err := a.ResolveBackend()
	if err != nil {
		return 0 // Validate rejects this before a serving config is published.
	}

	timeout, _ := a.BackendExecTimeout(backend)

	return timeout
}

// ServerTimeoutDuration is the worst-case server-side bound for one interactive
// approval: automated backend execution followed by the full human wait. The
// hook connection adds its own response-delivery grace beyond this bound.
func (a Approvals) ServerTimeoutDuration() time.Duration {
	backend := a.BackendPhaseTimeoutDuration()
	human := a.TimeoutDuration()

	const maxDuration = time.Duration(1<<63 - 1)
	if human > maxDuration-backend {
		return maxDuration
	}

	return backend + human
}

type MCPServerConfig struct {
	Name          string            `json:"-"              toml:"name"`
	Command       string            `json:"command"        toml:"command"`
	Args          []string          `json:"args,omitempty" toml:"args,omitempty"`
	Env           map[string]string `json:"env,omitempty"  toml:"env,omitempty"`
	Disabled      bool              `json:"-"              toml:"disabled,omitempty"`
	Sandbox       *bool             `json:"-"              toml:"sandbox,omitempty"`
	SandboxConfig *SandboxConfig    `json:"-"              toml:"sandbox_config,omitempty"`
}

type Agent struct {
	Command      string            `json:"command"                 toml:"command"`
	Args         []string          `json:"args,omitempty"          toml:"args"`
	ResumeArgs   []string          `json:"resume_args,omitempty"   toml:"resume_args"`
	ForkArgs     []string          `json:"fork_args,omitempty"     toml:"fork_args"`
	Env          map[string]string `json:"env,omitempty"           toml:"env"`
	IdleTimeout  string            `json:"idle_timeout,omitempty"  toml:"idle_timeout"`
	InjectPrompt *bool             `json:"inject_prompt,omitempty" toml:"inject_prompt"`
	// PromptInjection selects HOW graith delivers its operating prompt to this
	// agent (append_system_prompt / cursor_rules / developer_instructions /
	// none). It is distinct from InjectPrompt, which is the on/off switch. An
	// empty value falls back to name-based detection so the built-in claude,
	// cursor, and codex agents work without explicit config; a custom agent
	// must set this to receive a prompt at all. Validated in Config.Validate.
	// See issue #1232.
	PromptInjection   string                     `json:"prompt_injection,omitempty"    toml:"prompt_injection"`
	PreTrustWorkspace *bool                      `json:"pre_trust_workspace,omitempty" toml:"pre_trust_workspace"`
	Sandbox           SandboxConfig              `json:"sandbox"                       toml:"sandbox"`
	MCPServers        map[string]MCPServerConfig `json:"mcp_servers,omitempty"         toml:"mcp_servers"`
	ValidateModel     string                     `json:"validate_model,omitempty"      toml:"validate_model"`
	// InterruptCount is how many times the interrupt byte (Ctrl-C, 0x03) is sent
	// to interrupt this agent, and InterruptDelayMs is the pause in milliseconds
	// between successive sends. Some agent TUIs ignore a single Ctrl-C and need
	// two rapid presses to actually interrupt (Claude's TUI wants ~200ms apart),
	// so both are configurable per agent. Unset means the built-in defaults
	// (count 1, delay 0). See issue #620.
	InterruptCount   *int `json:"interrupt_count,omitempty"    toml:"interrupt_count"`
	InterruptDelayMs *int `json:"interrupt_delay_ms,omitempty" toml:"interrupt_delay_ms"`
	// HeadlessCapable marks an agent as supporting headless stream-json mode
	// (issue #1075). Unset means not capable. Built-in Claude is flagged; a custom
	// protocol-speaking wrapper may opt in too, so a --headless request against an
	// unsupported agent fails closed rather than silently downgrading.
	HeadlessCapable *bool `json:"headless_capable,omitempty" toml:"headless_capable"`
	// AddDirArgs is the flag template graith uses to grant the agent access to an
	// additional directory — each included repo's co-located worktree. It is
	// expanded once per directory with {dir} bound to that path (see
	// AddDirArgsFor). An empty AddDirArgs means the agent's CLI has no such flag,
	// so its included worktrees are exposed only via the GRAITH_INCLUDE_*_PATH env
	// vars; this replaces the former hard-coded agentSupportsAddDir allowlist so a
	// custom agent can opt in from config alone (issue #1236). Built-in
	// claude/codex/cursor set ["--add-dir", "{dir}"].
	AddDirArgs []string `json:"add_dir_args,omitempty" toml:"add_dir_args"`
	// HeadlessArgs is the argv prefix graith prepends when launching this agent in
	// headless stream-json mode (issue #1075); the agent's own template-expanded
	// args follow it. Only consulted for a headless session. Moving it here (from
	// the former hard-coded headlessArgs) lets a custom headless_capable agent
	// define its own control-channel flags (issue #1236). Built-in claude sets the
	// `-p --output-format stream-json …` flags.
	HeadlessArgs []string `json:"headless_args,omitempty" toml:"headless_args"`
	// OptionArgs are conditional argv groups appended after the base args on every
	// launch (create/resume/fork). Each group's Args are template-expanded and
	// appended only when its When template variable resolves non-empty, so an
	// unset option leaves the agent's own default untouched — e.g. codex's
	// ["--model", "{model}"] gated on `when = "model"`. This moves the formerly
	// hard-coded codex adapter (model / profile / reasoning-effort / service-tier
	// / search / approval flags) into config so custom agents can define their own
	// conditional flags (issue #1236).
	OptionArgs []AgentOptionArg `json:"option_args,omitempty" toml:"option_args"`
	// Hooks declares how graith wires its generated lifecycle-hook artifact into
	// this agent's launch (issue #1236). Nil (or an empty Mechanism) means the
	// agent has no hook support. This replaces the former hard-coded agent-name
	// dispatch in injectHooks so a custom agent can adopt a supported mechanism
	// from config. The dynamic artifact is still built in Go; only the capability
	// and argv spellings live here.
	Hooks *AgentHookConfig `json:"hooks,omitempty" toml:"hooks"`
	// MCP declares how graith wires daemon-managed MCP servers into this agent's
	// launch (issue #1236). Nil (or an empty Mechanism) means no MCP wiring.
	// Server discovery, the config-key security check, and the generated
	// file/override values stay in Go; only the capability and argv spellings are
	// config.
	MCP *AgentMCPConfig `json:"mcp,omitempty" toml:"mcp"`
	// PromptInjectionArgs is the argv template graith uses to deliver the operating
	// prompt for the append_system_prompt and developer_instructions mechanisms,
	// with {prompt} bound to the (possibly encoded) prompt (issue #1236). Unset
	// falls back to the built-in spelling for the selected prompt_injection
	// mechanism, so a custom agent that only sets prompt_injection still works.
	// The cursor_rules and none mechanisms ignore it (they write a file / do
	// nothing). Built-in claude: ["--append-system-prompt", "{prompt}"]; codex:
	// ["-c", "developer_instructions={prompt}"].
	PromptInjectionArgs []string `json:"prompt_injection_args,omitempty" toml:"prompt_injection_args,omitempty"`
	// EmptyIDResumeArgs is the fallback resume argv graith uses when resume_args
	// template {agent_session_id} but no native session id was captured (issue
	// #1236). Empty means start fresh. This replaces the hard-coded codex
	// `resume --last` branch in resolveResumeArgs. Built-in codex:
	// ["resume", "--last"].
	EmptyIDResumeArgs []string `json:"empty_id_resume_args,omitempty" toml:"empty_id_resume_args,omitempty"`
}

// Hook-injection mechanisms an agent may declare in [agents.<name>.hooks]
// mechanism. Each selects the Go builder for the generated hook artifact and the
// argv spellings graith appends (issue #1236). graith owns this enum, so an
// unknown value is a config error rather than a silent no-op.
const (
	// HookMechanismClaudeSettings writes a Claude settings JSON and passes
	// SettingsArgs (default ["--settings", "{path}"]).
	HookMechanismClaudeSettings = "claude_settings"
	// HookMechanismCodexConfig emits EventArgs once per installed hook event plus
	// TrustArgs (default event ["-c", "hooks.{hook_event}={hook_value}"], trust
	// ["--dangerously-bypass-hook-trust"]).
	HookMechanismCodexConfig = "codex_config"
	// HookMechanismCursorProject writes .cursor/hooks.json in the worktree and
	// passes no launch args.
	HookMechanismCursorProject = "cursor_project"
)

// MCP-injection mechanisms an agent may declare in [agents.<name>.mcp]
// mechanism (issue #1236).
const (
	// MCPMechanismClaudeConfig writes an MCP config JSON and passes ConfigArgs
	// (default ["--mcp-config", "{path}"]).
	MCPMechanismClaudeConfig = "claude_config"
	// MCPMechanismCodexConfig emits ServerArgs once per representable server
	// (default ["-c", "mcp_servers.{mcp_name}.command={mcp_command}", "-c",
	// "mcp_servers.{mcp_name}.args={mcp_args}"]).
	MCPMechanismCodexConfig = "codex_config"
)

// AgentHookConfig declares how graith wires its generated lifecycle-hook artifact
// into an agent's launch (issue #1236). The artifact (a Claude settings JSON,
// Codex inline-TOML hook overrides, a Cursor project hooks.json) is built in Go;
// this carries only the capability (Mechanism, which selects the builder) and
// the argv spellings graith appends.
type AgentHookConfig struct {
	// Mechanism selects the hook-injection strategy (one of the HookMechanism*
	// constants). Empty means the agent has no hook support.
	Mechanism string `json:"mechanism,omitempty" toml:"mechanism,omitempty"`
	// SettingsArgs is the argv template for HookMechanismClaudeSettings, with
	// {path} bound to the generated settings file.
	SettingsArgs []string `json:"settings_args,omitempty" toml:"settings_args,omitempty"`
	// EventArgs is the per-event argv template for HookMechanismCodexConfig,
	// emitted once per installed hook event with {hook_event} and {hook_value}
	// bound (the value is Go-built inline TOML).
	EventArgs []string `json:"event_args,omitempty" toml:"event_args,omitempty"`
	// TrustArgs are appended once after the codex_config EventArgs to bypass
	// interactive hook-trust review.
	TrustArgs []string `json:"trust_args,omitempty" toml:"trust_args,omitempty"`
}

// AgentMCPConfig declares how graith wires daemon-managed MCP servers into an
// agent's launch (issue #1236).
type AgentMCPConfig struct {
	// Mechanism selects the MCP-injection strategy (one of the MCPMechanism*
	// constants). Empty means no MCP wiring.
	Mechanism string `json:"mechanism,omitempty" toml:"mechanism,omitempty"`
	// ConfigArgs is the argv template for MCPMechanismClaudeConfig, with {path}
	// bound to the generated MCP config file.
	ConfigArgs []string `json:"config_args,omitempty" toml:"config_args,omitempty"`
	// ServerArgs is the per-server argv template for MCPMechanismCodexConfig,
	// emitted once per server whose name is a representable config key, with
	// {mcp_name}, {mcp_command} (JSON-encoded), and {mcp_args} (JSON-encoded)
	// bound.
	ServerArgs []string `json:"server_args,omitempty" toml:"server_args,omitempty"`
}

var sessionLaunchTemplateVars = []string{
	"username", "agent_session_id", "session_name", "session_id",
	"worktree_path", "fork_source_agent_session_id", "model",
}

var optionArgTemplateVars = []string{
	"username", "agent_session_id", "session_name", "session_id",
	"worktree_path", "fork_source_agent_session_id", "model",
	"profile", "reasoning_effort", "service_tier", "approval_policy", "web_search",
}

var addDirTemplateVars = []string{
	"username", "agent_session_id", "session_name", "session_id",
	"worktree_path", "fork_source_agent_session_id", "model", "dir",
}

// validateAdapters checks every agent launch-template context (issue #1236):
// mechanisms must be known enum values, and argv fields may only reference the
// placeholders actually bound at their expansion site. A bad template is
// rejected at config load rather than later on create/resume/fork/include or a
// conditional headless launch.
func (a Agent) validateAdapters(name string) []error {
	var errs []error

	field := func(s string) string { return "agents." + name + "." + s }

	errs = appendTemplateErrs(errs, field("args"), a.Args, sessionLaunchTemplateVars...)
	errs = appendTemplateErrs(errs, field("resume_args"), a.ResumeArgs, sessionLaunchTemplateVars...)
	errs = appendTemplateErrs(errs, field("fork_args"), a.ForkArgs, sessionLaunchTemplateVars...)
	errs = appendTemplateErrs(errs, field("headless_args"), a.HeadlessArgs, sessionLaunchTemplateVars...)
	errs = appendTemplateErrs(errs, field("empty_id_resume_args"), a.EmptyIDResumeArgs, sessionLaunchTemplateVars...)

	errs = appendTemplateErrs(errs, field("add_dir_args"), a.AddDirArgs, addDirTemplateVars...)

	for i, opt := range a.OptionArgs {
		errs = appendTemplateErrs(errs,
			fmt.Sprintf("agents.%s.option_args[%d].args", name, i),
			opt.Args, optionArgTemplateVars...)
	}

	if a.Hooks != nil {
		switch a.Hooks.Mechanism {
		case "", HookMechanismClaudeSettings, HookMechanismCodexConfig, HookMechanismCursorProject:
		default:
			errs = append(errs, fmt.Errorf("agents.%s.hooks.mechanism %q: must be one of %q, %q, %q (or empty)",
				name, a.Hooks.Mechanism, HookMechanismClaudeSettings, HookMechanismCodexConfig, HookMechanismCursorProject))
		}

		errs = appendTemplateErrs(errs, field("hooks.settings_args"), a.Hooks.SettingsArgs, "path")
		errs = appendTemplateErrs(errs, field("hooks.event_args"), a.Hooks.EventArgs, "hook_event", "hook_value")
		errs = appendTemplateErrs(errs, field("hooks.trust_args"), a.Hooks.TrustArgs)
	}

	if a.MCP != nil {
		switch a.MCP.Mechanism {
		case "", MCPMechanismClaudeConfig, MCPMechanismCodexConfig:
		default:
			errs = append(errs, fmt.Errorf("agents.%s.mcp.mechanism %q: must be one of %q, %q (or empty)",
				name, a.MCP.Mechanism, MCPMechanismClaudeConfig, MCPMechanismCodexConfig))
		}

		errs = appendTemplateErrs(errs, field("mcp.config_args"), a.MCP.ConfigArgs, "path")
		errs = appendTemplateErrs(errs, field("mcp.server_args"), a.MCP.ServerArgs, "mcp_name", "mcp_command", "mcp_args")
	}

	errs = appendTemplateErrs(errs, field("prompt_injection_args"), a.PromptInjectionArgs, "prompt")

	return errs
}

// appendTemplateErrs appends an error for each {token} in tmpl that is not in
// allowed, keyed by the config path field. Used by validateAdapters.
func appendTemplateErrs(errs []error, field string, tmpl []string, allowed ...string) []error {
	if len(tmpl) == 0 {
		return errs
	}

	ok := make(map[string]bool, len(allowed))
	for _, a := range allowed {
		ok[a] = true
	}

	for _, tok := range templateTokens(tmpl) {
		if !ok[tok] {
			errs = append(errs, fmt.Errorf("%s: unsupported template variable %q (allowed: %v)", field, tok, allowed))
		}
	}

	return errs
}

// HookMechanism returns the agent's configured hook mechanism, or "" when hooks
// are unset. Callers dispatch on this rather than the literal agent name.
func (a Agent) HookMechanism() string {
	if a.Hooks == nil {
		return ""
	}

	return a.Hooks.Mechanism
}

// MCPMechanism returns the agent's configured MCP mechanism, or "" when unset.
func (a Agent) MCPMechanism() string {
	if a.MCP == nil {
		return ""
	}

	return a.MCP.Mechanism
}

// HookSettingsArgsOrDefault returns the configured claude_settings argv template
// or the built-in default, so a custom agent that selects the mechanism without
// spelling out the flags still launches correctly (the #1237 fail-safe pattern).
func (a Agent) HookSettingsArgsOrDefault() []string {
	if a.Hooks != nil && len(a.Hooks.SettingsArgs) > 0 {
		return a.Hooks.SettingsArgs
	}

	return []string{"--settings", "{path}"}
}

// HookEventArgsOrDefault returns the configured codex_config per-event argv
// template or the built-in default.
func (a Agent) HookEventArgsOrDefault() []string {
	if a.Hooks != nil && len(a.Hooks.EventArgs) > 0 {
		return a.Hooks.EventArgs
	}

	return []string{"-c", "hooks.{hook_event}={hook_value}"}
}

// HookTrustArgsOrDefault returns the configured codex_config trust argv or the
// built-in default. Unlike the other accessors an explicit empty slice is
// honoured (a custom agent may opt out of the bypass flag); only a nil/unset
// Hooks or nil TrustArgs falls back to the default.
func (a Agent) HookTrustArgsOrDefault() []string {
	if a.Hooks != nil && a.Hooks.TrustArgs != nil {
		return a.Hooks.TrustArgs
	}

	return []string{"--dangerously-bypass-hook-trust"}
}

// MCPConfigArgsOrDefault returns the configured claude_config argv template or
// the built-in default.
func (a Agent) MCPConfigArgsOrDefault() []string {
	if a.MCP != nil && len(a.MCP.ConfigArgs) > 0 {
		return a.MCP.ConfigArgs
	}

	return []string{"--mcp-config", "{path}"}
}

// MCPServerArgsOrDefault returns the configured codex_config per-server argv
// template or the built-in default.
func (a Agent) MCPServerArgsOrDefault() []string {
	if a.MCP != nil && len(a.MCP.ServerArgs) > 0 {
		return a.MCP.ServerArgs
	}

	return []string{
		"-c", "mcp_servers.{mcp_name}.command={mcp_command}",
		"-c", "mcp_servers.{mcp_name}.args={mcp_args}",
	}
}

// AgentOptionArg is one conditional argv group for an agent (see Agent.OptionArgs).
type AgentOptionArg struct {
	// When names the template variable that gates this group: the args are
	// emitted only when the variable resolves to a non-empty value ("true" for a
	// boolean such as web_search). An empty When emits the group unconditionally.
	When string `json:"when,omitempty" toml:"when"`
	// Args are the argv templates emitted when the gate passes. They are expanded
	// with the same TemplateVars as the base args plus the option variables.
	Args []string `json:"args" toml:"args"`
}

// AddDirArgsFor builds the add-directory flags granting the agent access to each
// of dirs, expanding a.AddDirArgs once per directory with {dir} bound to it (the
// rest of base is carried through so a template may also reference the usual
// vars). Empty AddDirArgs — or no directories — yields nil, so an agent whose
// CLI has no add-dir flag never has an unknown flag injected. Empty directory
// entries are skipped defensively.
func (a Agent) AddDirArgsFor(base TemplateVars, dirs []string) ([]string, error) {
	if len(a.AddDirArgs) == 0 || len(dirs) == 0 {
		return nil, nil
	}

	var out []string

	for _, d := range dirs {
		if d == "" {
			continue
		}

		v := base
		v.Dir = d

		expanded, err := ExpandSlice(a.AddDirArgs, v)
		if err != nil {
			return nil, err
		}

		out = append(out, expanded...)
	}

	return out, nil
}

// OptionArgsFor expands the agent's conditional option-arg groups against vars,
// appending each group only when its When gate resolves non-empty (an empty When
// always emits). Returns nil when no group fires, so it is safe to append
// unconditionally on every launch path. This is the config-driven replacement
// for the hard-coded codex flag adapter (issue #1236).
func (a Agent) OptionArgsFor(vars TemplateVars) ([]string, error) {
	if len(a.OptionArgs) == 0 {
		return nil, nil
	}

	lookup := vars.toMap()

	var out []string

	for _, opt := range a.OptionArgs {
		if opt.When != "" && lookup[opt.When] == "" {
			continue
		}

		expanded, err := ExpandSlice(opt.Args, vars)
		if err != nil {
			return nil, err
		}

		out = append(out, expanded...)
	}

	return out, nil
}

// CodexOptions holds typed per-session options for the Codex CLI (issue #1186).
// Each maps to a Codex flag or `-c` config override and is emitted only when set,
// so an unset field leaves Codex's own default untouched. The session model is
// tracked separately (SessionState.Model / CreateOpts.Model) and is not repeated
// here. These are Codex-specific: setting any against a non-codex agent is an
// error rather than a silent no-op. Reasoning effort and service tier are passed
// as `-c model_reasoning_effort=…` / `-c service_tier=…` because Codex has no
// dedicated flag for them; profile, web search, and approval policy have flags.
type CodexOptions struct {
	Profile         string `json:"profile,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	ServiceTier     string `json:"service_tier,omitempty"`
	WebSearch       bool   `json:"web_search,omitempty"`
	ApprovalPolicy  string `json:"approval_policy,omitempty"`
}

// IsZero reports whether no Codex option is set.
func (o CodexOptions) IsZero() bool {
	return o == CodexOptions{}
}

// Note: graith deliberately does NOT validate the *values* of these options
// (reasoning effort, service tier, approval policy). Codex owns those enums and
// they are version- and model-dependent — e.g. reasoning effort has grown to
// include `none`/`max`/`ultra` and service tier accepts a legacy `fast` alias
// across Codex releases. A closed graith allowlist would both reject values
// Codex accepts and admit ones it rejects (`auto`), so it is worse than nothing:
// Codex is the source of truth and reports a clear startup error for a bad value.
// graith only enforces its own invariant — that these are codex-only (see the
// guard in SessionManager.Create).

// HeadlessCapableEnabled reports whether this agent may run in headless
// stream-json mode. Defaults to false when unset.
func (a Agent) HeadlessCapableEnabled() bool {
	return a.HeadlessCapable != nil && *a.HeadlessCapable
}

func (a Agent) PromptInjectionEnabled() bool {
	if a.InjectPrompt != nil {
		return *a.InjectPrompt
	}

	return true
}

func (a Agent) PreTrustWorkspaceEnabled() bool {
	if a.PreTrustWorkspace != nil {
		return *a.PreTrustWorkspace
	}

	return true
}

// Valid values for [agents.<name>].prompt_injection. Each names a prompt
// delivery mechanism graith owns: append_system_prompt is Claude's
// --append-system-prompt flag, cursor_rules writes a .cursor/rules file,
// developer_instructions is Codex's -c developer_instructions override, and
// none suppresses injection. graith owns this enum (it maps to graith's own
// launch behaviour), so an unknown value is a config error rather than a
// silent no-op. See issue #1232.
const (
	PromptInjectionAppendSystemPrompt    = "append_system_prompt"
	PromptInjectionCursorRules           = "cursor_rules"
	PromptInjectionDeveloperInstructions = "developer_instructions"
	PromptInjectionNone                  = "none"
)

// ValidPromptInjection reports whether s is empty (name-based fallback) or one
// of the known prompt_injection method names.
func ValidPromptInjection(s string) bool {
	switch s {
	case "",
		PromptInjectionAppendSystemPrompt,
		PromptInjectionCursorRules,
		PromptInjectionDeveloperInstructions,
		PromptInjectionNone:
		return true
	default:
		return false
	}
}

// InterruptCountValue returns how many times the interrupt byte (Ctrl-C, 0x03)
// should be sent to interrupt this agent. Defaults to 1 when unset; a value
// below 1 is clamped to 1 so an interrupt always sends at least once.
func (a Agent) InterruptCountValue() int {
	if a.InterruptCount == nil || *a.InterruptCount < 1 {
		return 1
	}

	return *a.InterruptCount
}

// InterruptDelay returns the pause between successive interrupt bytes. Defaults
// to 0 (send back-to-back) when unset; a negative value is treated as 0.
func (a Agent) InterruptDelay() time.Duration {
	if a.InterruptDelayMs == nil || *a.InterruptDelayMs < 0 {
		return 0
	}

	return time.Duration(*a.InterruptDelayMs) * time.Millisecond
}

func (a Agent) IdleTimeoutDuration() time.Duration {
	if a.IdleTimeout == "" {
		if len(a.ResumeArgs) > 0 {
			return time.Hour
		}

		return 0
	}

	d, err := time.ParseDuration(a.IdleTimeout)
	if err != nil {
		return 0
	}

	return d
}

type SandboxConfig struct {
	Enabled  bool  `json:"enabled"            toml:"enabled"`
	Disabled *bool `json:"disabled,omitempty" toml:"disabled,omitempty"`
	// Backend selects the sandbox backend: "safehouse" (macOS only) or "nono"
	// (Linux + macOS). It has NO default — when the sandbox is enabled and
	// Backend is unset the daemon fails closed with an actionable error. This
	// is a deliberate pre-1.0 behaviour change (see the nono sandbox design doc).
	Backend string `json:"backend,omitempty" toml:"backend"`
	Command string `json:"command,omitempty" toml:"command"`
	// Profile (nono only) is the base profile graith's generated profile
	// extends. Empty means nono's built-in "default" (its audited deny groups +
	// base system paths). Set it to a maintained registry profile — e.g.
	// "always-further/claude" — to inherit that agent's upstream file grants
	// (its ~/.claude, ~/.claude.json, versioned binary dir, …) instead of
	// hand-listing them via write_files.
	//
	// nono resolves "extends" by MERGING the base profile with graith's
	// generated one. Collection fields (filesystem.allow/read,
	// environment.allow_vars, network.allow_domain, …) are UNIONED (append +
	// dedup) — graith's grants are added to, not substituted for, the base's;
	// only scalar fields (e.g. workdir.access, security.signal_mode) are
	// child-overridden. So graith's filesystem grants are always present, but
	// graith's env allowlist can only WIDEN the base profile's, it cannot narrow
	// it. A base profile that allows extra env vars, network
	// domains, set_vars, command policies, or session hooks (which run outside
	// the sandbox) therefore relaxes graith's baseline — so a custom profile is
	// only as tight as the operator has audited it to be. Choose a trusted,
	// least-privilege profile. nono's audited deny groups (deny_credentials, …)
	// are marked required and merged into every resolved profile regardless of
	// this field, so a custom base cannot silently drop the credential-deny
	// baseline. The safehouse backend has no profile concept and ignores it.
	Profile   string   `json:"profile,omitempty"    toml:"profile"`
	Features  []string `json:"features,omitempty"   toml:"features"`
	ReadDirs  []string `json:"read_dirs,omitempty"  toml:"read_dirs"`
	WriteDirs []string `json:"write_dirs,omitempty" toml:"write_dirs"`
	// ReadFiles / WriteFiles grant access to individual files rather than whole
	// directories. They exist for paths that can't be expressed as a directory
	// grant without over-sharing — most importantly single files that live
	// directly in $HOME (e.g. an agent's ~/.claude.json login file), where
	// granting the parent directory would expose unrelated secrets (.env, ssh
	// keys, tfvars). ReadFiles is read-only; WriteFiles is read+write, mirroring
	// the read_dirs / write_dirs convention (where "write" means read+write, not
	// nono's write-only mode). They map to the nono profile's
	// filesystem.read_file / filesystem.allow_file; the safehouse backend folds
	// them into its read-only / read-write path lists.
	ReadFiles  []string `json:"read_files,omitempty"  toml:"read_files"`
	WriteFiles []string `json:"write_files,omitempty" toml:"write_files"`
	// SignalMode controls whether the sandboxed process may signal other
	// processes. It maps to nono's security.signal_mode ("isolated",
	// "allow_same_sandbox", "allow_all"). Empty inherits nono's base-profile
	// default (allow_same_sandbox). safehouse ignores it. Setting "isolated"
	// makes graith's `process-control` semantics meaningful under nono (Phase 1
	// left it a no-op). See the nono sandbox design doc §C5.
	SignalMode string `json:"signal_mode,omitempty" toml:"signal_mode"`
	// Network is an optional egress policy. It maps to the nono profile's
	// network section (network.block / network.allow_domain). safehouse has no
	// network primitive and only warns. A network policy also raises the
	// enforcement floor: nono needs Landlock ABI v4 (kernel 6.7+) to filter
	// network, so a requested policy on an older kernel fails closed.
	Network *SandboxNetworkConfig `json:"network,omitempty" toml:"network"`
}

// SandboxNetworkConfig is graith's egress policy. It maps directly onto nono
// v0.66.0's profile network section: Block -> network.block, AllowDomains ->
// network.allow_domain (an L7 proxy allowlist; a plain hostname allows the
// host, a URL glob restricts to matching endpoints).
type SandboxNetworkConfig struct {
	// Block denies all outbound network access (nono is network-allowed by
	// default). Maps to network.block = true.
	Block bool `json:"block,omitempty" toml:"block"`
	// AllowDomains is the proxy allowlist. Maps to network.allow_domain. When
	// set, nono runs its L7 filtering proxy and only these domains are
	// reachable. Entries are plain hostnames or URL globs.
	AllowDomains []string `json:"allow_domains,omitempty" toml:"allow_domains"`
}

// IsSet reports whether this network policy requests any egress restriction.
// A nil or empty config requests nothing (matches nono's allow-by-default).
func (n *SandboxNetworkConfig) IsSet() bool {
	return n != nil && (n.Block || len(n.AllowDomains) > 0)
}

func MergeMCPServers(global []MCPServerConfig, overrides map[string]MCPServerConfig) []MCPServerConfig {
	byName := make(map[string]MCPServerConfig, len(global))

	var order []string

	for _, s := range global {
		if _, exists := byName[s.Name]; !exists {
			order = append(order, s.Name)
		}

		byName[s.Name] = s
	}

	names := make([]string, 0, len(overrides))
	for name := range overrides {
		names = append(names, name)
	}

	sort.Strings(names)

	for _, name := range names {
		ovr := overrides[name]
		if _, exists := byName[name]; exists {
			if ovr.Disabled {
				delete(byName, name)
				continue
			}

			base := byName[name]
			if ovr.Command != "" {
				base.Command = ovr.Command
			}

			if ovr.Args != nil {
				base.Args = ovr.Args
			}

			if ovr.Env != nil {
				base.Env = ovr.Env
			}

			byName[name] = base
		} else if !ovr.Disabled {
			ovr.Name = name
			byName[name] = ovr
			order = append(order, name)
		}
	}

	var result []MCPServerConfig

	for _, name := range order {
		if s, ok := byName[name]; ok && !s.Disabled {
			result = append(result, s)
		}
	}

	return result
}

func (s SandboxConfig) Merge(agent SandboxConfig) SandboxConfig {
	merged := SandboxConfig{
		Enabled:    s.Enabled || agent.Enabled,
		Backend:    s.Backend,
		Command:    s.Command,
		Profile:    strings.TrimSpace(s.Profile),
		SignalMode: s.SignalMode,
		Network:    s.Network,
	}

	if agent.Disabled != nil && *agent.Disabled {
		merged.Enabled = false
		return merged
	}

	merged.Features = dedup(append(s.Features, agent.Features...))
	merged.ReadDirs = dedup(append(s.ReadDirs, agent.ReadDirs...))
	merged.WriteDirs = dedup(append(s.WriteDirs, agent.WriteDirs...))
	merged.ReadFiles = dedup(append(s.ReadFiles, agent.ReadFiles...))
	merged.WriteFiles = dedup(append(s.WriteFiles, agent.WriteFiles...))

	if agent.Backend != "" {
		merged.Backend = agent.Backend
	}

	if agent.Command != "" {
		merged.Command = agent.Command
	}

	// Trim so a whitespace-only per-agent value is treated as unset (inherit the
	// global profile) rather than clobbering it — matching buildNonoProfile,
	// which trims at emission. Without this, " " would win here then silently
	// fall back to "default", discarding a valid global profile.
	if p := strings.TrimSpace(agent.Profile); p != "" {
		merged.Profile = p
	}

	if agent.SignalMode != "" {
		merged.SignalMode = agent.SignalMode
	}

	// An agent-level network policy overrides the global one wholesale (like
	// Backend/Command), rather than being merged element-wise. A network policy
	// is a single coherent posture; interleaving global+agent domains would be
	// surprising.
	if agent.Network != nil {
		merged.Network = agent.Network
	}

	return merged
}

// SandboxSignalModes are the accepted values for [sandbox] signal_mode. They
// mirror nono v0.66.0's security.signal_mode enum. Empty is also valid (inherit
// nono's base-profile default).
var SandboxSignalModes = []string{"isolated", "allow_same_sandbox", "allow_all"}

// validateSignalMode rejects an unknown signal_mode value. Empty is allowed.
func (s SandboxConfig) validateSignalMode(where string) error {
	if s.SignalMode == "" {
		return nil
	}

	for _, m := range SandboxSignalModes {
		if s.SignalMode == m {
			return nil
		}
	}

	return fmt.Errorf("%s.signal_mode %q is invalid (want one of %s)", where, s.SignalMode, strings.Join(SandboxSignalModes, ", "))
}

func (c *Config) OrchestratorSandboxMerged(agentName string) SandboxConfig {
	merged := c.Sandbox.Merge(c.Agents[agentName].Sandbox)
	orch := c.Orchestrator.Sandbox
	merged.ReadDirs = dedup(append(merged.ReadDirs, orch.ReadDirs...))
	merged.WriteDirs = dedup(append(merged.WriteDirs, orch.WriteDirs...))
	merged.ReadFiles = dedup(append(merged.ReadFiles, orch.ReadFiles...))
	merged.WriteFiles = dedup(append(merged.WriteFiles, orch.WriteFiles...))

	return merged
}

func dedup(ss []string) []string {
	if len(ss) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(ss))

	out := make([]string, 0, len(ss))
	for _, s := range ss {
		if _, ok := seen[s]; ok {
			continue
		}

		seen[s] = struct{}{}
		out = append(out, s)
	}

	return out
}

func ExpandPath(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, p[2:])
		}
	}

	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}

	return filepath.Clean(p)
}

// ExpandPathRelative resolves a configured path deterministically: it expands a
// leading ~/, and resolves a still-relative path against baseDir (the directory
// holding the config file) rather than the process working directory, then
// cleans the result. This keeps a value like [approvals.builtin] config
// resolving to the same absolute path regardless of which directory the daemon
// or CLI happens to run from. An empty (or whitespace-only) path stays empty so
// callers can distinguish "unset" from a resolved path.
func ExpandPathRelative(p, baseDir string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}

	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, p[2:])
		}
	}

	if !filepath.IsAbs(p) && baseDir != "" {
		p = filepath.Join(baseDir, p)
	}

	return filepath.Clean(p)
}

func (rc RepoConfig) Validate() error {
	if rc.Singleton && rc.AllowConcurrent {
		return fmt.Errorf("repo %q: singleton and allow_concurrent cannot both be set", rc.Path)
	}

	if err := ValidateIncludes(rc.Path, rc.Includes); err != nil {
		return fmt.Errorf("repo %q: %w", rc.Path, err)
	}

	return nil
}

// ValidateIncludes checks a set of include paths against the main repo for the
// collisions that would break the worktree/env-var layout: an include equal to
// the main repo, duplicate basenames (across the main repo and the includes),
// and generated GRAITH_INCLUDE_* env-var name collisions. Included worktrees and
// their env vars are keyed by basename, so these must be unique. Used both by
// repo-config validation and by the session-create path for scenario-supplied
// includes (issue #1046), so both surfaces reject the same footguns up front
// rather than failing with a low-level git error mid-setup.
func ValidateIncludes(mainRepoPath string, includes []string) error {
	if len(includes) == 0 {
		return nil
	}

	mainResolved := ResolvePath(mainRepoPath)
	mainBase := strings.ToLower(filepath.Base(mainResolved))
	basenames := map[string]string{mainBase: mainRepoPath}
	envNames := map[string]string{}

	for _, inc := range includes {
		resolved := ResolvePath(inc)
		if resolved == mainResolved {
			return errors.New("cannot include itself")
		}

		base := strings.ToLower(filepath.Base(resolved))
		if prev, ok := basenames[base]; ok {
			return fmt.Errorf("duplicate basename %q from %q and %q", base, prev, inc)
		}

		basenames[base] = inc

		envName := IncludeEnvVarName(filepath.Base(resolved))
		if prev, ok := envNames[envName]; ok {
			return fmt.Errorf("env var collision %s from %q and %q", envName, prev, inc)
		}

		envNames[envName] = inc
	}

	return nil
}

// validateMessagesLimits checks the [messages] operational limits (issue #1249):
// each configured cap must be positive and at or below its hard safety ceiling,
// an explicitly configured conversation page size must not exceed the
// conversation max limit, and the busy timeout must be a positive duration
// within its ceiling. A non-positive integer is left to the accessor default;
// Load derives that default from a lowered conversation max before validation.
func (c *Config) validateMessagesLimits() []error {
	var errs []error

	m := c.Messages

	for _, f := range []struct {
		name    string
		val     int
		ceiling int
	}{
		{"messages.conversation_page_size", m.ConversationPageSize, MessagesConversationMaxLimitCeiling},
		{"messages.conversation_max_limit", m.ConversationMaxLimit, MessagesConversationMaxLimitCeiling},
		{"messages.jail_list_limit", m.JailListLimit, MessagesJailListLimitCeiling},
		{"messages.subscriber_buffer", m.SubscriberBuffer, MessagesSubscriberBufferCeiling},
	} {
		if f.val > f.ceiling {
			errs = append(errs, fmt.Errorf("%s %d: must be at most %d", f.name, f.val, f.ceiling))
		}
	}

	// A positive page size larger than the max limit is an explicit contradiction
	// (a page could never be served in full). The zero value means "derive the
	// default" and is safely clamped by ConversationPageSizeOrDefault.
	if page, maxLimit := m.ConversationPageSize, m.ConversationMaxLimitOrDefault(); page > 0 && page > maxLimit {
		errs = append(errs, fmt.Errorf("messages.conversation_page_size %d: must not exceed conversation_max_limit %d", page, maxLimit))
	}

	if s := strings.TrimSpace(m.BusyTimeout); s != "" {
		if d, err := ParseDurationWithDays(s); err != nil {
			errs = append(errs, fmt.Errorf("messages.busy_timeout %q: %w", s, err))
		} else if d <= 0 {
			errs = append(errs, fmt.Errorf("messages.busy_timeout %q: must be a positive duration", s))
		} else if d > MessagesBusyTimeoutCeiling {
			errs = append(errs, fmt.Errorf("messages.busy_timeout %q: must be at most %s", s, MessagesBusyTimeoutCeiling))
		}
	}

	return errs
}

// validateTodoLimits checks the [todo] operational limits (issue #1249). The
// title/note ceilings equal the database CHECK constraints, so a configured
// value above them would be silently rejected by the database; failing at load
// makes that misconfiguration loud. Durations must be positive and within their
// ceiling.
func (c *Config) validateTodoLimits() []error {
	var errs []error

	t := c.Todo

	for _, f := range []struct {
		name    string
		val     int
		ceiling int
	}{
		{"todo.max_title", t.MaxTitle, TodoMaxTitleCeiling},
		{"todo.max_note", t.MaxNote, TodoMaxNoteCeiling},
		{"todo.list_limit", t.ListLimit, TodoListLimitCeiling},
	} {
		if f.val > f.ceiling {
			errs = append(errs, fmt.Errorf("%s %d: must be at most %d", f.name, f.val, f.ceiling))
		}
	}

	if s := strings.TrimSpace(t.SweepInterval); s != "" {
		if d, err := ParseDurationWithDays(s); err != nil {
			errs = append(errs, fmt.Errorf("todo.sweep_interval %q: %w", s, err))
		} else if d <= 0 {
			errs = append(errs, fmt.Errorf("todo.sweep_interval %q: must be a positive duration", s))
		}
	}

	if s := strings.TrimSpace(t.BusyTimeout); s != "" {
		if d, err := ParseDurationWithDays(s); err != nil {
			errs = append(errs, fmt.Errorf("todo.busy_timeout %q: %w", s, err))
		} else if d <= 0 {
			errs = append(errs, fmt.Errorf("todo.busy_timeout %q: must be a positive duration", s))
		} else if d > TodoBusyTimeoutCeiling {
			errs = append(errs, fmt.Errorf("todo.busy_timeout %q: must be at most %s", s, TodoBusyTimeoutCeiling))
		}
	}

	return errs
}

func (c *Config) Validate() error {
	var errs []error
	if c.DataDir != "" && !filepath.IsAbs(c.DataDir) && !strings.HasPrefix(c.DataDir, "~/") {
		errs = append(errs, fmt.Errorf("data_dir %q must be an absolute path or start with ~/", c.DataDir))
	}

	for _, r := range c.Repos {
		if err := r.Validate(); err != nil {
			errs = append(errs, err)
		}
	}

	if err := c.Sandbox.validateSignalMode("sandbox"); err != nil {
		errs = append(errs, err)
	}

	// Validate the approvals backend name, mode/backend conflict, and ignored
	// command key here (pure static checks). Backend *availability* (command
	// present, localmost binary on PATH, builtin config loadable) is enforced at
	// session-create by the daemon, mirroring the sandbox availability check — so
	// a missing dependency fails the session loudly without bricking daemon
	// startup.
	if err := c.Approvals.Validate(); err != nil {
		errs = append(errs, err)
	}

	if err := c.Notifications.Validate(); err != nil {
		errs = append(errs, err)
	}

	if err := c.Keybindings.Validate(); err != nil {
		errs = append(errs, err)
	}

	// Validate the [remote] block statically (fail-closed when enabled). Runtime
	// listener provisioning (tailnet IP, TLS cert) is deferred to the daemon.
	if err := c.Remote.Validate(); err != nil {
		errs = append(errs, err)
	}

	for agentName, agent := range c.Agents {
		if err := agent.Sandbox.validateSignalMode("agents." + agentName + ".sandbox"); err != nil {
			errs = append(errs, err)
		}

		if !ValidPromptInjection(agent.PromptInjection) {
			errs = append(errs, fmt.Errorf("agents.%s.prompt_injection %q: must be one of %q, %q, %q, %q (or empty for name-based detection)",
				agentName, agent.PromptInjection,
				PromptInjectionAppendSystemPrompt, PromptInjectionCursorRules,
				PromptInjectionDeveloperInstructions, PromptInjectionNone))
		}

		for i, opt := range agent.OptionArgs {
			if len(opt.Args) == 0 {
				errs = append(errs, fmt.Errorf("agents.%s.option_args[%d]: args must not be empty", agentName, i))
			}

			if opt.When != "" && !IsTemplateVar(opt.When) {
				errs = append(errs, fmt.Errorf("agents.%s.option_args[%d].when %q: not a known template variable", agentName, i, opt.When))
			}
		}

		errs = append(errs, agent.validateAdapters(agentName)...)
	}

	// default_agent must name a configured agent. mergeAgents has already unioned
	// the built-in defaults into c.Agents by the time Validate runs (see Load), so
	// this checks membership in the final merged set. A typo or a removed
	// user-defined default therefore fails loudly at load/reload rather than
	// deferring to session-create or orchestrator startup (issue #1288). An empty
	// default_agent is left to the caller's own resolution, matching the sparse/
	// default merge semantics.
	if c.DefaultAgent != "" {
		if _, ok := c.Agents[c.DefaultAgent]; !ok {
			errs = append(errs, fmt.Errorf("default_agent %q: no matching [agents.%s] entry", c.DefaultAgent, c.DefaultAgent))
		}
	}

	// An explicit orchestrator agent is another agent-name reference and must be
	// checked against the same final built-in/user merged map. Empty keeps the
	// normal inheritance from default_agent.
	if c.Orchestrator.Agent != "" {
		if _, ok := c.Agents[c.Orchestrator.Agent]; !ok {
			errs = append(errs, fmt.Errorf("orchestrator.agent %q: no matching [agents.%s] entry", c.Orchestrator.Agent, c.Orchestrator.Agent))
		}
	}

	seen := make(map[string]bool, len(c.MCPServers))
	for _, s := range c.MCPServers {
		switch {
		case s.Name == "":
			errs = append(errs, errors.New("mcp_servers: entry with empty name"))
		case seen[s.Name]:
			errs = append(errs, fmt.Errorf("mcp_servers: duplicate name %q", s.Name))
		default:
			seen[s.Name] = true
		}

		if !s.Disabled && s.Command == "" {
			errs = append(errs, fmt.Errorf("mcp_servers: %q has no command", s.Name))
		}
	}

	globalNames := make(map[string]bool, len(c.MCPServers))
	for _, s := range c.MCPServers {
		globalNames[s.Name] = true
	}

	for agentName, agent := range c.Agents {
		for name, s := range agent.MCPServers {
			if !s.Disabled && s.Command == "" && !globalNames[name] {
				errs = append(errs, fmt.Errorf("agents.%s.mcp_servers: %q has no command (not overriding a global server)", agentName, name))
			}
		}
	}

	if c.GitPull.Interval != "" {
		d, err := ParseDurationWithDays(c.GitPull.Interval)
		if err != nil {
			errs = append(errs, fmt.Errorf("git_pull.interval %q: %w", c.GitPull.Interval, err))
		} else if d < time.Minute {
			errs = append(errs, fmt.Errorf("git_pull.interval %q: must be at least 1 minute", c.GitPull.Interval))
		}
	}

	// A non-empty but unparseable retention must fail loudly rather than
	// silently falling back to the 24h default (the accessor's fail-safe): a
	// typo should tell the user, not quietly pick a window they didn't ask for.
	if strings.TrimSpace(c.Delete.Retention) != "" {
		if _, err := ParseDurationWithDays(c.Delete.Retention); err != nil {
			errs = append(errs, fmt.Errorf("delete.retention %q: %w", c.Delete.Retention, err))
		}
	}

	// Purge-loop cadence: reject an unparseable value (fail loudly rather than
	// silently using the default) and a non-positive one (a zero/negative delay
	// or interval would busy-spin the timer or defeat the coarse cadence).
	if s := strings.TrimSpace(c.Delete.PurgeStartupDelay); s != "" {
		if d, err := ParseDurationWithDays(s); err != nil {
			errs = append(errs, fmt.Errorf("delete.purge_startup_delay %q: %w", s, err))
		} else if d <= 0 {
			errs = append(errs, fmt.Errorf("delete.purge_startup_delay %q: must be a positive duration", s))
		}
	}

	if s := strings.TrimSpace(c.Delete.PurgeInterval); s != "" {
		if d, err := ParseDurationWithDays(s); err != nil {
			errs = append(errs, fmt.Errorf("delete.purge_interval %q: %w", s, err))
		} else if d <= 0 {
			errs = append(errs, fmt.Errorf("delete.purge_interval %q: must be a positive duration", s))
		}
	}

	// [gc] orphan_min_age: reject an unparseable or negative value. "0" is
	// allowed (explicit opt-out of the age floor).
	if s := strings.TrimSpace(c.GC.OrphanMinAge); s != "" {
		if d, err := ParseDurationWithDays(s); err != nil {
			errs = append(errs, fmt.Errorf("gc.orphan_min_age %q: %w", s, err))
		} else if d < 0 {
			errs = append(errs, fmt.Errorf("gc.orphan_min_age %q: must not be negative", s))
		}
	}

	// [todo]: reject an unknown emit_events mode and an unparseable duration so a
	// typo surfaces at startup rather than silently changing retention/lease
	// behaviour (mirrors delete.retention).
	if m := strings.TrimSpace(c.Todo.EmitEvents); m != "" && m != TodoEmitScenario && m != TodoEmitAll && m != TodoEmitOff {
		errs = append(errs, fmt.Errorf("todo.emit_events %q: must be one of %q, %q, %q", m, TodoEmitScenario, TodoEmitAll, TodoEmitOff))
	}

	for _, f := range []struct{ name, val string }{
		{"todo.claim_lease", c.Todo.ClaimLease},
		{"todo.retention", c.Todo.Retention},
	} {
		if strings.TrimSpace(f.val) != "" {
			if _, err := ParseDurationWithDays(f.val); err != nil {
				errs = append(errs, fmt.Errorf("%s %q: %w", f.name, f.val, err))
			}
		}
	}

	errs = append(errs, c.validateMessagesLimits()...)
	errs = append(errs, c.validateTodoLimits()...)

	for _, f := range []struct {
		name, val string
	}{
		{"pr_watch.poll_pending", c.PRWatch.PollPending},
		{"pr_watch.poll_terminal", c.PRWatch.PollTerminal},
		{"pr_watch.poll_merged", c.PRWatch.PollMerged},
		{"pr_watch.debounce", c.PRWatch.Debounce},
	} {
		if f.val == "" {
			continue
		}

		if _, err := ParseDurationWithDays(f.val); err != nil {
			errs = append(errs, fmt.Errorf("%s %q: %w", f.name, f.val, err))
		}
	}

	// Advanced PR-watch durations all pace polling, anti-flood windows, command
	// deadlines, or ref-event coalescing. None has a documented zero-disable
	// contract, so reject non-positive values on load/reload and keep the
	// accessor fallbacks only as a defence for directly-constructed configs.
	for _, f := range []struct{ name, val string }{
		{"pr_watch.advanced.base_tick", c.PRWatch.Advanced.BaseTick},
		{"pr_watch.advanced.no_pr_negative_cache", c.PRWatch.Advanced.NoPRNegativeCache},
		{"pr_watch.advanced.notification_rate_window", c.PRWatch.Advanced.NotificationRateWindow},
		{"pr_watch.advanced.untrusted_author_prompt_window", c.PRWatch.Advanced.UntrustedAuthorPromptWindow},
		{"pr_watch.advanced.kick_cooldown", c.PRWatch.Advanced.KickCooldown},
		{"pr_watch.advanced.kicked_no_pr_backoff", c.PRWatch.Advanced.KickedNoPRBackoff},
		{"pr_watch.advanced.ref_reconcile_interval", c.PRWatch.Advanced.RefReconcileInterval},
		{"pr_watch.advanced.ref_debounce", c.PRWatch.Advanced.RefDebounce},
		{"pr_watch.advanced.gh_timeout", c.PRWatch.Advanced.GHTimeout},
	} {
		validatePositiveDurationField(&errs, f.name, f.val)
	}

	// [orchestrator.restart]: every configured duration must be positive. In
	// geometric mode the initial delay cannot exceed its cap; explicit schedule
	// entries must be nondecreasing so later crashes never reduce the restart
	// floor. Accessors retain positive fallbacks for direct struct construction.
	rc := c.Orchestrator.Restart
	for _, f := range []struct{ name, val string }{
		{"orchestrator.restart.initial_backoff", rc.InitialBackoff},
		{"orchestrator.restart.max_backoff", rc.MaxBackoff},
		{"orchestrator.restart.stable_reset", rc.StableReset},
	} {
		validatePositiveDurationField(&errs, f.name, f.val)
	}

	initial, initialErr := ParseDurationWithDays(rc.InitialBackoff)
	maxBackoff, maxErr := ParseDurationWithDays(rc.MaxBackoff)

	if strings.TrimSpace(rc.InitialBackoff) != "" && strings.TrimSpace(rc.MaxBackoff) != "" &&
		initialErr == nil && maxErr == nil && initial > 0 && maxBackoff > 0 && initial > maxBackoff {
		errs = append(errs, fmt.Errorf("orchestrator.restart.initial_backoff %q: must not exceed max_backoff %q", rc.InitialBackoff, rc.MaxBackoff))
	}

	var previous time.Duration

	havePrevious := false

	for i, s := range rc.Schedule {
		d, err := ParseDurationWithDays(s)
		if err != nil {
			errs = append(errs, fmt.Errorf("orchestrator.restart.schedule[%d] %q: %w", i, s, err))
			continue
		}

		if d <= 0 {
			errs = append(errs, fmt.Errorf("orchestrator.restart.schedule[%d] %q: must be greater than zero", i, s))
			continue
		}

		if havePrevious && d < previous {
			errs = append(errs, fmt.Errorf("orchestrator.restart.schedule[%d] %q: must not be less than the previous delay %s", i, s, previous))
		}

		previous = d
		havePrevious = true
	}

	// [updates]: a non-empty but unparseable interval/timeout must fail loudly
	// rather than silently falling back to the accessor default (mirrors
	// git_pull.interval and delete.retention). A repository that is set but not
	// in "owner/repo" form is rejected so a typo can't send the check at a
	// URL the user didn't intend.
	for _, f := range []struct{ name, val string }{
		{"updates.interval", c.Updates.Interval},
		{"updates.timeout", c.Updates.Timeout},
	} {
		if strings.TrimSpace(f.val) == "" {
			continue
		}

		if d, err := ParseDurationWithDays(f.val); err != nil {
			errs = append(errs, fmt.Errorf("%s %q: %w", f.name, f.val, err))
		} else if d <= 0 {
			errs = append(errs, fmt.Errorf("%s %q: must be greater than zero", f.name, f.val))
		}
	}

	if repo := strings.TrimSpace(c.Updates.Repository); repo != "" {
		if owner, name, ok := strings.Cut(repo, "/"); !ok || owner == "" || name == "" || strings.Contains(name, "/") {
			errs = append(errs, fmt.Errorf("updates.repository %q: must be in \"owner/repo\" form", c.Updates.Repository))
		}
	}

	// [status] ttl and [config] reload_debounce: a non-empty but unparseable
	// value must fail loudly rather than silently falling back to the accessor
	// default (mirrors delete.retention). reload_debounce must also be positive —
	// a zero/negative debounce would busy-loop the config watcher.
	if s := strings.TrimSpace(c.Status.TTL); s != "" {
		if _, err := ParseDurationWithDays(s); err != nil {
			errs = append(errs, fmt.Errorf("status.ttl %q: %w", s, err))
		}
	}

	if s := strings.TrimSpace(c.ConfigReload.ReloadDebounce); s != "" {
		if d, err := ParseDurationWithDays(s); err != nil {
			errs = append(errs, fmt.Errorf("config.reload_debounce %q: %w", s, err))
		} else if d <= 0 {
			errs = append(errs, fmt.Errorf("config.reload_debounce %q: must be a positive duration", s))
		}
	}

	// [terminal] refresh_interval: a non-empty but unparseable value must fail
	// loudly rather than silently falling back to the accessor default, and it
	// must be positive — a zero/negative cadence would busy-loop the refresh
	// tick. The integer summary_width field self-clamps in its accessor (a
	// non-positive value simply means "use the default"), so it needs no
	// load-time rejection.
	if s := strings.TrimSpace(c.Terminal.RefreshInterval); s != "" {
		if d, err := ParseDurationWithDays(s); err != nil {
			errs = append(errs, fmt.Errorf("terminal.refresh_interval %q: %w", s, err))
		} else if d <= 0 {
			errs = append(errs, fmt.Errorf("terminal.refresh_interval %q: must be a positive duration", s))
		}
	}

	// [git] operation timeouts: a non-empty but unparseable or non-positive
	// value must fail loudly rather than silently falling back to the accessor
	// default (mirrors updates.interval). A zero/negative timeout would cancel
	// the operation immediately.
	for _, f := range []struct{ name, val string }{
		{"git.fetch_timeout", c.Git.FetchTimeout},
		{"git.merge_timeout", c.Git.MergeTimeout},
		{"git.username_timeout", c.Git.UsernameTimeout},
	} {
		if strings.TrimSpace(f.val) == "" {
			continue
		}

		if d, err := ParseDurationWithDays(f.val); err != nil {
			errs = append(errs, fmt.Errorf("%s %q: %w", f.name, f.val, err))
		} else if d <= 0 {
			errs = append(errs, fmt.Errorf("%s %q: must be greater than zero", f.name, f.val))
		}
	}

	// [headless] control_timeout / interrupt_timeout and [migration]
	// health_window: a non-empty but unparseable or non-positive duration must
	// fail loudly rather than silently falling back to its accessor default
	// (issue #1250). A zero/negative timeout or health window has no sensible
	// meaning for these round-trips/waits.
	for _, f := range []struct{ name, val string }{
		{"headless.control_timeout", c.Headless.ControlTimeout},
		{"headless.interrupt_timeout", c.Headless.InterruptTimeout},
		{"migration.health_window", c.Migration.HealthWindow},
	} {
		if strings.TrimSpace(f.val) == "" {
			continue
		}

		if d, err := ParseDurationWithDays(f.val); err != nil {
			errs = append(errs, fmt.Errorf("%s %q: %w", f.name, f.val, err))
		} else if d <= 0 {
			errs = append(errs, fmt.Errorf("%s %q: must be a positive duration", f.name, f.val))
		}
	}

	// [headless] and [transcript] byte-size limits: a negative value is a config
	// error (a zero means "use the default"). Guards against a typo silently
	// disabling a safety cap (issue #1250).
	for _, f := range []struct {
		name string
		val  int
	}{
		{"headless.max_line_bytes", c.Headless.MaxLineBytes},
		{"headless.preview_bytes", c.Headless.PreviewBytes},
		{"transcript.max_context_bytes", c.Transcript.MaxContextBytes},
		{"transcript.max_tool_output_bytes", c.Transcript.MaxToolOutputBytes},
		{"transcript.max_line_bytes", c.Transcript.MaxLineBytes},
		{"transcript.max_metadata_line_bytes", c.Transcript.MaxMetadataLineBytes},
	} {
		if f.val < 0 {
			errs = append(errs, fmt.Errorf("%s %d: must not be negative", f.name, f.val))
		}
	}

	// [tools]: validate explicit executable overrides so a bad path/name fails
	// at startup, not at the first git/gh/notification call. Unset defaults are
	// skipped (they keep PATH-lookup semantics).
	if err := c.Tools.Validate(); err != nil {
		errs = append(errs, err)
	}

	// [connection] deadlines/intervals: a non-empty but unparseable or
	// non-positive value must fail loudly rather than silently falling back to
	// the accessor default (mirrors git timeouts). A zero/negative dial or
	// handshake timeout would abort the connection immediately; a zero poll
	// interval would busy-loop.
	for _, f := range []struct{ name, val string }{
		{"connection.dial_timeout", c.Connection.DialTimeout},
		{"connection.handshake_timeout", c.Connection.HandshakeTimeout},
		{"connection.start_timeout", c.Connection.StartTimeout},
		{"connection.start_poll_interval", c.Connection.StartPollInterval},
		{"connection.reconnect_timeout", c.Connection.ReconnectTimeout},
		{"connection.reconnect_interval", c.Connection.ReconnectInterval},
		{"connection.remote_dial_timeout", c.Connection.RemoteDialTimeout},
		{"connection.remote_handshake_timeout", c.Connection.RemoteHandshakeTimeout},
		{"connection.remote_pairing_timeout", c.Connection.RemotePairingTimeout},
	} {
		if strings.TrimSpace(f.val) == "" {
			continue
		}

		if d, err := ParseDurationWithDays(f.val); err != nil {
			errs = append(errs, fmt.Errorf("%s %q: %w", f.name, f.val, err))
		} else if d <= 0 {
			errs = append(errs, fmt.Errorf("%s %q: must be greater than zero", f.name, f.val))
		}
	}

	// [launch] watchdog/slot cadences and [lifecycle] escalation waits: a
	// non-empty but unparseable or non-positive value must fail loudly rather than
	// silently falling back to the accessor default. The signal-escalation ORDER
	// is a code invariant; these only bound the waits between steps. A zero/
	// negative wait would either escalate instantly (skipping a gentler stop) or
	// busy-loop a poll.
	for _, f := range []struct{ name, val string }{
		{"launch.watchdog_interval", c.Launch.WatchdogInterval},
		{"launch.slot_poll_interval", c.Launch.SlotPollInterval},
		{"lifecycle.convert_settle_timeout", c.Lifecycle.ConvertSettleTimeout},
		{"lifecycle.convert_kill_timeout", c.Lifecycle.ConvertKillTimeout},
		{"lifecycle.convert_force_kill_timeout", c.Lifecycle.ConvertForceKillTimeout},
		{"lifecycle.mass_exit_window", c.Lifecycle.MassExitWindow},
		{"lifecycle.process_kill_grace", c.Lifecycle.ProcessKillGrace},
		{"lifecycle.adopted_timeout", c.Lifecycle.AdoptedTimeout},
		{"lifecycle.adopted_poll_interval", c.Lifecycle.AdoptedPollInterval},
		{"lifecycle.input_delay", c.Lifecycle.InputDelay},
	} {
		validatePositiveDurationField(&errs, f.name, f.val)
	}

	// [lifecycle] default geometry must fit a terminal winsize (uint16). A value
	// beyond that would silently wrap when narrowed, so reject it at load.
	for _, f := range []struct {
		name string
		val  int
	}{
		{"lifecycle.default_cols", c.Lifecycle.DefaultCols},
		{"lifecycle.default_rows", c.Lifecycle.DefaultRows},
	} {
		if f.val > 65535 {
			errs = append(errs, fmt.Errorf("%s %d: must be at most 65535", f.name, f.val))
		}
	}

	errs = append(errs, c.validateTriggers()...)

	return errors.Join(errs...)
}

func IncludeEnvVarName(repoBasename string) string {
	name := strings.ToUpper(repoBasename)
	name = strings.NewReplacer("-", "_", ".", "_").Replace(name)

	var clean []rune

	for _, r := range name {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			clean = append(clean, r)
		}
	}

	return "GRAITH_INCLUDE_" + string(clean) + "_PATH"
}

func (c *Config) FindRepo(repoPath string) (RepoConfig, bool) {
	repoPath = ResolvePath(repoPath)
	for _, r := range c.Repos {
		if ResolvePath(r.Path) == repoPath {
			return r, true
		}
	}

	return RepoConfig{}, false
}

func (c *Config) RepoPathAllowed(repoPath string) bool {
	if len(c.AllowedRepoPaths) == 0 {
		return true
	}

	repoPath = ResolvePath(repoPath)
	for _, allowed := range c.AllowedRepoPaths {
		prefix := ResolvePath(allowed)
		if repoPath == prefix || strings.HasPrefix(repoPath, prefix+string(filepath.Separator)) {
			return true
		}
	}

	return false
}

// AvailableRepoPaths returns the repo paths the orchestrator may use, combining
// the allowed_repo_paths list and the [[repos]] entries with ~ expanded, in
// config order and de-duplicated. It returns nil when none are configured.
func (c *Config) AvailableRepoPaths() []string {
	var paths []string

	seen := make(map[string]bool)

	add := func(p string) {
		if p == "" {
			return
		}

		expanded := ExpandPath(p)
		if seen[expanded] {
			return
		}

		seen[expanded] = true
		paths = append(paths, expanded)
	}

	for _, p := range c.AllowedRepoPaths {
		add(p)
	}

	for _, r := range c.Repos {
		add(r.Path)
	}

	return paths
}

func ResolvePath(p string) string {
	p = ExpandPath(p)
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		p = resolved
	}

	return p
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	cfg := Default()

	defaultAgents := cfg.Agents
	if err := toml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing config %s: %w", path, err)
	}

	cfg.Agents = mergeAgents(defaultAgents, cfg.Agents)
	applyConversationLimitDefaults(cfg, data)

	// Resolve relative [tools] paths against the directory holding config.toml so
	// validation and every execution site use the identical absolute path (issue
	// #1293). Do this before Validate so a normalized path is what gets checked.
	configDir := filepath.Dir(path)
	if abs, err := filepath.Abs(configDir); err == nil {
		configDir = abs
	}

	cfg.Tools = cfg.Tools.NormalizeRelative(configDir)

	applyPRWatchCommentCompat(cfg, data)

	// go-toml/v2's default Unmarshal silently ignores unknown keys, so a typo'd
	// key under [approvals.builtin] (e.g. "dney" for "deny") would be dropped —
	// a fail-open for an approvals deny-list. Reject unknown keys in that subtree
	// specifically (scoped, so the rest of the config keeps its leniency).
	if err := checkApprovalsBuiltinKeys(data); err != nil {
		return nil, fmt.Errorf("config validation: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config validation: %w", err)
	}

	cfg.Warnings = append(cfg.Warnings, cfg.Keybindings.Conflicts()...)

	return cfg, nil
}

// applyPRWatchCommentCompat preserves the pre-split meaning of
// notify_review_comments for existing configs. Before notify_pr_comments
// existed, notify_review_comments gated BOTH inline review comments and
// issue-style PR conversation comments. So that upgrading doesn't silently drop
// a whole notification class, a config that enables notify_review_comments but
// never mentions notify_pr_comments keeps delivering conversation comments. A
// config that sets notify_pr_comments explicitly (to either value) has opted
// into the granular gates and is left untouched — including the fresh-install
// default, where the embedded default TOML sets the key.
func applyPRWatchCommentCompat(cfg *Config, data []byte) {
	if !cfg.PRWatch.NotifyReviewComments {
		return
	}

	if tomlHasKey(data, "pr_watch", "notify_pr_comments") {
		return
	}

	cfg.PRWatch.NotifyPRComments = true
}

// applyConversationLimitDefaults derives the page-size default after applying
// a sparse user overlay. The embedded page size is 500, but when a user lowers
// only conversation_max_limit, the inherited default must clamp to that new
// maximum. A positive explicit page size remains untouched so Validate can
// reject an intentional contradiction. Non-positive values retain their
// documented "use the default" semantics and are materialized here too.
func applyConversationLimitDefaults(cfg *Config, data []byte) {
	if tomlHasKey(data, "messages", "conversation_page_size") && cfg.Messages.ConversationPageSize > 0 {
		return
	}

	cfg.Messages.ConversationPageSize = cfg.Messages.ConversationPageSizeOrDefault()
}

// tomlHasKey reports whether the raw TOML sets table.key. It distinguishes an
// explicitly-set value from an absent one, which a typed unmarshal into a plain
// bool cannot. Structural errors are ignored here — they surface from the typed
// Unmarshal in Load instead.
func tomlHasKey(data []byte, table, key string) bool {
	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		return false
	}

	t, ok := raw[table].(map[string]any)
	if !ok {
		return false
	}

	_, ok = t[key]

	return ok
}

// approvalsBuiltinKeys is the set of valid keys under [approvals.builtin].
var approvalsBuiltinKeys = map[string]struct{}{
	"config": {}, "allow": {}, "deny": {}, "allowSafeXargs": {}, "askNoninteractive": {},
}

// checkApprovalsBuiltinKeys rejects unknown keys under [approvals.builtin] so a
// misspelled key can't be silently dropped (a fail-open for a deny-list). It
// does a targeted generic decode rather than DisallowUnknownFields on the whole
// document, keeping the rest of the config forwards-compatible.
func checkApprovalsBuiltinKeys(data []byte) error {
	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		return nil // structural errors surface from the typed Unmarshal instead
	}

	approvals, ok := raw["approvals"].(map[string]any)
	if !ok {
		return nil
	}

	builtin, ok := approvals["builtin"].(map[string]any)
	if !ok {
		return nil
	}

	for k := range builtin {
		if _, ok := approvalsBuiltinKeys[k]; !ok {
			return fmt.Errorf("[approvals.builtin] unknown key %q (valid: config, allow, deny, allowSafeXargs, askNoninteractive)", k)
		}
	}

	return nil
}

// UnknownKey is a config key that graith's schema does not recognise. It is a
// diagnostic aid (surfaced by `gr doctor`), not a load error: the runtime load
// stays lenient so an older daemon won't refuse a config written for a newer
// graith, and a typo silently drops the key rather than bricking startup. See
// issue #720.
type UnknownKey struct {
	// Table is the dotted parent-table path, e.g. "agents.claude.sandbox".
	// Empty for top-level keys.
	Table string
	// Name is the unrecognised leaf key, e.g. "read_dir".
	Name string
	// Suggestion is the closest known key in the same table, or "" if none is
	// close enough to be worth a "did you mean".
	Suggestion string
}

// FullKey renders the key with its table prefix, e.g. "sandbox.read_dir".
func (u UnknownKey) FullKey() string {
	if u.Table == "" {
		return u.Name
	}

	return u.Table + "." + u.Name
}

// UnknownKeys parses the TOML at path and reports keys that don't map to any
// field in the Config schema — typos (read_dir vs read_dirs), keys under the
// wrong table, or options from a newer graith than this binary. Unknown keys
// are never returned as an error; the returned error is only for a missing,
// unreadable, or unparseable file.
func UnknownKeys(path string) ([]UnknownKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	return unknownKeysFromTOML(data)
}

func unknownKeysFromTOML(data []byte) ([]UnknownKey, error) {
	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	var found []UnknownKey

	collectUnknownKeys(reflect.TypeOf(Config{}), raw, "", &found)

	// Array-of-tables entries share a table path, so the same key can surface
	// more than once (e.g. a typo repeated across [[repos]] blocks). Dedupe by
	// full key so doctor reports each unknown key once.
	seen := make(map[string]bool, len(found))
	out := found[:0]

	for _, u := range found {
		if seen[u.FullKey()] {
			continue
		}

		seen[u.FullKey()] = true

		out = append(out, u)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].FullKey() < out[j].FullKey() })

	return out, nil
}

// collectUnknownKeys walks a decoded TOML table against the struct type that
// models it, recording keys that have no matching field and recursing into
// nested tables, arrays of tables, and dynamic-key maps (e.g. [agents.<name>]).
func collectUnknownKeys(t reflect.Type, raw map[string]any, table string, out *[]UnknownKey) {
	t = derefType(t)
	if t.Kind() != reflect.Struct {
		return
	}

	fields, known := tomlFields(t)

	for key, val := range raw {
		ft, ok := fields[strings.ToLower(key)]
		if !ok {
			*out = append(*out, UnknownKey{
				Table:      table,
				Name:       key,
				Suggestion: closestKey(key, known),
			})

			continue
		}

		recurseUnknownKeys(ft, val, joinTable(table, key), out)
	}
}

func recurseUnknownKeys(ft reflect.Type, val any, table string, out *[]UnknownKey) {
	switch derefType(ft).Kind() {
	case reflect.Struct:
		if m, ok := val.(map[string]any); ok {
			collectUnknownKeys(ft, m, table, out)
		}
	case reflect.Map:
		// Dynamic keys (e.g. agents.<name>): the key itself is user-defined, so
		// recurse into the map's value type for each entry.
		if m, ok := val.(map[string]any); ok {
			for k, v := range m {
				recurseUnknownKeys(derefType(ft).Elem(), v, joinTable(table, k), out)
			}
		}
	case reflect.Slice, reflect.Array:
		// Arrays of tables ([[repos]]) decode to a slice of tables. Iterate
		// reflectively so both []any and []map[string]any element shapes work.
		rv := reflect.ValueOf(val)
		if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
			return
		}

		for i := 0; i < rv.Len(); i++ {
			recurseUnknownKeys(derefType(ft).Elem(), rv.Index(i).Interface(), table, out)
		}
	}
}

// tomlFields returns, for a struct type, a lookup from lowercased TOML key to
// field type (matching is case-insensitive, mirroring go-toml's leniency so we
// don't flag a key the loader would actually accept) and the canonical key
// names for "did you mean" suggestions.
func tomlFields(t reflect.Type) (map[string]reflect.Type, []string) {
	lookup := make(map[string]reflect.Type, t.NumField())
	names := make([]string, 0, t.NumField())

	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" { // unexported
			continue
		}

		name := strings.Split(f.Tag.Get("toml"), ",")[0]
		if name == "-" {
			continue
		}

		if name == "" {
			name = f.Name
		}

		lookup[strings.ToLower(name)] = f.Type
		names = append(names, name)
	}

	return lookup, names
}

func joinTable(table, key string) string {
	if table == "" {
		return key
	}

	return table + "." + key
}

func derefType(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	return t
}

// closestKey returns the known key nearest to key by edit distance, or "" if
// none is close enough to be a plausible typo.
func closestKey(key string, known []string) string {
	lowKey := strings.ToLower(key)
	best := ""
	bestDist := -1

	for _, k := range known {
		d := levenshtein(lowKey, strings.ToLower(k))
		if bestDist == -1 || d < bestDist {
			bestDist = d
			best = k
		}
	}

	// Only offer a suggestion when the nearest key is a plausible typo: up to a
	// third of the key's length in edits, floored at 2 (so short keys like
	// "arg"/"args" still match) and capped at 3. The cap matters for forward
	// compatibility — a genuinely new, long key from a newer graith should warn
	// as unknown without a misleading "did you mean" pointing at an unrelated
	// existing key. Length is measured in runes to match levenshtein's unit.
	maxDist := len([]rune(key)) / 3
	if maxDist < 2 {
		maxDist = 2
	}

	if maxDist > 3 {
		maxDist = 3
	}

	if bestDist >= 0 && bestDist <= maxDist {
		return best
	}

	return ""
}

func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	curr := make([]int, len(rb)+1)

	for j := range prev {
		prev[j] = j
	}

	for i := 1; i <= len(ra); i++ {
		curr[0] = i

		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}

			curr[j] = min(prev[j]+1, curr[j-1]+1, prev[j-1]+cost)
		}

		prev, curr = curr, prev
	}

	return prev[len(rb)]
}

func mergeAgents(defaults, user map[string]Agent) map[string]Agent {
	merged := make(map[string]Agent, len(defaults)+len(user))
	for name, def := range defaults {
		merged[name] = def
	}

	for name, usr := range user {
		def, hasDefault := merged[name]
		if !hasDefault {
			merged[name] = usr
			continue
		}

		merged[name] = mergeAgent(def, usr)
	}

	return merged
}

func mergeAgent(def, usr Agent) Agent {
	if usr.Command != "" {
		def.Command = usr.Command
	}

	if usr.Args != nil {
		def.Args = usr.Args
	}

	if usr.ResumeArgs != nil {
		def.ResumeArgs = usr.ResumeArgs
	}

	if usr.ForkArgs != nil {
		def.ForkArgs = usr.ForkArgs
	}

	if usr.Env != nil {
		def.Env = usr.Env
	}

	if usr.IdleTimeout != "" {
		def.IdleTimeout = usr.IdleTimeout
	}

	// A whitespace-only Profile is treated as unset here too, matching
	// SandboxConfig.Merge and buildNonoProfile. Otherwise a whitespace typo would
	// trip this override predicate and replace the embedded default sandbox
	// (dropping default grants like Claude's ~/.claude), even though the profile
	// itself is later trimmed to unset — a fail-closed but surprising regression.
	if usr.Sandbox.Enabled || usr.Sandbox.Disabled != nil || usr.Sandbox.Backend != "" ||
		usr.Sandbox.Command != "" || strings.TrimSpace(usr.Sandbox.Profile) != "" || usr.Sandbox.Features != nil ||
		usr.Sandbox.ReadDirs != nil || usr.Sandbox.WriteDirs != nil ||
		usr.Sandbox.ReadFiles != nil || usr.Sandbox.WriteFiles != nil ||
		usr.Sandbox.SignalMode != "" || usr.Sandbox.Network != nil {
		def.Sandbox = usr.Sandbox
	}

	if usr.InjectPrompt != nil {
		def.InjectPrompt = usr.InjectPrompt
	}

	if usr.PromptInjection != "" {
		def.PromptInjection = usr.PromptInjection
	}

	if usr.PreTrustWorkspace != nil {
		def.PreTrustWorkspace = usr.PreTrustWorkspace
	}

	if usr.MCPServers != nil {
		def.MCPServers = usr.MCPServers
	}

	if usr.ValidateModel != "" {
		def.ValidateModel = usr.ValidateModel
	}

	if usr.InterruptCount != nil {
		def.InterruptCount = usr.InterruptCount
	}

	if usr.InterruptDelayMs != nil {
		def.InterruptDelayMs = usr.InterruptDelayMs
	}

	if usr.HeadlessCapable != nil {
		def.HeadlessCapable = usr.HeadlessCapable
	}

	if usr.AddDirArgs != nil {
		def.AddDirArgs = usr.AddDirArgs
	}

	if usr.HeadlessArgs != nil {
		def.HeadlessArgs = usr.HeadlessArgs
	}

	if usr.OptionArgs != nil {
		def.OptionArgs = usr.OptionArgs
	}

	if usr.Hooks != nil {
		def.Hooks = usr.Hooks
	}

	if usr.MCP != nil {
		def.MCP = usr.MCP
	}

	if usr.PromptInjectionArgs != nil {
		def.PromptInjectionArgs = usr.PromptInjectionArgs
	}

	if usr.EmptyIDResumeArgs != nil {
		def.EmptyIDResumeArgs = usr.EmptyIDResumeArgs
	}

	return def
}

func Default() *Config {
	cfg := &Config{}
	if err := toml.Unmarshal(defaultConfigTOML, cfg); err != nil {
		panic("invalid embedded default config: " + err.Error())
	}

	return cfg
}

func DefaultAgentPrompt() string {
	return Default().AgentPrompt
}

func DefaultTOML() []byte {
	out := make([]byte, len(defaultConfigTOML))
	copy(out, defaultConfigTOML)

	return out
}

func LoadOrDefault(path string) (*Config, error) {
	profile, _, err := ResolveProfile()
	if err != nil {
		return nil, err
	}

	if path == "" {
		p, err := ResolvePaths()
		if err != nil {
			return nil, err
		}

		path = p.ConfigFile
	}

	cfg, err := Load(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if profile == "" {
				if legacy := legacyConfigFile(); legacy != "" {
					if lcfg, lerr := Load(legacy); lerr == nil {
						return lcfg, nil
					}
				}
			}

			return Default(), nil
		}

		return nil, err
	}

	return cfg, nil
}

// ResolveConfigPath returns the config file that LoadOrDefault(explicit) would
// read and whether that file exists on disk. When explicit is set it is used
// verbatim. When empty, resolution mirrors LoadOrDefault: the profile/XDG path,
// falling back to the legacy macOS path only when the XDG file is absent and no
// profile is active. Diagnostics (e.g. gr doctor) use this so the reported and
// inspected file is the same one the CLI/daemon actually load.
func ResolveConfigPath(explicit string) (path string, exists bool, err error) {
	if explicit != "" {
		_, statErr := os.Stat(explicit)
		return explicit, statErr == nil, nil
	}

	profile, _, err := ResolveProfile()
	if err != nil {
		return "", false, err
	}

	p, err := ResolvePaths()
	if err != nil {
		return "", false, err
	}

	if _, statErr := os.Stat(p.ConfigFile); statErr == nil {
		return p.ConfigFile, true, nil
	}

	if profile == "" {
		if legacy := legacyConfigFile(); legacy != "" {
			if _, statErr := os.Stat(legacy); statErr == nil {
				return legacy, true, nil
			}
		}
	}

	return p.ConfigFile, false, nil
}

// legacyConfigFile returns the old macOS config path (~/Library/Application
// Support/graith/config.toml) if it differs from the current path.
func legacyConfigFile() string {
	legacy := filepath.Join(xdg.ConfigHome, baseAppName, "config.toml")

	current := filepath.Join(configHome(), baseAppName, "config.toml")
	if legacy == current {
		return ""
	}

	return legacy
}
