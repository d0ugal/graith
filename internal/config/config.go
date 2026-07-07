package config

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
	Agents           map[string]Agent   `toml:"agents"`
}

type Overlay struct {
	ShortcutKeys string `toml:"shortcut_keys"`
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
// backend. All three legacy modes map to the "command" backend — for
// mode="localmost" this preserves the historical behaviour (graith's own JSON
// contract, NOT the native-protocol "localmost" backend).
func legacyModeBackend(mode string) (string, bool) {
	switch mode {
	case "command", "external", "localmost":
		return "command", true
	default:
		return "", false
	}
}

func knownApprovalsBackend(name string) bool {
	switch name {
	case "prompt", "command", "external", "localmost", "builtin":
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
			return "", "", fmt.Errorf("[approvals] backend %q is not recognised (want one of prompt, command, external, localmost, builtin)", a.Backend)
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

	if usr.Sandbox.Enabled || usr.Sandbox.Disabled != nil || usr.Sandbox.Backend != "" ||
		usr.Sandbox.Command != "" || usr.Sandbox.Profile != "" || usr.Sandbox.Features != nil ||
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
