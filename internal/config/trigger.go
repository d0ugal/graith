package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/d0ugal/graith/internal/cronx"
)

// TriggerConfig is one [[trigger]] block. A trigger is (source) -> (action):
// exactly one of Schedule (#592) or Watch (#593) is the source, and Action is
// what runs. Everything below the source line is shared between the two source
// kinds. See docs/design/2026-07-11-triggers-design.md.
type TriggerConfig struct {
	Name     string          `toml:"name"`
	Enabled  *bool           `toml:"enabled"`  // nil => default true; explicit false disables
	Schedule *ScheduleConfig `toml:"schedule"` // time-driven source
	Watch    *WatchConfig    `toml:"watch"`    // file-event source
	Action   ActionConfig    `toml:"action"`
	Policy   TriggerPolicy   `toml:"policy"`
}

// ScheduleConfig is the time-driven source. Exactly one of Cron/Every is set.
type ScheduleConfig struct {
	Cron     string `toml:"cron"`     // 5-field cron, or @hourly/@daily/@weekly/@monthly
	Every    string `toml:"every"`    // Go duration (supports "7d"): "15m", "1h30m"
	Timezone string `toml:"timezone"` // IANA zone for cron; default = daemon local time
}

// WatchConfig is the file-event source. It is a POLICY selector (repo/role),
// never a literal live session name in config. Binds to matching sessions as
// they are created.
type WatchConfig struct {
	Repo     string   `toml:"repo"`     // bind to sessions on this repo
	Role     string   `toml:"role"`     // bind to sessions with this scenario role
	Paths    []string `toml:"paths"`    // optional include globs (worktree-relative)
	Ignore   []string `toml:"ignore"`   // extra ignore globs (added to built-ins + .gitignore)
	Debounce string   `toml:"debounce"` // quiet-window; default 30s
}

// ActionConfig is the shared action vocabulary. Type selects the verb.
type ActionConfig struct {
	Type string `toml:"type"` // command | session | scenario | message | tracker

	// command:
	Command  string `toml:"command"`
	Repo     string `toml:"repo"`     // required for schedule commands; rejected for watch
	Timeout  string `toml:"timeout"`  // max run time; default 5m
	Mutating bool   `toml:"mutating"` // may write its execution root; rejected in v1
	Sandbox  *bool  `toml:"sandbox"`  // nil => default true; false runs unconfined
	// SandboxConfig is extra sandbox grants merged onto the base command profile,
	// mirroring the MCP-server pattern (MCPServerConfig.SandboxConfig).
	SandboxConfig *SandboxConfig `toml:"sandbox_config"`

	// session:
	Prompt string `toml:"prompt"`
	Agent  string `toml:"agent"`
	Model  string `toml:"model"`
	Ensure bool   `toml:"ensure"` // idempotent ensure-reviewer (watch source only)
	// AutoCleanup soft-deletes a trigger-spawned session once it stops, so a
	// finished briefing/report session doesn't clutter `gr list`. It is a union
	// of bool and string: absent/false/"" disables it; true (or "always")
	// deletes on any stop; "on_success" deletes only on a clean (exit 0) stop.
	// Decoded as any so TOML can supply either a bool or the string enum; use
	// AutoCleanupMode to normalise. Session action only.
	AutoCleanup any `toml:"auto_cleanup"`
	// IdleTimeout auto-stops the spawned session after it sits idle (agent at
	// rest, no attached client) this long, overriding the agent default. A Go
	// duration ("1m", "5m"). Session action only. When unset, an
	// auto_cleanup="always" session defaults to a short idle window so a finished
	// briefing reaps itself promptly (finish -> idle-stop -> soft-delete); see
	// SessionIdleTimeout.
	IdleTimeout string `toml:"idle_timeout"`

	// scenario:
	Scenario string `toml:"scenario"`

	// tracker: keep live sessions in sync with an issue tracker. On each
	// scheduled fire the daemon polls the tracker for active issues and
	// reconciles sessions against them — spawning one per active issue (seeded
	// with the templated Prompt above) and reaping the session when its issue
	// leaves the active state. Schedule source only. See TrackerConfig and
	// docs/design/2026-07-16-tracker-poll-action.md.
	Tracker *TrackerConfig `toml:"tracker"`

	// message:
	Body string `toml:"body"`

	// notify (any action type): when NotifyOnComplete is set, the daemon fires a
	// proactive push notification (see [notifications]) once the action finishes
	// firing. NotifyMessage is the body (templated with the trigger vars;
	// defaults to a generic "<name> completed"); NotifyPriority is low/normal/high
	// (defaults to normal, or high when the action errored).
	NotifyOnComplete bool   `toml:"notify_on_complete"`
	NotifyMessage    string `toml:"notify_message"`
	NotifyPriority   string `toml:"notify_priority"`

	Deliver DeliverConfig `toml:"deliver"`
}

// RepoPath returns the action's configured repo canonicalised the same way
// sessions and the store CLI treat a repo path: a leading ~/ expanded, made
// absolute, and symlinks resolved (via ResolvePath). This matters for repo-store
// delivery, whose namespace is keyed off the repo path — a raw ~/... or a
// symlinked spelling would otherwise scope to a different store than the one
// agents read. It returns "" when no repo is set — unlike ResolvePath/ExpandPath,
// which would resolve "" to the working directory — so callers can still
// distinguish "unset" (shared store / no execution root) from a resolved path.
func (a ActionConfig) RepoPath() string {
	if a.Repo == "" {
		return ""
	}

	return ResolvePath(a.Repo)
}

// DeliverConfig routes action output. All fields are templated at fire time.
type DeliverConfig struct {
	Inbox string `toml:"inbox"` // session name, "orchestrator", or a template like "{session_name}"
	Topic string `toml:"topic"` // pub/sub topic
	Store string `toml:"store"` // store key (prefix "shared:" for the shared store)
	Wake  bool   `toml:"wake"`  // resume a non-orchestrator stopped inbox target
}

// TrackerConfig configures a tracker action's poll + reconcile behaviour. The
// spawned sessions' agent/model/prompt come from the enclosing ActionConfig; this
// block is the tracker-specific part. See
// docs/design/2026-07-16-tracker-poll-action.md.
type TrackerConfig struct {
	Provider      string   `toml:"provider"`       // "github" (v1); "" defaults to github
	Repo          string   `toml:"repo"`           // resolves the tracker + is the spawn repo (required)
	ActiveState   string   `toml:"active_state"`   // open | closed | all (default open)
	ActiveLabels  []string `toml:"active_labels"`  // active iff the issue has one of these (empty = any state-matching issue)
	Assignee      string   `toml:"assignee"`       // optional tracker assignee filter (e.g. "@me")
	Grace         string   `toml:"grace"`          // inactive this long before reaping; default 5m
	MaxConcurrent int      `toml:"max_concurrent"` // cap on live tracker sessions (0 = unlimited)
	Reap          string   `toml:"reap"`           // stop | delete | none (default stop)
	Limit         int      `toml:"limit"`          // max issues fetched per poll (default 50)
}

// Tracker provider values for TrackerConfig.Provider.
const (
	TrackerProviderGitHub = "github"
)

// Tracker active-state values for TrackerConfig.ActiveState.
const (
	TrackerStateOpen   = "open"
	TrackerStateClosed = "closed"
	TrackerStateAll    = "all"
)

// Tracker reap-policy values for TrackerConfig.Reap.
const (
	TrackerReapStop   = "stop"   // stop the agent (recoverable via gr resume)
	TrackerReapDelete = "delete" // soft-delete the session (recoverable via gr restore)
	TrackerReapNone   = "none"   // leave the session; report only
)

const (
	defaultTrackerGrace = 5 * time.Minute
	defaultTrackerLimit = 50
)

// ProviderOr returns the configured provider, defaulting to github.
func (t TrackerConfig) ProviderOr() string {
	if t.Provider == "" {
		return TrackerProviderGitHub
	}

	return t.Provider
}

// ActiveStateOr returns the configured active state, defaulting to open.
func (t TrackerConfig) ActiveStateOr() string {
	if t.ActiveState == "" {
		return TrackerStateOpen
	}

	return t.ActiveState
}

// ReapMode returns the configured reap policy, defaulting to stop.
func (t TrackerConfig) ReapMode() string {
	if t.Reap == "" {
		return TrackerReapStop
	}

	return t.Reap
}

// GraceDuration returns the reap grace window, defaulting to 5m.
func (t TrackerConfig) GraceDuration() time.Duration {
	return parseDurationOr(t.Grace, defaultTrackerGrace)
}

// LimitOr returns the per-poll issue fetch cap, defaulting to 50.
func (t TrackerConfig) LimitOr() int {
	if t.Limit <= 0 {
		return defaultTrackerLimit
	}

	return t.Limit
}

// RepoPath returns the tracker repo canonicalised the same way ActionConfig.RepoPath
// treats a repo (see that method). Empty when unset.
func (t TrackerConfig) RepoPath() string {
	if t.Repo == "" {
		return ""
	}

	return ResolvePath(t.Repo)
}

// TriggerPolicy controls missed-run / overlap / rate-limit behaviour.
type TriggerPolicy struct {
	CatchUp   bool   `toml:"catch_up"`   // default false: never backfill missed fires
	Overlap   string `toml:"overlap"`    // "" or "skip" (default) | "allow" | "queue"(v2)
	RateLimit string `toml:"rate_limit"` // "N/duration"; default "5/30m"
}

// TriggersRuntime holds daemon-wide trigger settings ([triggers] table, distinct
// from the [[trigger]] array).
type TriggersRuntime struct {
	MaxConcurrent int `toml:"max_concurrent"` // default 4
	// Advanced holds the low-level scheduler and file-watch tuning knobs (loop
	// cadence, run-history retention, degraded-binding backoff, the daemon-wide
	// watch ignore list, and the command-output cap). Every field is optional and
	// falls back to the historical default via the accessors below, so a config
	// that omits [triggers.advanced] behaves exactly as before. Expose these only
	// for operators who need to trade off detection latency, filesystem-watch
	// load, and notification size — the defaults suit ordinary use.
	Advanced TriggersAdvancedConfig `toml:"advanced"`
}

// TriggersAdvancedConfig carries the advanced tuning for the trigger scheduler and
// the file-watch runtime. These were formerly hard-coded policy literals in the
// daemon (internal/daemon/trigger.go, filewatch.go, trigger_actions.go); they are
// surfaced here so an operator can tune scheduler latency, file-watch reconcile
// cadence, degraded-binding retry backoff, the always-ignored directory set, and
// the command-output cap without a rebuild. Every field is optional: an unset
// (zero/empty) value resolves to the documented default through the TriggersRuntime
// accessors, so leaving [triggers.advanced] out is a no-op.
type TriggersAdvancedConfig struct {
	// SchedulerTick is the trigger scheduler loop cadence. Cron granularity is one
	// minute, so this only bounds sub-minute "every" intervals and dispatch
	// latency. Default 1s. Applied when the scheduler loop starts.
	SchedulerTick string `toml:"scheduler_tick"`
	// RunHistoryMax caps how many past runs each trigger retains in its persisted
	// history. Default 20.
	RunHistoryMax int `toml:"run_history_max"`
	// WatchReconcileInterval is how often file-watch bindings are reconciled
	// against live sessions (creating, tearing down, and retrying degraded
	// bindings). Default 2s. Applied when the file-watch loop starts.
	WatchReconcileInterval string `toml:"watch_reconcile_interval"`
	// WatchRetryBaseBackoff is the delay before the first retry of a degraded
	// file-watch binding (e.g. one that hit fs.inotify.max_user_watches).
	// Subsequent retries back off exponentially from here. Default 5s.
	WatchRetryBaseBackoff string `toml:"watch_retry_base_backoff"`
	// WatchRetryMaxBackoff caps the exponential degraded-binding backoff so a
	// persistently degraded binding keeps retrying periodically. Default 5m.
	WatchRetryMaxBackoff string `toml:"watch_retry_max_backoff"`
	// WatchBuiltinIgnores is the daemon-wide set of directories/patterns never
	// watched by any file-watch trigger (on top of git ignore rules and per-trigger
	// watch.ignore). Omitting the key uses DefaultWatchBuiltinIgnores; an explicit
	// empty list ([]) keeps only the mandatory ignores. ".git"/".git/" are always
	// ignored regardless of this list (a watched .git churns constantly and creates
	// a feedback loop).
	WatchBuiltinIgnores []string `toml:"watch_builtin_ignores"`
	// CommandOutputCap truncates a command action's captured output to this many
	// bytes before delivery, bounding notification size. Default 4096.
	CommandOutputCap int `toml:"command_output_cap"`
}

// ReservedTriggerNamePrefix is reserved for the daemon's namespaced
// scenario-embedded trigger names (scenario:<id>:<name>). A config-origin
// trigger name must not use it, or it would be misrouted to a scenario lookup.
const ReservedTriggerNamePrefix = "scenario:"

// Action type values for ActionConfig.Type.
const (
	ActionCommand  = "command"
	ActionSession  = "session"
	ActionScenario = "scenario"
	ActionMessage  = "message"
	ActionTracker  = "tracker"
)

// Overlap policy values for TriggerPolicy.Overlap.
const (
	OverlapSkip  = "skip"
	OverlapAllow = "allow"
	OverlapQueue = "queue" // deferred to v2
)

// Auto-cleanup mode values for a session action's AutoCleanup.
const (
	CleanupAlways    = "always"     // delete on any stop
	CleanupOnSuccess = "on_success" // delete only on a clean (exit 0) stop
)

// defaultAutoCleanupIdle is the idle window an auto_cleanup="always" session
// gets when it doesn't set idle_timeout explicitly: short enough that a finished
// briefing reaps itself promptly, long enough not to reap a brief mid-task rest.
const defaultAutoCleanupIdle = time.Minute

const (
	defaultDebounce      = 30 * time.Second
	defaultCommandRun    = 5 * time.Minute
	defaultMaxConcurrent = 4
	defaultRateLimitN    = 5
	defaultRateLimitWin  = 30 * time.Minute
)

// Advanced trigger scheduler / file-watch tuning defaults (issue #1248). These
// are the daemon's historical policy literals; the accessors below fall back to
// them when [triggers.advanced] omits the key, and default_config.toml carries
// the same values so `gr config show/diff/reset` describe the full behaviour.
const (
	defaultSchedulerTick    = 1 * time.Second
	defaultRunHistoryMax    = 20
	defaultWatchReconcile   = 2 * time.Second
	defaultWatchRetryBase   = 5 * time.Second
	defaultWatchRetryMax    = 5 * time.Minute
	defaultCommandOutputCap = 4096
)

// DefaultWatchBuiltinIgnores is the daemon-wide set of directories/patterns never
// watched by a file-watch trigger when [triggers.advanced] watch_builtin_ignores
// is unset. Watching any of these is never useful and they are prime
// feedback-loop / churn sources. Materialized in default_config.toml.
var DefaultWatchBuiltinIgnores = []string{".git/", ".git", ".hg/", ".svn/", "*.swp", "*.swx", "4913", ".DS_Store"}

// TriggerEnabled reports whether the trigger is enabled (nil => true).
func (t TriggerConfig) TriggerEnabled() bool {
	return t.Enabled == nil || *t.Enabled
}

// IsSchedule / IsWatch report the source kind.
func (t TriggerConfig) IsSchedule() bool { return t.Schedule != nil }
func (t TriggerConfig) IsWatch() bool    { return t.Watch != nil }

// DebounceDuration returns the watch debounce, defaulting to 30s.
func (w WatchConfig) DebounceDuration() time.Duration {
	return parseDurationOr(w.Debounce, defaultDebounce)
}

// TimeoutDuration returns a command action's timeout, defaulting to 5m.
func (a ActionConfig) TimeoutDuration() time.Duration {
	return parseDurationOr(a.Timeout, defaultCommandRun)
}

// Sandboxed reports whether a command action runs sandboxed (nil => true).
func (a ActionConfig) Sandboxed() bool {
	return a.Sandbox == nil || *a.Sandbox
}

// AutoCleanupMode normalises the auto_cleanup union to "" (disabled),
// CleanupAlways, or CleanupOnSuccess. true is shorthand for "always"; false and
// an absent value are disabled. Any other value is a config error.
func (a ActionConfig) AutoCleanupMode() (string, error) {
	switch v := a.AutoCleanup.(type) {
	case nil:
		return "", nil
	case bool:
		if v {
			return CleanupAlways, nil
		}

		return "", nil
	case string:
		switch v {
		case "":
			return "", nil
		case "true":
			return CleanupAlways, nil
		case "false":
			return "", nil
		case CleanupAlways, CleanupOnSuccess:
			return v, nil
		default:
			return "", fmt.Errorf("auto_cleanup %q is invalid (want true, false, %q, or %q)", v, CleanupAlways, CleanupOnSuccess)
		}
	default:
		return "", fmt.Errorf("auto_cleanup must be a boolean or one of %q/%q", CleanupAlways, CleanupOnSuccess)
	}
}

// SessionIdleTimeout resolves the idle-stop window for a spawned session action.
// An explicit idle_timeout always wins. Otherwise an auto_cleanup="always"
// session gets defaultAutoCleanupIdle so it reaps itself promptly. "on_success"
// is deliberately not auto-idled: an idle-stop is a non-zero (SIGTERM) exit that
// "on_success" would not clean up, so idling it would just leave stopped clutter
// — the very thing auto_cleanup avoids. 0 means "use the agent default".
func (a ActionConfig) SessionIdleTimeout() (time.Duration, error) {
	if a.IdleTimeout != "" {
		d, err := ParseDurationWithDays(a.IdleTimeout)
		if err != nil {
			return 0, fmt.Errorf("idle_timeout %q: %w", a.IdleTimeout, err)
		}

		return d, nil
	}

	mode, err := a.AutoCleanupMode()
	if err != nil {
		return 0, err
	}

	if mode == CleanupAlways {
		return defaultAutoCleanupIdle, nil
	}

	return 0, nil
}

// OverlapMode returns the effective overlap policy (empty => skip).
func (p TriggerPolicy) OverlapMode() string {
	if p.Overlap == "" {
		return OverlapSkip
	}

	return p.Overlap
}

// RateLimitParsed parses "N/duration" (e.g. "5/30m"), defaulting to 5 per 30m.
func (p TriggerPolicy) RateLimitParsed() (int, time.Duration) {
	if p.RateLimit == "" {
		return defaultRateLimitN, defaultRateLimitWin
	}

	parts := strings.SplitN(p.RateLimit, "/", 2)
	if len(parts) != 2 {
		return defaultRateLimitN, defaultRateLimitWin
	}

	var n int
	if _, err := fmt.Sscanf(strings.TrimSpace(parts[0]), "%d", &n); err != nil || n <= 0 {
		return defaultRateLimitN, defaultRateLimitWin
	}

	win, err := ParseDurationWithDays(strings.TrimSpace(parts[1]))
	if err != nil || win <= 0 {
		return defaultRateLimitN, defaultRateLimitWin
	}

	return n, win
}

// validRateLimit reports whether s is a well-formed "N/duration" rate limit
// with N>0 and a positive duration.
func validRateLimit(s string) bool {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 {
		return false
	}

	var n int
	if _, err := fmt.Sscanf(strings.TrimSpace(parts[0]), "%d", &n); err != nil || n <= 0 {
		return false
	}

	d, err := ParseDurationWithDays(strings.TrimSpace(parts[1]))

	return err == nil && d > 0
}

// MaxConcurrentOr returns the daemon-wide concurrency cap, defaulting to 4.
func (r TriggersRuntime) MaxConcurrentOr() int {
	if r.MaxConcurrent <= 0 {
		return defaultMaxConcurrent
	}

	return r.MaxConcurrent
}

// The accessors below resolve the [triggers.advanced] tuning knobs, each falling
// back to the daemon's historical default when unset (or non-positive / invalid).
// Keeping the default here — not in the daemon — mirrors the PR-watch advanced
// accessors and lets the embedded default_config.toml materialize the same values
// while the Go fallback stays authoritative for an omitted key (see issue #1228).

// SchedulerTickDuration is the trigger scheduler loop cadence. Default 1s; an
// unset, unparseable, or non-positive value uses the default (the scheduler
// feeds this straight to time.NewTicker, which panics on a non-positive
// interval).
func (r TriggersRuntime) SchedulerTickDuration() time.Duration {
	return positiveDurationOrDefault(r.Advanced.SchedulerTick, defaultSchedulerTick)
}

// RunHistoryMax is the per-trigger retained run-history length. Default 20.
func (r TriggersRuntime) RunHistoryMax() int {
	if r.Advanced.RunHistoryMax <= 0 {
		return defaultRunHistoryMax
	}

	return r.Advanced.RunHistoryMax
}

// WatchReconcileIntervalDuration is the file-watch binding reconcile cadence.
// Default 2s; an unset, unparseable, or non-positive value uses the default (the
// file watcher feeds this straight to time.NewTicker, which panics on a
// non-positive interval).
func (r TriggersRuntime) WatchReconcileIntervalDuration() time.Duration {
	return positiveDurationOrDefault(r.Advanced.WatchReconcileInterval, defaultWatchReconcile)
}

// WatchRetryBaseBackoffDuration is the first-retry delay for a degraded file-watch
// binding. Default 5s.
func (r TriggersRuntime) WatchRetryBaseBackoffDuration() time.Duration {
	return parseDurationOr(r.Advanced.WatchRetryBaseBackoff, defaultWatchRetryBase)
}

// WatchRetryMaxBackoffDuration caps the exponential degraded-binding backoff.
// Default 5m.
func (r TriggersRuntime) WatchRetryMaxBackoffDuration() time.Duration {
	return parseDurationOr(r.Advanced.WatchRetryMaxBackoff, defaultWatchRetryMax)
}

// WatchBuiltinIgnores returns the daemon-wide watch ignore list. An omitted key
// (nil) resolves to DefaultWatchBuiltinIgnores; an explicit empty list ([]) is
// honored as "only the mandatory ignores", so a nil slice and a present-empty
// slice are NOT conflated (issue #1309). The daemon additionally always ignores
// ".git"/".git/" regardless of this list. A fresh copy is returned so callers
// cannot mutate the shared default slice, and a present-empty list is returned
// as a non-nil slice so consumers can distinguish it from an omitted policy.
func (r TriggersRuntime) WatchBuiltinIgnores() []string {
	if r.Advanced.WatchBuiltinIgnores == nil {
		return append([]string(nil), DefaultWatchBuiltinIgnores...)
	}

	return append([]string{}, r.Advanced.WatchBuiltinIgnores...)
}

// CommandOutputCap is the command-action output truncation cap in bytes.
// Default 4096.
func (r TriggersRuntime) CommandOutputCap() int {
	if r.Advanced.CommandOutputCap <= 0 {
		return defaultCommandOutputCap
	}

	return r.Advanced.CommandOutputCap
}

// validateTriggers checks every [[trigger]] block. It is called from
// Config.Validate. Rules follow docs/design/2026-07-11-triggers-design.md.
func (c *Config) validateTriggers() []error {
	var errs []error

	seen := make(map[string]bool)

	for i := range c.Triggers {
		t := &c.Triggers[i]

		where := fmt.Sprintf("trigger[%d]", i)
		if t.Name != "" {
			where = fmt.Sprintf("trigger %q", t.Name)
		}

		if t.Name == "" {
			errs = append(errs, fmt.Errorf("%s: name is required", where))
		} else if seen[t.Name] {
			errs = append(errs, fmt.Errorf("%s: duplicate trigger name", where))
		} else if strings.HasPrefix(t.Name, ReservedTriggerNamePrefix) {
			errs = append(errs, fmt.Errorf("%s: name must not start with the reserved %q prefix", where, ReservedTriggerNamePrefix))
		}

		seen[t.Name] = true

		errs = append(errs, ValidateTriggerStructure(where, t)...)
		errs = append(errs, c.validateActionConfigDeps(where, t)...)
	}

	return errs
}

// ValidateTriggerStructure runs the config-independent structural validation for
// a single trigger: exactly one source, the source's own rules, the action's
// shape, and the policy. Config-dependent checks (allowed_repo_paths and
// [orchestrator] enabled) are layered on separately by validateActionConfigDeps.
// It is exported so the scenario-file loader can hold scenario-embedded
// [[trigger]] blocks to the same shape rules without a full *Config.
func ValidateTriggerStructure(where string, t *TriggerConfig) []error {
	var errs []error

	// Exactly one source.
	switch {
	case t.Schedule == nil && t.Watch == nil:
		errs = append(errs, fmt.Errorf("%s: exactly one of [schedule] or [watch] is required (neither set)", where))
	case t.Schedule != nil && t.Watch != nil:
		errs = append(errs, fmt.Errorf("%s: exactly one of [schedule] or [watch] is required (both set)", where))
	case t.Schedule != nil:
		errs = append(errs, validateSchedule(where, t.Schedule)...)
	case t.Watch != nil:
		errs = append(errs, validateWatch(where, t.Watch)...)
	}

	errs = append(errs, validateActionStructure(where, t)...)
	errs = append(errs, validatePolicy(where, &t.Policy)...)

	return errs
}

func validateSchedule(where string, s *ScheduleConfig) []error {
	var errs []error

	switch {
	case s.Cron == "" && s.Every == "":
		errs = append(errs, fmt.Errorf("%s: [schedule] requires exactly one of cron or every (neither set)", where))
	case s.Cron != "" && s.Every != "":
		errs = append(errs, fmt.Errorf("%s: [schedule] requires exactly one of cron or every (both set)", where))
	}

	if s.Cron != "" {
		if err := cronx.Validate(s.Cron); err != nil {
			errs = append(errs, fmt.Errorf("%s: [schedule] cron %q: %w", where, s.Cron, err))
		}

		if s.Timezone != "" {
			if _, err := time.LoadLocation(s.Timezone); err != nil {
				errs = append(errs, fmt.Errorf("%s: [schedule] timezone %q: %w", where, s.Timezone, err))
			}
		}
	}

	if s.Every != "" {
		d, err := ParseDurationWithDays(s.Every)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: [schedule] every %q: %w", where, s.Every, err))
		} else if d <= 0 {
			errs = append(errs, fmt.Errorf("%s: [schedule] every must be > 0", where))
		}

		if s.Timezone != "" {
			errs = append(errs, fmt.Errorf("%s: [schedule] timezone is only valid with cron, not every", where))
		}
	}

	return errs
}

func validateWatch(where string, w *WatchConfig) []error {
	var errs []error

	switch {
	case w.Repo == "" && w.Role == "":
		errs = append(errs, fmt.Errorf("%s: [watch] requires exactly one of repo or role (neither set)", where))
	case w.Repo != "" && w.Role != "":
		errs = append(errs, fmt.Errorf("%s: [watch] requires exactly one of repo or role (both set)", where))
	}

	if w.Debounce != "" {
		if d, err := ParseDurationWithDays(w.Debounce); err != nil {
			errs = append(errs, fmt.Errorf("%s: [watch] debounce %q: %w", where, w.Debounce, err))
		} else if d <= 0 {
			errs = append(errs, fmt.Errorf("%s: [watch] debounce must be > 0", where))
		}
	}

	return errs
}

// validateActionStructure checks an action's shape independent of any *Config:
// the action-type switch, cleanup/idle placement, and notify priority. The two
// config-dependent checks (allowed_repo_paths, [orchestrator] enabled) live in
// validateActionConfigDeps.
func validateActionStructure(where string, t *TriggerConfig) []error {
	var errs []error

	a := &t.Action

	switch a.Type {
	case ActionCommand:
		errs = append(errs, validateCommandActionStructure(where, t)...)
	case ActionSession:
		if a.Ensure && !t.IsWatch() {
			errs = append(errs, fmt.Errorf("%s: action ensure=true is only valid for a [watch] source", where))
		}

		mode, err := a.AutoCleanupMode()
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", where, err))
		} else if mode != "" && a.Ensure {
			// A reused reactor is deliberately long-lived; deleting it on every
			// stop would defeat ensure's idempotent reuse.
			errs = append(errs, fmt.Errorf("%s: action.auto_cleanup is incompatible with ensure=true (reused reactors are not auto-deleted)", where))
		}

		// Unlike auto_cleanup, idle_timeout IS allowed with ensure=true: idle-
		// stopping a reactor is recoverable (reuseReactor auto-resumes a stopped
		// reactor on the next event), so it frees resources without defeating
		// idempotent reuse the way a soft-delete would.
		if a.IdleTimeout != "" {
			if d, err := ParseDurationWithDays(a.IdleTimeout); err != nil {
				errs = append(errs, fmt.Errorf("%s: action.idle_timeout %q: %w", where, a.IdleTimeout, err))
			} else if d < time.Second {
				// Idle stopping is second-granular (stored as whole seconds); a
				// sub-second value would truncate to 0 and silently fall back to
				// the agent default — the opposite of a tight timeout — so reject it.
				errs = append(errs, fmt.Errorf("%s: action.idle_timeout must be at least 1s", where))
			}
		}
	case ActionScenario:
		if a.Scenario == "" {
			errs = append(errs, fmt.Errorf("%s: scenario action requires action.scenario", where))
		}

		if a.Deliver != (DeliverConfig{}) {
			errs = append(errs, fmt.Errorf("%s: scenario action does not support [action.deliver]", where))
		}
	case ActionMessage:
		if a.Body == "" {
			errs = append(errs, fmt.Errorf("%s: message action requires action.body", where))
		}

		if a.Deliver.Inbox == "" && a.Deliver.Topic == "" {
			errs = append(errs, fmt.Errorf("%s: message action requires action.deliver.inbox or action.deliver.topic", where))
		}
	case ActionTracker:
		errs = append(errs, validateTrackerActionStructure(where, t)...)
	case "":
		errs = append(errs, fmt.Errorf("%s: action.type is required (command|session|scenario|message|tracker)", where))
	default:
		errs = append(errs, fmt.Errorf("%s: unknown action.type %q", where, a.Type))
	}

	// auto_cleanup and idle_timeout act on a spawned session's lifecycle, so they
	// only make sense for the session action.
	if a.Type != ActionSession && a.Type != "" {
		if mode, err := a.AutoCleanupMode(); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", where, err))
		} else if mode != "" {
			errs = append(errs, fmt.Errorf("%s: action.auto_cleanup is only valid for a session action", where))
		}

		if a.IdleTimeout != "" {
			errs = append(errs, fmt.Errorf("%s: action.idle_timeout is only valid for a session action", where))
		}
	}

	if a.NotifyPriority != "" {
		if _, ok := NormalizeNotifyPriority(a.NotifyPriority); !ok {
			errs = append(errs, fmt.Errorf("%s: action.notify_priority %q is invalid (want low|normal|high)", where, a.NotifyPriority))
		}
	}

	return errs
}

// validateActionConfigDeps holds the two trigger-action checks that need the
// full *Config: a schedule command's repo must be an allowed repo path, and
// session/scenario actions need an orchestrator to own the spawned work. Applied
// on config load but deliberately NOT by the scenario-file loader — a scenario
// is always orchestrator-owned, and scenario triggers forbid an external repo
// (see scenariofile.ValidateScenarioTriggers).
func (c *Config) validateActionConfigDeps(where string, t *TriggerConfig) []error {
	var errs []error

	a := &t.Action

	if a.Type == ActionCommand && t.IsSchedule() && a.Repo != "" && !c.RepoPathAllowed(a.Repo) {
		errs = append(errs, fmt.Errorf("%s: action.repo %q is not in allowed_repo_paths", where, a.Repo))
	}

	if a.Type == ActionTracker && a.Tracker != nil {
		if a.Tracker.Repo != "" && !c.RepoPathAllowed(a.Tracker.Repo) {
			errs = append(errs, fmt.Errorf("%s: action.tracker.repo %q is not in allowed_repo_paths", where, a.Tracker.Repo))
		}

		// reap = "delete" is a SOFT delete, recoverable within the retention window.
		// With retention disabled it would become an immediate hard purge, violating
		// "reaping never destroys". Require soft delete to be enabled.
		if a.Tracker.ReapMode() == TrackerReapDelete && c.Delete.RetentionDuration() <= 0 {
			errs = append(errs, fmt.Errorf("%s: tracker action.tracker.reap = delete requires [delete] retention > 0 (a soft delete must be recoverable)", where))
		}
	}

	if (a.Type == ActionSession || a.Type == ActionScenario || a.Type == ActionTracker) && !c.Orchestrator.Enabled {
		errs = append(errs, fmt.Errorf("%s: %s action requires [orchestrator] enabled (it owns spawned sessions)", where, a.Type))
	}

	return errs
}

// validateCommandActionStructure checks a command action's shape independent of
// any *Config. The allowed_repo_paths check for a schedule command lives in
// validateActionConfigDeps.
func validateCommandActionStructure(where string, t *TriggerConfig) []error {
	var errs []error

	a := &t.Action

	if a.Command == "" {
		errs = append(errs, fmt.Errorf("%s: command action requires action.command", where))
	}

	if a.Mutating {
		errs = append(errs, fmt.Errorf("%s: action.mutating is not supported in v1 (watch commands are read-only)", where))
	}

	if a.Timeout != "" {
		if d, err := ParseDurationWithDays(a.Timeout); err != nil {
			errs = append(errs, fmt.Errorf("%s: action.timeout %q: %w", where, a.Timeout, err))
		} else if d <= 0 {
			errs = append(errs, fmt.Errorf("%s: action.timeout must be > 0", where))
		}
	}
	// Execution root: schedule commands require repo; watch commands derive it
	// from the bound session's worktree and must not set repo.
	if t.IsSchedule() {
		if a.Repo == "" {
			errs = append(errs, fmt.Errorf("%s: schedule command action requires action.repo", where))
		}
	} else if a.Repo != "" {
		errs = append(errs, fmt.Errorf("%s: watch command action must not set action.repo (execution root is the bound worktree)", where))
	}

	return errs
}

// validateTrackerActionStructure checks a tracker action's shape independent of
// any *Config. The orchestrator-required and repo-allow-list checks live in
// validateActionConfigDeps.
func validateTrackerActionStructure(where string, t *TriggerConfig) []error {
	var errs []error

	a := &t.Action

	if !t.IsSchedule() {
		errs = append(errs, fmt.Errorf("%s: tracker action requires a [schedule] source (it polls on a cadence)", where))
	}

	// A tracker reconcile reads the live session set, then spawns/reaps. Concurrent
	// passes could double-spawn or reap on a stale snapshot, so it must serialise
	// — reject overlap = allow (the default skip is required).
	if t.Policy.OverlapMode() == OverlapAllow {
		errs = append(errs, fmt.Errorf("%s: tracker action requires policy.overlap = skip (concurrent reconciles could double-spawn or reap a stale view)", where))
	}

	tc := a.Tracker
	if tc == nil {
		errs = append(errs, fmt.Errorf("%s: tracker action requires an [action.tracker] block", where))
		return errs
	}

	if tc.Repo == "" {
		errs = append(errs, fmt.Errorf("%s: tracker action requires action.tracker.repo", where))
	}

	switch tc.ProviderOr() {
	case TrackerProviderGitHub:
	default:
		errs = append(errs, fmt.Errorf("%s: tracker action.tracker.provider %q is unsupported (want %q)", where, tc.Provider, TrackerProviderGitHub))
	}

	switch tc.ActiveStateOr() {
	case TrackerStateOpen, TrackerStateClosed, TrackerStateAll:
	default:
		errs = append(errs, fmt.Errorf("%s: tracker action.tracker.active_state %q is invalid (want open|closed|all)", where, tc.ActiveState))
	}

	switch tc.ReapMode() {
	case TrackerReapStop, TrackerReapDelete, TrackerReapNone:
	default:
		errs = append(errs, fmt.Errorf("%s: tracker action.tracker.reap %q is invalid (want stop|delete|none)", where, tc.Reap))
	}

	if tc.Grace != "" {
		if d, err := ParseDurationWithDays(tc.Grace); err != nil {
			errs = append(errs, fmt.Errorf("%s: tracker action.tracker.grace %q: %w", where, tc.Grace, err))
		} else if d < 0 {
			errs = append(errs, fmt.Errorf("%s: tracker action.tracker.grace must not be negative", where))
		}
	}

	if tc.MaxConcurrent < 0 {
		errs = append(errs, fmt.Errorf("%s: tracker action.tracker.max_concurrent must not be negative", where))
	}

	if tc.Limit < 0 {
		errs = append(errs, fmt.Errorf("%s: tracker action.tracker.limit must not be negative", where))
	}

	return errs
}

func validatePolicy(where string, p *TriggerPolicy) []error {
	var errs []error

	switch p.Overlap {
	case "", OverlapSkip, OverlapAllow:
	case OverlapQueue:
		errs = append(errs, fmt.Errorf("%s: policy.overlap = %q is not supported in v1", where, OverlapQueue))
	default:
		errs = append(errs, fmt.Errorf("%s: policy.overlap %q is invalid (want skip|allow)", where, p.Overlap))
	}

	if p.RateLimit != "" {
		if !validRateLimit(p.RateLimit) {
			errs = append(errs, fmt.Errorf("%s: policy.rate_limit %q must be \"N/duration\" with N>0 (e.g. 5/30m)", where, p.RateLimit))
		}
	}

	return errs
}
