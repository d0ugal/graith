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
	Sandbox          SandboxConfig      `toml:"sandbox"`
	Approvals        Approvals          `toml:"approvals"`
	Status           StatusConfig       `toml:"status"`
	GitPull          GitPullConfig      `toml:"git_pull"`
	PRWatch          PRWatchConfig      `toml:"pr_watch"`
	MCPServers       []MCPServerConfig  `toml:"mcp_servers"`
	Overlay          Overlay            `toml:"overlay"`
	Orchestrator     OrchestratorConfig `toml:"orchestrator"`
	Remote           RemoteConfig       `toml:"remote"`
	Agents           map[string]Agent   `toml:"agents"`
}

type Overlay struct {
	ShortcutKeys string `toml:"shortcut_keys"`
}

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
	// "<n>/<unit>" (e.g. "5/min"); units are sec, min, or hour. Empty means no
	// configured limit here (the daemon applies its own default).
	PairRequestRate string `toml:"pair_request_rate"`
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
			return fmt.Errorf("[remote] auth_key_file is a tsnet-only field and cannot be set in interface mode")
		}

		if len(r.Tags) > 0 {
			return fmt.Errorf("[remote] tags is a tsnet-only field and cannot be set in interface mode")
		}
	}

	if strings.TrimSpace(r.PairRequestRate) != "" {
		if _, err := ParsePairRequestRate(r.PairRequestRate); err != nil {
			return err
		}
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

func (o OrchestratorConfig) AgentName() string {
	if o.Agent != "" {
		return o.Agent
	}

	return "claude"
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
// CI failures and review feedback are gated separately because they carry
// different authority: a CI failure is a machine verdict (safe to act on,
// default on); a review comment or decision is human intent that may not be
// actionable (default off).
type PRWatchConfig struct {
	Enabled               bool   `toml:"enabled"`
	NotifyCIFailures      bool   `toml:"notify_ci_failures"`
	NotifyMergeConflicts  bool   `toml:"notify_merge_conflicts"`
	NotifyReviewComments  bool   `toml:"notify_review_comments"`
	NotifyReviewDecisions bool   `toml:"notify_review_decisions"`
	NotifyPRLifecycle     bool   `toml:"notify_pr_lifecycle"`
	NotifyCIRecovery      bool   `toml:"notify_ci_recovery"`
	PollPending           string `toml:"poll_pending"`
	PollTerminal          string `toml:"poll_terminal"`
	PollMerged            string `toml:"poll_merged"`
	MaxNotificationsPerPR int    `toml:"max_notifications_per_pr"`
	Debounce              string `toml:"debounce"`
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

func (m Messages) MaxAgeDuration() time.Duration {
	if m.MaxAge == "" {
		return 0
	}

	d, _ := ParseDurationWithDays(m.MaxAge)

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
		return 0, fmt.Errorf("negative duration not allowed")
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
}

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
		return fmt.Errorf(
			"[approvals.builtin] config (external file) and inline rules " +
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
	Command           string                     `json:"command"                       toml:"command"`
	Args              []string                   `json:"args,omitempty"                toml:"args"`
	ResumeArgs        []string                   `json:"resume_args,omitempty"         toml:"resume_args"`
	ForkArgs          []string                   `json:"fork_args,omitempty"           toml:"fork_args"`
	Env               map[string]string          `json:"env,omitempty"                 toml:"env"`
	IdleTimeout       string                     `json:"idle_timeout,omitempty"        toml:"idle_timeout"`
	InjectPrompt      *bool                      `json:"inject_prompt,omitempty"       toml:"inject_prompt"`
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

	if len(rc.Includes) == 0 {
		return nil
	}

	mainBase := strings.ToLower(filepath.Base(ResolvePath(rc.Path)))
	basenames := map[string]string{mainBase: rc.Path}
	envNames := map[string]string{}

	for _, inc := range rc.Includes {
		resolved := ResolvePath(inc)
		if resolved == ResolvePath(rc.Path) {
			return fmt.Errorf("repo %q: cannot include itself", rc.Path)
		}

		base := strings.ToLower(filepath.Base(resolved))
		if prev, ok := basenames[base]; ok {
			return fmt.Errorf("repo %q: duplicate basename %q from %q and %q", rc.Path, base, prev, inc)
		}

		basenames[base] = inc

		envName := IncludeEnvVarName(filepath.Base(resolved))
		if prev, ok := envNames[envName]; ok {
			return fmt.Errorf("repo %q: env var collision %s from %q and %q", rc.Path, envName, prev, inc)
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

	// Validate the [remote] block statically (fail-closed when enabled). Runtime
	// listener provisioning (tailnet IP, TLS cert) is deferred to the daemon.
	if err := c.Remote.Validate(); err != nil {
		errs = append(errs, err)
	}

	for agentName, agent := range c.Agents {
		if err := agent.Sandbox.validateSignalMode("agents." + agentName + ".sandbox"); err != nil {
			errs = append(errs, err)
		}
	}

	seen := make(map[string]bool, len(c.MCPServers))
	for _, s := range c.MCPServers {
		switch {
		case s.Name == "":
			errs = append(errs, fmt.Errorf("mcp_servers: entry with empty name"))
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
