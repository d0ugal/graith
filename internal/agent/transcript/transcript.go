// Package transcript reads an AI coding agent's on-disk conversation
// transcript and renders it to a neutral, agent-agnostic Markdown document.
//
// It supports Claude Code and Codex as source agents. The rendered output is
// handed to a different agent during an in-place migration (see
// docs/design/2026-06-24-cross-agent-conversation-migration-design.md) so the
// new agent can continue the work with the full readable history.
//
// Reading is deliberately defensive: undocumented, drifting formats and
// partially-written (live) files are tolerated by skipping unparseable lines
// and counting them, rather than failing.
package transcript

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Agent identifiers for supported source transcripts.
const (
	AgentClaude = "claude"
	AgentCodex  = "codex"
)

// ErrNoTurns is returned by Read when a transcript parsed successfully but
// contained no usable conversation turns. Callers use this to fail fast before
// disrupting a running session.
var ErrNoTurns = errors.New("transcript contains no usable turns")

// ErrUnsupportedAgent is returned for source agents without a reader.
var ErrUnsupportedAgent = errors.New("unsupported source agent for migration")

// Role classifies a turn in the neutral model.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
	// RoleContext is historical developer/system context (e.g. Codex
	// `developer` messages). It is rendered as background, never promoted to
	// live instructions.
	RoleContext Role = "context"
)

// ToolCall is a single tool invocation and its result, flattened from whatever
// nested or call-id-linked representation the source agent used.
type ToolCall struct {
	Name   string
	Args   string
	Output string
	Failed bool
}

// Turn is one neutral conversation turn.
type Turn struct {
	Role      Role
	Text      string
	Timestamp time.Time
	Tool      *ToolCall // non-nil when Role == RoleTool
	SrcAgent  string    // source agent that produced the turn
}

// Conversation is the parsed, normalized transcript.
type Conversation struct {
	SrcAgent     string
	Turns        []Turn
	DroppedLines int // unparseable/skipped lines (format drift, partial tail)
	Truncated    bool
}

// reader parses an agent's transcript file into ordered turns.
type reader interface {
	read(path string, opts readOptions) ([]Turn, int, bool, error)
}

// ReadOptions bounds transcript parsing for latency-sensitive callers such as
// search. Zero values preserve the full-read behaviour used by migration and
// token accounting.
type ReadOptions struct {
	Context           context.Context
	MaxBytesPerSource int64
	MaxTurnsPerSource int
}

type readOptions struct {
	ctx               context.Context
	maxBytesPerSource int64
	maxTurnsPerSource int
}

func readerFor(agent string) (reader, error) {
	switch agent {
	case AgentClaude:
		return claudeReader{}, nil
	case AgentCodex:
		return codexReader{}, nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedAgent, agent)
	}
}

// Supported reports whether an agent can be used as a migration source.
func Supported(agent string) bool {
	_, err := readerFor(agent)
	return err == nil
}

// Read locates and parses the transcript for a session. agentSessionID is the
// id graith tracks for the source agent (used to locate Claude transcripts);
// worktreePath is the session's working directory (used to locate Codex
// transcripts and as a fallback). Returns ErrNoTurns if the transcript parsed
// but yielded nothing usable.
func Read(agent, agentSessionID, worktreePath string) (*Conversation, error) {
	return ReadWithRoot(agent, agentSessionID, worktreePath, "")
}

// ReadWithRoot is Read scoped to an explicit agent-native state root when the
// provider supports one.
func ReadWithRoot(agent, agentSessionID, worktreePath, stateRoot string) (*Conversation, error) {
	sources, err := LocateWithRoot(agent, agentSessionID, worktreePath, stateRoot)
	if err != nil {
		return nil, err
	}

	return ReadFrom(agent, sources)
}

// ReadFrom parses already-located transcript sources. It exists for callers
// that fingerprint sources before parsing, such as token accounting and search.
func ReadFrom(agent string, sources []Source) (*Conversation, error) {
	return ReadFromWithOptions(agent, sources, ReadOptions{})
}

// ReadFromWithOptions parses already-located transcript sources with optional
// read bounds.
func ReadFromWithOptions(agent string, sources []Source, opts ReadOptions) (*Conversation, error) {
	r, err := readerFor(agent)
	if err != nil {
		return nil, err
	}

	readOpts := normalizeReadOptions(opts)

	var (
		turns     []Turn
		dropped   int
		truncated bool
	)

	for _, source := range sources {
		if err := readOpts.contextErr(); err != nil {
			return nil, err
		}

		srcTurns, srcDropped, srcTruncated, readErr := r.read(source.Path, readOpts)
		if readErr != nil {
			return nil, fmt.Errorf("read %s transcript %s: %w", agent, source.Path, readErr)
		}

		turns = append(turns, srcTurns...)
		dropped += srcDropped
		truncated = truncated || srcTruncated
	}

	turns = pairToolOutputs(turns)
	if len(turns) == 0 {
		if truncated {
			return &Conversation{SrcAgent: agent, Turns: nil, DroppedLines: dropped, Truncated: true}, nil
		}

		return nil, ErrNoTurns
	}

	for i := range turns {
		turns[i].SrcAgent = agent
	}

	return &Conversation{SrcAgent: agent, Turns: turns, DroppedLines: dropped, Truncated: truncated}, nil
}

// locateWithRoot resolves the on-disk transcript path for an agent/session,
// optionally scoped to an agent-native state root such as CODEX_HOME.
func locateWithRoot(agent, agentSessionID, worktreePath, stateRoot string) (string, error) {
	switch agent {
	case AgentClaude:
		return locateClaude(agentSessionID)
	case AgentCodex:
		return locateCodexInRoot(stateRoot, agentSessionID, worktreePath)
	default:
		return "", fmt.Errorf("%w: %q", ErrUnsupportedAgent, agent)
	}
}

// pairToolOutputs is a no-op hook retained for symmetry; readers already pair
// tool calls with their outputs. It exists so future cross-record pairing can
// be added in one place without touching each reader.
func pairToolOutputs(turns []Turn) []Turn {
	return turns
}

func normalizeReadOptions(opts ReadOptions) readOptions {
	return readOptions{
		ctx:               opts.Context,
		maxBytesPerSource: opts.MaxBytesPerSource,
		maxTurnsPerSource: opts.MaxTurnsPerSource,
	}
}

func (o readOptions) contextErr() error {
	if o.ctx == nil {
		return nil
	}

	select {
	case <-o.ctx.Done():
		return o.ctx.Err()
	default:
		return nil
	}
}

func parseTimestamp(value string) time.Time {
	if value == "" {
		return time.Time{}
	}

	if ts, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return ts
	}

	if ts, err := time.Parse(time.RFC3339, value); err == nil {
		return ts
	}

	return time.Time{}
}
