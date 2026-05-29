package config

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/adrg/xdg"
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
	MCPServers       []MCPServerConfig  `toml:"mcp_servers"`
	Orchestrator     OrchestratorConfig `toml:"orchestrator"`
	Agents           map[string]Agent   `toml:"agents"`
}

type OrchestratorConfig struct {
	Enabled     bool   `toml:"enabled"`
	Agent       string `toml:"agent"`
	Model       string `toml:"model"`
	IdleTimeout string `toml:"idle_timeout"`
	PromptFile  string `toml:"prompt_file"`
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
	Mode    string `toml:"mode"`
	AutoPop bool   `toml:"auto_pop"`
	Timeout string `toml:"timeout"`
	Command string `toml:"command"`
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
	Command       string                     `json:"command"                  toml:"command"`
	Args          []string                   `json:"args,omitempty"           toml:"args"`
	ResumeArgs    []string                   `json:"resume_args,omitempty"    toml:"resume_args"`
	ForkArgs      []string                   `json:"fork_args,omitempty"      toml:"fork_args"`
	Env           map[string]string          `json:"env,omitempty"            toml:"env"`
	IdleTimeout   string                     `json:"idle_timeout,omitempty"   toml:"idle_timeout"`
	InjectPrompt  *bool                      `json:"inject_prompt,omitempty"  toml:"inject_prompt"`
	Sandbox       SandboxConfig              `json:"sandbox"                  toml:"sandbox"`
	MCPServers    map[string]MCPServerConfig `json:"mcp_servers,omitempty"    toml:"mcp_servers"`
	ValidateModel string                     `json:"validate_model,omitempty" toml:"validate_model"`
}

func (a Agent) PromptInjectionEnabled() bool {
	if a.InjectPrompt != nil {
		return *a.InjectPrompt
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
	Enabled   bool     `json:"enabled"              toml:"enabled"`
	Disabled  *bool    `json:"disabled,omitempty"   toml:"disabled,omitempty"`
	Command   string   `json:"command,omitempty"    toml:"command"`
	Features  []string `json:"features,omitempty"   toml:"features"`
	ReadDirs  []string `json:"read_dirs,omitempty"  toml:"read_dirs"`
	WriteDirs []string `json:"write_dirs,omitempty" toml:"write_dirs"`
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
		Enabled: s.Enabled || agent.Enabled,
		Command: s.Command,
	}

	if agent.Disabled != nil && *agent.Disabled {
		merged.Enabled = false
		return merged
	}

	merged.Features = dedup(append(s.Features, agent.Features...))
	merged.ReadDirs = dedup(append(s.ReadDirs, agent.ReadDirs...))
	merged.WriteDirs = dedup(append(s.WriteDirs, agent.WriteDirs...))

	if agent.Command != "" {
		merged.Command = agent.Command
	}

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
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config validation: %w", err)
	}
	return cfg, nil
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
	if usr.Sandbox.Enabled || usr.Sandbox.Disabled != nil || usr.Sandbox.Command != "" ||
		usr.Sandbox.Features != nil || usr.Sandbox.ReadDirs != nil || usr.Sandbox.WriteDirs != nil {
		def.Sandbox = usr.Sandbox
	}
	if usr.InjectPrompt != nil {
		def.InjectPrompt = usr.InjectPrompt
	}
	if usr.MCPServers != nil {
		def.MCPServers = usr.MCPServers
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
