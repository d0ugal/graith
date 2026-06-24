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
	"errors"
	"fmt"
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
	Role     Role
	Text     string
	Tool     *ToolCall // non-nil when Role == RoleTool
	SrcAgent string    // source agent that produced the turn
}

// Conversation is the parsed, normalized transcript.
type Conversation struct {
	SrcAgent     string
	Turns        []Turn
	DroppedLines int // unparseable/skipped lines (format drift, partial tail)
}

// reader parses an agent's transcript file into ordered turns.
type reader interface {
	read(path string) ([]Turn, int, error)
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
	r, err := readerFor(agent)
	if err != nil {
		return nil, err
	}
	path, err := locate(agent, agentSessionID, worktreePath)
	if err != nil {
		return nil, err
	}
	turns, dropped, err := r.read(path)
	if err != nil {
		return nil, fmt.Errorf("read %s transcript %s: %w", agent, path, err)
	}
	turns = pairToolOutputs(turns)
	if len(turns) == 0 {
		return nil, ErrNoTurns
	}
	for i := range turns {
		turns[i].SrcAgent = agent
	}
	return &Conversation{SrcAgent: agent, Turns: turns, DroppedLines: dropped}, nil
}

// locate resolves the on-disk transcript path for an agent/session.
func locate(agent, agentSessionID, worktreePath string) (string, error) {
	switch agent {
	case AgentClaude:
		return locateClaude(agentSessionID)
	case AgentCodex:
		return locateCodex(agentSessionID, worktreePath)
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
