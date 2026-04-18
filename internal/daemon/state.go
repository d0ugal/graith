package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const CurrentStateVersion = 1

type SessionStatus string

const (
	StatusRunning SessionStatus = "running"
	StatusStopped SessionStatus = "stopped"
	StatusErrored SessionStatus = "errored"
)

type SessionState struct {
	ID             string        `json:"id"`
	Name           string        `json:"name"`
	RepoPath       string        `json:"repo_path"`
	RepoName       string        `json:"repo_name"`
	WorktreePath   string        `json:"worktree_path"`
	Branch         string        `json:"branch"`
	BaseBranch     string        `json:"base_branch"`
	Agent          string        `json:"agent"`
	AgentSessionID string        `json:"agent_session_id,omitempty"`
	Status         SessionStatus `json:"status"`
	AgentStatus    string        `json:"agent_status,omitempty"`
	IdleSince      *time.Time    `json:"-"`
	GitDirty       bool          `json:"-"`
	GitUnpushed    int           `json:"-"`
	ExitCode       *int          `json:"exit_code,omitempty"`
	PID            int           `json:"pid,omitempty"`
	CreatedAt      time.Time     `json:"created_at"`
	LastAttachedAt *time.Time    `json:"last_attached_at,omitempty"`
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
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

func (s *State) Reconcile() {
	for id, sess := range s.Sessions {
		if sess.Status == StatusRunning && sess.PID > 0 {
			if !isProcessAlive(sess.PID) {
				slog.Info("session process died, marking stopped", "id", id, "pid", sess.PID)
				sess.Status = StatusStopped
				sess.PID = 0
			}
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
