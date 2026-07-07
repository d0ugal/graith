package approvals

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/d0ugal/graith/internal/approvals/localmost"
)

// builtinBackend is graith's own clean-room, behaviourally-compatible
// reimplementation of localmost's rule engine (see internal/approvals/localmost).
// It evaluates Bash commands against a localmost-format config.json; other tools
// defer to the human, matching localmost's Bash-matcher scope.
type builtinBackend struct{}

func (builtinBackend) Name() string { return BackendBuiltin }

func (builtinBackend) Availability(cfg Config) Availability {
	if len(cfg.BuiltinInline) == 0 && strings.TrimSpace(cfg.BuiltinConfig) == "" {
		return Availability{
			CanEnforce: false,
			Detail:     `approvals backend "builtin" requires [approvals.builtin] config (external file) or inline rules to be set`,
		}
	}

	if _, err := builtinEngine(cfg); err != nil {
		return Availability{
			CanEnforce: false,
			Detail:     fmt.Sprintf(`approvals backend "builtin" config is invalid: %v`, err),
		}
	}

	return Availability{CanEnforce: true}
}

// engineCacheEntry is a compiled engine plus the file identity it was compiled
// from, so a reload can be skipped while the file is unchanged.
type engineCacheEntry struct {
	modTime time.Time
	size    int64
	engine  *localmost.Engine
}

var (
	engineCacheMu sync.Mutex
	engineCache   = map[string]engineCacheEntry{}
)

// builtinEngine compiles the localmost engine for the builtin backend, from the
// inline ruleset when present, else from the external config.json path.
func builtinEngine(cfg Config) (*localmost.Engine, error) {
	if len(cfg.BuiltinInline) > 0 {
		return localmost.Parse(cfg.BuiltinInline)
	}

	path := strings.TrimSpace(cfg.BuiltinConfig)
	if path == "" {
		return nil, fmt.Errorf("no builtin approvals config configured")
	}

	return loadEngineCached(path)
}

// loadEngineCached returns the compiled engine for the config.json at path,
// reusing a previously compiled engine while the file's mtime and size are
// unchanged. On a stat or reload failure it returns the error rather than a
// stale engine, and leaves any existing cache entry untouched so subsequent
// calls keep surfacing the failure until the file is valid again.
func loadEngineCached(path string) (*localmost.Engine, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat approvals config: %w", err)
	}

	modTime := info.ModTime()
	size := info.Size()

	engineCacheMu.Lock()
	defer engineCacheMu.Unlock()

	if entry, ok := engineCache[path]; ok && entry.modTime.Equal(modTime) && entry.size == size {
		return entry.engine, nil
	}

	engine, err := localmost.Load(path)
	if err != nil {
		return nil, err
	}

	engineCache[path] = engineCacheEntry{modTime: modTime, size: size, engine: engine}

	return engine, nil
}

func (builtinBackend) Decide(_ context.Context, req Request, cfg Config) (Decision, error) {
	// localmost only reasons about shell commands; defer other tools.
	if req.ToolName != "Bash" {
		return Decision{Decision: DecisionDefer}, nil
	}

	engine, err := builtinEngine(cfg)
	if err != nil {
		return Decision{Decision: DecisionDefer}, fmt.Errorf("load builtin approvals config: %w", err)
	}

	command := bashCommand(req.ToolInput)
	if command == "" {
		return Decision{Decision: DecisionDefer}, nil
	}

	policy, err := engine.Evaluate(command)
	if err != nil {
		return Decision{Decision: DecisionDefer}, fmt.Errorf("evaluate command: %w", err)
	}

	switch policy {
	case localmost.PolicyAllow:
		return Decision{Decision: DecisionAllow}, nil
	case localmost.PolicyDeny:
		return Decision{Decision: DecisionBlock, Reason: "blocked by approvals rules"}, nil
	default: // ask
		// Documented divergence: localmost's askNoninteractive (map ask->deny
		// when no human is present) is not enforced here. A pure backend can't
		// observe whether a client is attached, so "ask" always defers to the
		// human queue; an unattended session still ends in block via the
		// approval timeout, just not immediately.
		return Decision{Decision: DecisionDefer}, nil
	}
}

// bashCommand extracts the command string from a Bash tool's input JSON.
func bashCommand(toolInput string) string {
	if toolInput == "" {
		return ""
	}

	var ti struct {
		Command string `json:"command"`
	}

	if json.Unmarshal([]byte(toolInput), &ti) != nil {
		return ""
	}

	return ti.Command
}
