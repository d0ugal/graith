package daemon

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/d0ugal/graith/internal/config"
)

const CurrentStateVersion = 10

type SessionStatus string

const (
	StatusRunning  SessionStatus = "running"
	StatusStopped  SessionStatus = "stopped"
	StatusErrored  SessionStatus = "errored"
	StatusDeleting SessionStatus = "deleting"
	StatusCreating SessionStatus = "creating"
)

// CreationConfig captures the agent and sandbox configuration at session
// creation time so the overlay can detect when the live config has diverged.
type CreationConfig struct {
	Agent         config.Agent         `json:"agent"`
	SandboxConfig config.SandboxConfig `json:"sandbox_config"`
}

type SessionState struct {
	ID                     string                `json:"id"`
	ParentID               string                `json:"parent_id,omitempty"`
	Name                   string                `json:"name"`
	RepoPath               string                `json:"repo_path"`
	RepoName               string                `json:"repo_name"`
	WorktreePath           string                `json:"worktree_path"`
	Branch                 string                `json:"branch"`
	BaseBranch             string                `json:"base_branch"`
	Agent                  string                `json:"agent"`
	AgentSessionID         string                `json:"agent_session_id,omitempty"`
	Model                  string                `json:"model,omitempty"`
	Status                 SessionStatus         `json:"status"`
	AgentStatus            string                `json:"agent_status,omitempty"`
	StatusChangedAt        time.Time             `json:"status_changed_at"`
	IdleSince              *time.Time            `json:"-"`
	GitDirty               bool                  `json:"-"`
	GitUnpushed            int                   `json:"-"`
	HookModel              string                `json:"-"`
	HookToolName           string                `json:"-"`
	HookCostUSD            *float64              `json:"-"`
	HookContextPercent     *float64              `json:"-"`
	ExitCode               *int                  `json:"exit_code,omitempty"`
	ExitSignal             string                `json:"exit_signal,omitempty"`
	PID                    int                   `json:"pid,omitempty"`
	PIDStartTime           int64                 `json:"pid_start_time,omitempty"`
	Sandboxed              bool                  `json:"sandboxed,omitempty"`
	SandboxConfig          *config.SandboxConfig `json:"sandbox_config,omitempty"`
	SharedWorktree         bool                  `json:"shared_worktree,omitempty"`
	SharedWorktreeSourceID string                `json:"shared_worktree_source_id,omitempty"`
	InPlace                bool                  `json:"in_place,omitempty"`
	Includes               []IncludedRepoState   `json:"includes,omitempty"`
	AgentHooks             bool                  `json:"agent_hooks,omitempty"`
	ApprovalsEnabled       bool                  `json:"approvals_enabled,omitempty"` // deprecated: migrated to AgentHooks in v4
	Starred                bool                  `json:"starred,omitempty"`
	SystemKind             string                `json:"system_kind,omitempty"`
	StopReason             string                `json:"stop_reason,omitempty"`
	BackoffLevel           int                   `json:"backoff_level,omitempty"`
	FreshStart             bool                  `json:"fresh_start,omitempty"`
	LastStartedAt          time.Time             `json:"last_started_at,omitempty"`
	SummaryText            string                `json:"summary_text,omitempty"`
	SummarySetAt           *time.Time            `json:"summary_set_at,omitempty"`
	SummaryTTL             int                   `json:"summary_ttl,omitempty"`
	LastOutputAt           *time.Time            `json:"last_output_at,omitempty"`
	CreatedAt              time.Time             `json:"created_at"`
	LastAttachedAt         *time.Time            `json:"last_attached_at,omitempty"`
	CreationCfg            *CreationConfig       `json:"creation_config,omitempty"`
	Token                  string                `json:"token,omitempty"`
}

type IncludedRepoState struct {
	RepoPath     string `json:"repo_path"`
	RepoName     string `json:"repo_name"`
	WorktreePath string `json:"worktree_path"`
	Branch       string `json:"branch"`
	BaseBranch   string `json:"base_branch"`

	dirty    bool `json:"-"`
	unpushed int  `json:"-"`
}

func cloneSessionState(s *SessionState) SessionState {
	c := *s
	if len(s.Includes) > 0 {
		c.Includes = make([]IncludedRepoState, len(s.Includes))
		copy(c.Includes, s.Includes)
	}
	return c
}

type State struct {
	Version  int                      `json:"version"`
	Sessions map[string]*SessionState `json:"sessions"`
}

func NewState() *State {
	return &State{Version: CurrentStateVersion, Sessions: make(map[string]*SessionState)}
}

func LoadState(path string) (*State, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return NewState(), nil
		}
		return nil, fmt.Errorf("read state: %w", err)
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		slog.Warn("corrupted state file, starting fresh", "path", path, "err", err)
		return NewState(), nil
	}
	if state.Sessions == nil {
		state.Sessions = make(map[string]*SessionState)
	}

	if state.Version > CurrentStateVersion {
		return nil, fmt.Errorf("state file version %d is newer than this binary supports (%d) — upgrade graith", state.Version, CurrentStateVersion)
	}

	if err := migrateState(&state); err != nil {
		slog.Warn("state migration failed, starting fresh", "path", path, "err", err)
		return NewState(), nil
	}

	return &state, nil
}

func SaveState(path string, state *State) error {
	state.Version = CurrentStateVersion
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	return writeFileAtomic(path, data)
}

var migrations = map[int]func(*State) error{
	0: migrateV0ToV1,
	1: migrateV1ToV2,
	2: migrateV2ToV3,
	3: migrateV3ToV4,
	4: migrateV4ToV5,
	5: migrateV5ToV6,
	6: migrateV6ToV7,
	7: migrateV7ToV8,
	8: migrateV8ToV9,
	9: migrateV9ToV10,
}

func generateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func migrateState(state *State) error {
	for state.Version < CurrentStateVersion {
		fn, ok := migrations[state.Version]
		if !ok {
			return fmt.Errorf("no migration from version %d", state.Version)
		}
		if err := fn(state); err != nil {
			return fmt.Errorf("migrate v%d→v%d: %w", state.Version, state.Version+1, err)
		}
		state.Version++
	}
	return nil
}

// migrateV0ToV1 is a no-op: v0 and v1 share the same schema. Kept because
// removing it would break the migration chain for any state file at version 0.
func migrateV0ToV1(_ *State) error {
	return nil
}

func migrateV1ToV2(_ *State) error {
	return nil
}

// migrateV2ToV3 is a no-op: v3 adds the optional creation_config field which
// defaults to nil for existing sessions (shown as "unknown" rather than stale).
func migrateV2ToV3(_ *State) error {
	return nil
}

// migrateV3ToV4 transfers the renamed approvals_enabled field to agent_hooks.
func migrateV3ToV4(state *State) error {
	for _, s := range state.Sessions {
		if s.ApprovalsEnabled {
			s.AgentHooks = true
			s.ApprovalsEnabled = false
		}
	}
	return nil
}

// migrateV4ToV5 backfills StatusChangedAt from CreatedAt for existing sessions
// that predate the field. This gives a conservative "last changed at" of session
// creation, meaning these sessions will sort oldest in "Needs Attention" views.
func migrateV4ToV5(state *State) error {
	for _, s := range state.Sessions {
		if s.StatusChangedAt.IsZero() {
			s.StatusChangedAt = s.CreatedAt
		}
	}
	return nil
}

// migrateV5ToV6 is a no-op: v6 adds the optional starred field which
// defaults to false for existing sessions.
func migrateV5ToV6(_ *State) error {
	return nil
}

// migrateV6ToV7 is a no-op: v7 adds optional summary/output fields which
// default to zero values for existing sessions.
func migrateV6ToV7(_ *State) error {
	return nil
}

// migrateV7ToV8 is a no-op: v8 adds optional orchestrator fields (system_kind,
// stop_reason, backoff_level, last_started_at) which default to zero values.
func migrateV7ToV8(_ *State) error {
	return nil
}

// migrateV8ToV9 is a no-op: v9 adds the optional pid_start_time field which
// defaults to 0 (unrecorded) for existing sessions.
func migrateV8ToV9(_ *State) error {
	return nil
}

// migrateV9ToV10 generates auth tokens for all existing sessions.
func migrateV9ToV10(state *State) error {
	for id, s := range state.Sessions {
		if s.Token != "" {
			continue
		}
		token, err := generateToken()
		if err != nil {
			return fmt.Errorf("generate token for session %s: %w", id, err)
		}
		s.Token = token
	}
	return nil
}

func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".state-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename: %w", err)
	}
	if err := syncDir(dir); err != nil {
		return fmt.Errorf("sync dir: %w", err)
	}
	return nil
}

func syncDir(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	err = d.Sync()
	d.Close()
	return err
}

func (s *State) Reconcile() {
	for id, sess := range s.Sessions {
		if sess.Status == StatusCreating {
			slog.Info("session was mid-creation when daemon stopped, marking errored", "id", id)
			sess.Status = StatusErrored
			sess.StatusChangedAt = time.Now()
			applyLifecycleSummaryLocked(sess, "Interrupted by daemon restart")
			continue
		}
		if sess.Status == StatusRunning && sess.PID > 0 {
			if !isProcessAlive(sess.PID) {
				slog.Info("session process died, marking stopped", "id", id, "pid", sess.PID)
				sess.Status = StatusStopped
				sess.StatusChangedAt = time.Now()
				sess.PID = 0
				sess.PIDStartTime = 0
				applyLifecycleSummaryLocked(sess, "Lost during daemon restart")
			}
		}
		if sess.Status == StatusDeleting {
			slog.Info("session stuck in deleting, reverting to stopped", "id", id)
			sess.Status = StatusStopped
			sess.StatusChangedAt = time.Now()
			applyLifecycleSummaryLocked(sess, "Delete interrupted by restart")
		}
	}
}

func isProcessAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	return errors.Is(err, syscall.EPERM)
}
