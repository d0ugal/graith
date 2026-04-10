package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/dougalmatthews/graith/internal/config"
	"github.com/dougalmatthews/graith/internal/git"
	grpty "github.com/dougalmatthews/graith/internal/pty"
)

// SessionManager orchestrates PTY sessions, state persistence, and git worktrees.
type SessionManager struct {
	mu       sync.RWMutex
	state    *State
	sessions map[string]*grpty.Session
	cfg      *config.Config
	paths    config.Paths
	log      *slog.Logger
}

// NewSessionManager creates a SessionManager with the given config and paths.
func NewSessionManager(cfg *config.Config, paths config.Paths, log *slog.Logger) *SessionManager {
	return &SessionManager{
		state:    NewState(),
		sessions: make(map[string]*grpty.Session),
		cfg:      cfg,
		paths:    paths,
		log:      log,
	}
}

// LoadState reads persisted state from disk and reconciles dead processes.
func (sm *SessionManager) LoadState() error {
	state, err := LoadState(sm.paths.StateFile)
	if err != nil {
		return err
	}
	state.Reconcile()
	sm.state = state
	return sm.saveState()
}

func (sm *SessionManager) saveState() error {
	return SaveState(sm.paths.StateFile, sm.state)
}

func generateID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func repoHash(repoPath string) string {
	h := uint64(0)
	for _, c := range repoPath {
		h = h*31 + uint64(c)
	}
	b := make([]byte, 8)
	for i := 0; i < 8; i++ {
		b[i] = byte(h >> (i * 8))
	}
	return hex.EncodeToString(b)[:12]
}

// Create starts a new agent session in a git worktree.
func (sm *SessionManager) Create(name, agentName, repoPath, baseBranch string, rows, cols uint16) (*SessionState, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if !git.IsInsideGitRepo(repoPath) {
		return nil, fmt.Errorf("not inside a git repository: %s", repoPath)
	}

	repoRoot, err := git.RepoRootPath(repoPath)
	if err != nil {
		return nil, fmt.Errorf("find repo root: %w", err)
	}

	if baseBranch == "" {
		baseBranch, err = git.DiscoverDefaultBranch(repoRoot)
		if err != nil {
			return nil, err
		}
	}

	agent, ok := sm.cfg.Agents[agentName]
	if !ok {
		return nil, fmt.Errorf("unknown agent %q", agentName)
	}

	id := generateID()
	repoName := filepath.Base(repoRoot)

	username := sm.cfg.GitHubUsername
	if username == "" {
		username, _ = git.DiscoverGitHubUsername(repoRoot)
	}

	branchPrefix, _ := config.Expand(sm.cfg.BranchPrefix, config.TemplateVars{Username: username})
	branchName := fmt.Sprintf("%s/%s-%s", branchPrefix, name, id)

	worktreePath := filepath.Join(sm.paths.DataDir, "worktrees", repoHash(repoRoot), id)

	if err := git.SetupSession(repoRoot, worktreePath, branchName, baseBranch, sm.cfg.FetchOnCreate); err != nil {
		return nil, fmt.Errorf("setup git session: %w", err)
	}

	agentSessionID := ""
	if agentName == "claude" {
		b := make([]byte, 16)
		_, _ = rand.Read(b)
		agentSessionID = fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
	}

	vars := config.TemplateVars{
		Username:       username,
		AgentSessionID: agentSessionID,
		SessionName:    name,
		SessionID:      id,
		WorktreePath:   worktreePath,
	}
	expandedArgs, err := config.ExpandSlice(agent.Args, vars)
	if err != nil {
		_ = git.TeardownSession(repoRoot, worktreePath, branchName)
		return nil, fmt.Errorf("expand agent args: %w", err)
	}

	logPath := filepath.Join(sm.paths.LogDir, id+".log")

	ptySess, err := grpty.NewSession(grpty.SessionOpts{
		ID:         id,
		Command:    agent.Command,
		Args:       expandedArgs,
		Dir:        worktreePath,
		Env:        agent.Env,
		Rows:       rows,
		Cols:       cols,
		LogPath:    logPath,
		MaxLogSize: 100 * 1024 * 1024,
	})
	if err != nil {
		_ = git.TeardownSession(repoRoot, worktreePath, branchName)
		return nil, fmt.Errorf("start pty session: %w", err)
	}

	sessState := &SessionState{
		ID:             id,
		Name:           name,
		RepoPath:       repoRoot,
		RepoName:       repoName,
		WorktreePath:   worktreePath,
		Branch:         branchName,
		BaseBranch:     baseBranch,
		Agent:          agentName,
		AgentSessionID: agentSessionID,
		Status:         StatusRunning,
		PID:            ptySess.Cmd.Process.Pid,
		CreatedAt:      time.Now().UTC(),
	}

	sm.state.Sessions[id] = sessState
	sm.sessions[id] = ptySess

	go sm.watchSession(id, ptySess)

	if err := sm.saveState(); err != nil {
		sm.log.Error("failed to save state", "err", err)
	}

	return sessState, nil
}

// watchSession waits for a PTY session to exit and updates state accordingly.
func (sm *SessionManager) watchSession(id string, sess *grpty.Session) {
	<-sess.Done()

	sm.mu.Lock()
	defer sm.mu.Unlock()

	if s, ok := sm.state.Sessions[id]; ok {
		exitCode := sess.ExitCode()
		s.Status = StatusStopped
		s.ExitCode = &exitCode
		s.PID = 0
		_ = sm.saveState()
	}

	sm.log.Info("session exited", "id", id, "exit_code", sess.ExitCode())
}

// Delete stops a session, removes its worktree/branch, and deletes state.
func (sm *SessionManager) Delete(id string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sessState, ok := sm.state.Sessions[id]
	if !ok {
		return fmt.Errorf("session %q not found", id)
	}

	if ptySess, ok := sm.sessions[id]; ok {
		if !ptySess.Exited() {
			_ = ptySess.Kill()
			select {
			case <-ptySess.Done():
			case <-time.After(5 * time.Second):
				_ = ptySess.ForceKill()
			}
		}
		ptySess.Close()
		delete(sm.sessions, id)
	}

	_ = git.TeardownSession(sessState.RepoPath, sessState.WorktreePath, sessState.Branch)
	_ = os.Remove(filepath.Join(sm.paths.LogDir, id+".log"))

	delete(sm.state.Sessions, id)
	return sm.saveState()
}

// Rename changes the display name of a session.
func (sm *SessionManager) Rename(id, newName string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	s, ok := sm.state.Sessions[id]
	if !ok {
		return fmt.Errorf("session %q not found", id)
	}
	s.Name = newName
	return sm.saveState()
}

// List returns all known session states.
func (sm *SessionManager) List() []*SessionState {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	list := make([]*SessionState, 0, len(sm.state.Sessions))
	for _, s := range sm.state.Sessions {
		list = append(list, s)
	}
	return list
}

// Get returns a session state by ID.
func (sm *SessionManager) Get(id string) (*SessionState, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	s, ok := sm.state.Sessions[id]
	return s, ok
}

// GetPTY returns the live PTY session by ID.
func (sm *SessionManager) GetPTY(id string) (*grpty.Session, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	s, ok := sm.sessions[id]
	return s, ok
}

// FindByName looks up a session by exact name, then by prefix match.
func (sm *SessionManager) FindByName(name string) (*SessionState, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	for _, s := range sm.state.Sessions {
		if s.Name == name {
			return s, true
		}
	}

	for _, s := range sm.state.Sessions {
		if len(name) > 0 && len(s.Name) >= len(name) && s.Name[:len(name)] == name {
			return s, true
		}
	}

	return nil, false
}

// StopAll gracefully terminates all running sessions.
func (sm *SessionManager) StopAll(ctx context.Context) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	for id, sess := range sm.sessions {
		if !sess.Exited() {
			sm.log.Info("stopping session", "id", id)
			_ = sess.Kill()
		}
	}

	deadline := time.After(5 * time.Second)
	for id, sess := range sm.sessions {
		select {
		case <-sess.Done():
		case <-deadline:
			sm.log.Warn("force killing session", "id", id)
			_ = sess.ForceKill()
		}
	}
}

// Run starts the daemon: acquires PID file, listens on the Unix socket,
// serves connections, and blocks until SIGTERM/SIGINT.
func Run(cfg *config.Config, paths config.Paths) error {
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	if err := paths.EnsureDirs(); err != nil {
		return err
	}

	if err := AcquirePIDFile(paths.PIDFile); err != nil {
		return err
	}
	defer ReleasePIDFile(paths.PIDFile)

	l, err := Listen(paths.SocketPath)
	if err != nil {
		return err
	}
	defer os.Remove(paths.SocketPath)
	defer l.Close()

	sm := NewSessionManager(cfg, paths, log)
	if err := sm.LoadState(); err != nil {
		log.Warn("failed to load state", "err", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv := NewServer(l, func(ctx context.Context, conn net.Conn) {
		HandleConnection(ctx, conn, sm, log)
	}, log)

	go srv.Serve(ctx)

	log.Info("daemon started", "socket", paths.SocketPath, "pid", os.Getpid())

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	<-sigCh

	log.Info("shutting down")
	cancel()
	sm.StopAll(ctx)
	srv.Shutdown()

	return nil
}
