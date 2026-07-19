package commandpolicy

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/d0ugal/graith/internal/commandpolicy/localmost"
)

type builtinBackend struct {
	loadEngine func(Config) (*localmost.Engine, error)
}

type builtinWorkResult struct {
	decision Decision
	engine   *localmost.Engine
	err      error
}

// builtinWorkSlot bounds timed-out work that cannot be interrupted inside a
// filesystem read or parser call. A timed-out evaluation may finish in the
// background, but at most one such worker can exist; later requests wait on the
// same slot with their own deadline and fail closed instead of accumulating
// goroutines.
var builtinWorkSlot = make(chan struct{}, 1)

func (builtinBackend) Name() string { return BackendBuiltin }

func (b builtinBackend) Availability(cfg Config) Availability {
	if len(cfg.BuiltinInline) == 0 && strings.TrimSpace(cfg.BuiltinConfig) == "" {
		return Availability{CanEnforce: false, Detail: `command policy backend "builtin" requires [command_policy.builtin] rules or config`}
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.execTimeout())
	defer cancel()

	result, err := runBuiltinWork(ctx, func() builtinWorkResult {
		engine, loadErr := b.engine(cfg)
		return builtinWorkResult{engine: engine, err: loadErr}
	})
	if err == nil {
		err = result.err
	}

	if err != nil {
		return Availability{CanEnforce: false, Detail: fmt.Sprintf("command policy configuration is invalid: %v", err)}
	}

	return Availability{CanEnforce: true}
}

type engineCacheEntry struct {
	hash   [sha256.Size]byte
	engine *localmost.Engine
}

var (
	engineCacheMu sync.Mutex
	engineCache   = map[string]engineCacheEntry{}
)

func builtinEngine(cfg Config) (*localmost.Engine, error) {
	if len(cfg.BuiltinInline) > 0 {
		return localmost.Parse(cfg.BuiltinInline)
	}

	path := strings.TrimSpace(cfg.BuiltinConfig)
	if path == "" {
		return nil, errors.New("no builtin command policy configured")
	}

	return loadEngineCached(path)
}

func loadEngineCached(path string) (*localmost.Engine, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read command policy config: %w", err)
	}

	hash := sha256.Sum256(data)

	engineCacheMu.Lock()
	defer engineCacheMu.Unlock()

	if entry, ok := engineCache[path]; ok && entry.hash == hash {
		return entry.engine, nil
	}

	engine, err := localmost.Parse(data)
	if err != nil {
		return nil, err
	}

	engineCache[path] = engineCacheEntry{hash: hash, engine: engine}

	return engine, nil
}

func (b builtinBackend) Evaluate(parent context.Context, req Request, cfg Config) (Decision, error) {
	ctx, cancel := context.WithTimeout(parent, cfg.execTimeout())
	defer cancel()

	result, err := runBuiltinWork(ctx, func() builtinWorkResult {
		decision, evaluateErr := b.evaluate(req, cfg)
		return builtinWorkResult{decision: decision, err: evaluateErr}
	})
	if err != nil {
		return Decision{}, fmt.Errorf("builtin command policy: %w", err)
	}

	return result.decision, result.err
}

func (b builtinBackend) evaluate(req Request, cfg Config) (Decision, error) {
	command, inScope, err := shellCommand(req.ToolName, req.ToolInput)
	if err != nil {
		return Decision{}, err
	}

	if !inScope {
		return Decision{Decision: DecisionAllow}, nil
	}

	engine, err := b.engine(cfg)
	if err != nil {
		return Decision{}, fmt.Errorf("load builtin command policy: %w", err)
	}

	policy, err := engine.Evaluate(command)
	if err != nil {
		return Decision{}, fmt.Errorf("evaluate command: %w", err)
	}

	switch policy {
	case localmost.PolicyAllow:
		return Decision{Decision: DecisionAllow}, nil
	case localmost.PolicyDeny:
		return Decision{Decision: DecisionDeny, Reason: "denied by command policy"}, nil
	default:
		return Decision{Decision: DecisionDeny, Reason: "command policy returned ask; non-interactive sessions deny immediately"}, nil
	}
}

func (b builtinBackend) engine(cfg Config) (*localmost.Engine, error) {
	if b.loadEngine != nil {
		return b.loadEngine(cfg)
	}

	return builtinEngine(cfg)
}

func runBuiltinWork(ctx context.Context, work func() builtinWorkResult) (builtinWorkResult, error) {
	select {
	case builtinWorkSlot <- struct{}{}:
	case <-ctx.Done():
		return builtinWorkResult{}, ctx.Err()
	}

	resultCh := make(chan builtinWorkResult, 1)

	go func() {
		defer func() { <-builtinWorkSlot }()

		resultCh <- work()
	}()

	select {
	case result := <-resultCh:
		return result, nil
	case <-ctx.Done():
		return builtinWorkResult{}, ctx.Err()
	}
}
