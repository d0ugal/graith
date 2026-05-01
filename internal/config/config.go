package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/adrg/xdg"
	"github.com/pelletier/go-toml/v2"
)

type Config struct {
	DefaultAgent     string           `toml:"default_agent"`
	GitHubUsername   string           `toml:"github_username"`
	BranchPrefix     string           `toml:"branch_prefix"`
	FetchOnCreate    bool             `toml:"fetch_on_create"`
	AllowedRepoPaths []string         `toml:"allowed_repo_paths"`
	Repos            []RepoConfig     `toml:"repos"`
	StatusBar        StatusBar        `toml:"status_bar"`
	Keybindings      Keybindings      `toml:"keybindings"`
	Notifications    Notifications    `toml:"notifications"`
	Messages         Messages         `toml:"messages"`
	Sandbox          SandboxConfig    `toml:"sandbox"`
	Approvals        Approvals        `toml:"approvals"`
	Agents           map[string]Agent `toml:"agents"`
}

type RepoConfig struct {
	Path            string `toml:"path"`
	AllowConcurrent bool   `toml:"allow_concurrent"`
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
	return ParseDurationWithDays(m.MaxAge)
}

func ParseDurationWithDays(s string) time.Duration {
	var total time.Duration
	if i := strings.Index(s, "d"); i > 0 {
		var days int
		if _, err := fmt.Sscanf(s[:i+1], "%dd", &days); err == nil {
			total = time.Duration(days) * 24 * time.Hour
			s = s[i+1:]
		}
	}
	if s == "" {
		return total
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0
	}
	return total + d
}

type Keybindings struct {
	Prefix        string `toml:"prefix"`
	NewSession    string `toml:"new_session"`
	ForkSession   string `toml:"fork_session"`
	DeleteSession string `toml:"delete_session"`
	Detach        string `toml:"detach"`
	SessionList   string `toml:"session_list"`
	NextSession   string `toml:"next_session"`
	PrevSession   string `toml:"prev_session"`
	LastSession   string `toml:"last_session"`
	ResumeSession string `toml:"resume_session"`
	RenameSession string `toml:"rename_session"`
	Search        string `toml:"search"`
	ScrollMode    string `toml:"scroll_mode"`
	Shell         string `toml:"shell"`
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

func (a Approvals) TimeoutDuration() time.Duration {
	if a.Timeout == "" {
		return 10 * time.Minute
	}
	return ParseDurationWithDays(a.Timeout)
}

type Agent struct {
	Command     string            `toml:"command"`
	Args        []string          `toml:"args"`
	ResumeArgs  []string          `toml:"resume_args"`
	ForkArgs    []string          `toml:"fork_args"`
	Env         map[string]string `toml:"env"`
	IdleTimeout string            `toml:"idle_timeout"`
	Sandbox     SandboxConfig     `toml:"sandbox"`
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
	return def
}

func Default() *Config {
	return &Config{
		DefaultAgent:  "claude",
		BranchPrefix:  "{username}/graith",
		FetchOnCreate: true,
		StatusBar: StatusBar{
			Enabled:  true,
			Position: "bottom",
		},
		Notifications: Notifications{
			Enabled:    true,
			OnApproval: true,
		},
		Approvals: Approvals{
			Mode:    "prompt",
			Timeout: "10m",
		},
		Keybindings: Keybindings{
			Prefix: "ctrl+b", NewSession: "c", ForkSession: "f",
			DeleteSession: "x", Detach: "d", SessionList: "w",
			NextSession: "n", PrevSession: "p", LastSession: "l", ResumeSession: "R",
			RenameSession: ",", Search: "/", ScrollMode: "[", Shell: "s",
		},
		Agents: map[string]Agent{
			"claude": {
				Command:    "claude",
				Args:       []string{"--session-id", "{agent_session_id}"},
				ResumeArgs: []string{"--resume", "{agent_session_id}"},
				ForkArgs:   []string{"--session-id", "{agent_session_id}", "--fork-session", "{fork_source_agent_session_id}"},
			},
			"codex": {
				Command:    "codex",
				Args:       []string{},
				ResumeArgs: []string{"resume", "--last"},
				ForkArgs:   []string{"fork", "{fork_source_agent_session_id}"},
			},
			"opencode": {Command: "opencode", Args: []string{}, ResumeArgs: []string{"--session", "{agent_session_id}"}},
			"agy":      {Command: "agy", Args: []string{}, ResumeArgs: []string{"--conversation", "{agent_session_id}"}},
		},
	}
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
