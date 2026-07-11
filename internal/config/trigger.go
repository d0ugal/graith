package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// cronSpecParser accepts 5-field cron expressions plus @-descriptors. It is the
// same shape the daemon's firing loop uses, so config validation rejects exactly
// what the runtime can't parse.
var cronSpecParser = cron.NewParser(
	cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
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
	Type string `toml:"type"` // command | session | scenario | message

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

	// scenario:
	Scenario string `toml:"scenario"`

	// message:
	Body string `toml:"body"`

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
func (a *ActionConfig) RepoPath() string {
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
}

// Action type values for ActionConfig.Type.
const (
	ActionCommand  = "command"
	ActionSession  = "session"
	ActionScenario = "scenario"
	ActionMessage  = "message"
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

const (
	defaultDebounce      = 30 * time.Second
	defaultCommandRun    = 5 * time.Minute
	defaultMaxConcurrent = 4
	defaultRateLimitN    = 5
	defaultRateLimitWin  = 30 * time.Minute
)

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
		}

		seen[t.Name] = true

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

		errs = append(errs, c.validateAction(where, t)...)
		errs = append(errs, validatePolicy(where, &t.Policy)...)
	}

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
		if _, err := cronSpecParser.Parse(s.Cron); err != nil {
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

func (c *Config) validateAction(where string, t *TriggerConfig) []error {
	var errs []error

	a := &t.Action

	switch a.Type {
	case ActionCommand:
		errs = append(errs, c.validateCommandAction(where, t)...)
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
	case "":
		errs = append(errs, fmt.Errorf("%s: action.type is required (command|session|scenario|message)", where))
	default:
		errs = append(errs, fmt.Errorf("%s: unknown action.type %q", where, a.Type))
	}

	// auto_cleanup watches a spawned session's lifecycle, so it only makes sense
	// for the session action.
	if a.Type != ActionSession && a.Type != "" {
		if mode, err := a.AutoCleanupMode(); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", where, err))
		} else if mode != "" {
			errs = append(errs, fmt.Errorf("%s: action.auto_cleanup is only valid for a session action", where))
		}
	}

	// session/scenario actions need an orchestrator to own the spawned work.
	if (a.Type == ActionSession || a.Type == ActionScenario) && !c.Orchestrator.Enabled {
		errs = append(errs, fmt.Errorf("%s: %s action requires [orchestrator] enabled (it owns spawned sessions)", where, a.Type))
	}

	return errs
}

func (c *Config) validateCommandAction(where string, t *TriggerConfig) []error {
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
		} else if !c.RepoPathAllowed(a.Repo) {
			errs = append(errs, fmt.Errorf("%s: action.repo %q is not in allowed_repo_paths", where, a.Repo))
		}
	} else if a.Repo != "" {
		errs = append(errs, fmt.Errorf("%s: watch command action must not set action.repo (execution root is the bound worktree)", where))
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
