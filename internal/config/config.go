package config

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/pelletier/go-toml/v2"
)

type Config struct {
	DefaultAgent   string           `toml:"default_agent"`
	GitHubUsername string           `toml:"github_username"`
	BranchPrefix   string           `toml:"branch_prefix"`
	FetchOnCreate  bool             `toml:"fetch_on_create"`
	Keybindings    Keybindings      `toml:"keybindings"`
	Agents         map[string]Agent `toml:"agents"`
}

type Keybindings struct {
	Prefix        string `toml:"prefix"`
	NewSession    string `toml:"new_session"`
	DeleteSession string `toml:"delete_session"`
	Detach        string `toml:"detach"`
	SessionList   string `toml:"session_list"`
	NextSession   string `toml:"next_session"`
	PrevSession   string `toml:"prev_session"`
	ResumeSession string `toml:"resume_session"`
	RenameSession string `toml:"rename_session"`
	Search        string `toml:"search"`
	ScrollMode    string `toml:"scroll_mode"`
	Shell         string `toml:"shell"`
}

type Agent struct {
	Command     string            `toml:"command"`
	Args        []string          `toml:"args"`
	ResumeArgs  []string          `toml:"resume_args"`
	Env         map[string]string `toml:"env"`
	IdleTimeout string            `toml:"idle_timeout"`
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

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}
	cfg := Default()
	if err := toml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing config %s: %w", path, err)
	}
	return cfg, nil
}

func Default() *Config {
	return &Config{
		DefaultAgent:  "claude",
		BranchPrefix:  "{username}/graith",
		FetchOnCreate: true,
		Keybindings: Keybindings{
			Prefix: "ctrl+b", NewSession: "c", DeleteSession: "x",
			Detach: "d", SessionList: "w", NextSession: "n",
			PrevSession: "p", ResumeSession: "R", RenameSession: ",",
			Search: "/", ScrollMode: "[", Shell: "s",
		},
		Agents: map[string]Agent{
			"claude":   {Command: "claude", Args: []string{"--session-id", "{agent_session_id}"}, ResumeArgs: []string{"--resume", "{agent_session_id}"}},
			"codex":    {Command: "codex", Args: []string{}, ResumeArgs: []string{"resume", "--last"}},
			"opencode": {Command: "opencode", Args: []string{}, ResumeArgs: []string{"--session", "{agent_session_id}"}},
			"agy":      {Command: "agy", Args: []string{}, ResumeArgs: []string{"--conversation", "{agent_session_id}"}},
		},
	}
}

func LoadOrDefault(path string) (*Config, error) {
	if path == "" {
		p := ResolvePaths()
		path = p.ConfigFile
	}
	cfg, err := Load(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Default(), nil
		}
		return nil, err
	}
	return cfg, nil
}
