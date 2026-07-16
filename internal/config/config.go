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
	Triggers         []TriggerConfig    `toml:"trigger"`   // [[trigger]] array
	TriggersRuntime  TriggersRuntime    `toml:"triggers"`  // [triggers] table (daemon-wide settings)
	Headless         HeadlessConfig     `toml:"headless"`  // [headless] table (issue #1075)
	Updates          UpdatesConfig      `toml:"updates"`   // [updates] table (issue #1253)
	Detection        DetectionConfig    `toml:"detection"` // [detection] table (issue #1241)
	ConfigReload     ConfigReload       `toml:"config"`    // [config] table (issue #1237)
	Tools            ToolsConfig        `toml:"tools"`     // [tools] table (issue #1238)
	Git              GitConfig          `toml:"git"`       // [git] table (issue #1238)
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

// HeadlessConfig is the [headless] block gating headless stream-json sessions
// (issue #1075). Headless is inert unless Experimental is true — the control
// protocol it uses is an SDK-internal contract, so v1 is opt-in and
// experimental. Default, when Experimental is on, decides whether new sessions
// go headless without an explicit --headless.
type HeadlessConfig struct {
	Experimental bool `toml:"experimental"`
	Default      bool `toml:"default"`
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
// parse or are negative. It returns nil when Schedule is empty or nothing valid
// survives, signalling geometric mode to callers.
func (r OrchestratorRestartConfig) parsedSchedule() []time.Duration {
	if len(r.Schedule) == 0 {
		return nil
	}

	out := make([]time.Duration, 0, len(r.Schedule))

	for _, s := range r.Schedule {
		d, err := ParseDurationWithDays(s)
		if err != nil || d < 0 {
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

	initial := durationOrDefault(r.InitialBackoff, OrchestratorInitialBackoffDefault)
	maxDelay := durationOrDefault(r.MaxBackoff, OrchestratorMaxBackoffDefault)

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
// backoff level. Empty or unparseable uses OrchestratorStableResetDefault.
func (r OrchestratorRestartConfig) StableResetDuration() time.Duration {
	return durationOrDefault(r.StableReset, OrchestratorStableResetDefault)
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
// unparseable, or negative.
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
}

// Launch tuning defaults. MaxConcurrent defaults to 3 because the #1092
// evidence showed ~4 concurrent startups completing fine while the 5th stalled.
const (
	LaunchMaxConcurrentDefault  = 3
	LaunchStartupTimeoutDefault = 3 * time.Minute
	LaunchSettleTimeoutDefault  = 10 * time.Second
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
// back to the daemon's historical default when unset (or non-positive / invalid).
// Keeping the default in one place here — not in the daemon — mirrors the poll/
// debounce accessors above and keeps the embedded default_config.toml free to omit
// the keys (so the Go fallback stays authoritative; see issue #1228).

// BaseTickDuration is the base poll-loop cadence. Default 15s.
func (p PRWatchConfig) BaseTickDuration() time.Duration {
	return parseDurationOr(p.Advanced.BaseTick, 15*time.Second)
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
	return parseDurationOr(p.Advanced.NoPRNegativeCache, 5*time.Minute)
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
	return parseDurationOr(p.Advanced.NotificationRateWindow, 30*time.Minute)
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
	return parseDurationOr(p.Advanced.UntrustedAuthorPromptWindow, 30*time.Minute)
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
	return parseDurationOr(p.Advanced.KickCooldown, 3*time.Second)
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
	return parseDurationOr(p.Advanced.KickedNoPRBackoff, 20*time.Second)
}

// RefReconcileIntervalDuration is the git-ref watcher reconcile cadence. Default 2s.
func (p PRWatchConfig) RefReconcileIntervalDuration() time.Duration {
	return parseDurationOr(p.Advanced.RefReconcileInterval, 2*time.Second)
}

// RefDebounceDuration coalesces a burst of ref writes into one kick. Default 750ms.
func (p PRWatchConfig) RefDebounceDuration() time.Duration {
	return parseDurationOr(p.Advanced.RefDebounce, 750*time.Millisecond)
}

// GHTimeoutDuration is the per-`gh`-command timeout. Default 5s.
func (p PRWatchConfig) GHTimeoutDuration() time.Duration {
	return parseDurationOr(p.Advanced.GHTimeout, 5*time.Second)
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

type Messages struct {
	MaxAge       string `toml:"max_age"`
	MaxPerStream int    `toml:"max_per_stream"`
}

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
}

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
	// enclosing human/headless approval Timeout so a hung backend cannot outlive
	// the deadline that encloses it — a class of bug that previously caused
	// approval-behaviour glitches (see #244). Validate enforces this hierarchy.
	CommandTimeout   string `toml:"command_timeout"`
	LocalmostTimeout string `toml:"localmost_timeout"`
}

// Backend execution timeout bounds. A backend's automated decision runs *inside*
// the enclosing approval deadline (the human queue wait, or the headless
// caller-side ctx), so the backend's own subprocess timeout must be shorter than
// that enclosing deadline to stay coherent. defaultBackendExecTimeout preserves
// the historical fixed 5s; maxBackendExecTimeout caps how long a single backend
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
// enclosing approval Timeout. The last check enforces the deadline hierarchy — a
// backend decision runs inside the human/headless approval deadline, so a
// backend timeout at or above it is incoherent and can cause the approval
// glitches #244 tracked. Explicitly-set fields are always checked; the resolved
// backend's effective timeout (including the 5s default) is also checked so a
// deliberately tiny [approvals] timeout is caught against the default backend
// bound too.
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
				"[approvals] %s=%q must be shorter than the enclosing approval timeout=%s "+
					"so a hung backend cannot outlive the approval deadline (see #244)",
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
				"the enclosing approval timeout=%s; raise [approvals] timeout or lower the backend timeout (see #244)",
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
	// (issue #1075). Unset means not capable — only agents explicitly flagged
	// (Claude Code in v1) may run headless, so a --headless request against an
	// unsupported agent fails closed rather than silently downgrading.
	HeadlessCapable *bool `json:"headless_capable,omitempty" toml:"headless_capable"`
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

	// [orchestrator.restart]: a non-empty but unparseable duration must fail
	// loudly rather than silently falling back to the accessor default (mirrors
	// delete.retention). Schedule entries are validated too; a bad entry is
	// otherwise silently skipped by parsedSchedule.
	rc := c.Orchestrator.Restart
	for _, f := range []struct{ name, val string }{
		{"orchestrator.restart.initial_backoff", rc.InitialBackoff},
		{"orchestrator.restart.max_backoff", rc.MaxBackoff},
		{"orchestrator.restart.stable_reset", rc.StableReset},
	} {
		if strings.TrimSpace(f.val) != "" {
			if _, err := ParseDurationWithDays(f.val); err != nil {
				errs = append(errs, fmt.Errorf("%s %q: %w", f.name, f.val, err))
			}
		}
	}

	for i, s := range rc.Schedule {
		if _, err := ParseDurationWithDays(s); err != nil {
			errs = append(errs, fmt.Errorf("orchestrator.restart.schedule[%d] %q: %w", i, s, err))
		}
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

	// [tools]: validate explicit executable overrides so a bad path/name fails
	// at startup, not at the first git/gh/notification call. Unset defaults are
	// skipped (they keep PATH-lookup semantics).
	if err := c.Tools.Validate(); err != nil {
		errs = append(errs, err)
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
