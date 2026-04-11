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

type attachedClient struct {
	conn net.Conn
	kick func()
}

// SessionManager orchestrates PTY sessions, state persistence, and git worktrees.
type SessionManager struct {
	mu              sync.RWMutex
	state           *State
	sessions        map[string]*grpty.Session
	attachedClients map[string]*attachedClient
	cfg             *config.Config
	paths           config.Paths
	log             *slog.Logger
	configFile      string
	upgradeCh       chan struct{}
}

// NewSessionManager creates a SessionManager with the given config and paths.
func NewSessionManager(cfg *config.Config, paths config.Paths, log *slog.Logger) *SessionManager {
	return &SessionManager{
		state:           NewState(),
		sessions:        make(map[string]*grpty.Session),
		attachedClients: make(map[string]*attachedClient),
		cfg:             cfg,
		paths:           paths,
		log:             log,
	}
}

func (sm *SessionManager) KickAttachedClient(sessionID string) {
	sm.mu.Lock()
	ac, ok := sm.attachedClients[sessionID]
	if ok {
		delete(sm.attachedClients, sessionID)
	}
	sm.mu.Unlock()
	if ok {
		ac.kick()
	}
}

func (sm *SessionManager) SetAttachedClient(sessionID string, conn net.Conn, kick func()) {
	sm.mu.Lock()
	sm.attachedClients[sessionID] = &attachedClient{conn: conn, kick: kick}
	sm.mu.Unlock()
}

func (sm *SessionManager) ClearAttachedClient(sessionID string, conn net.Conn) {
	sm.mu.Lock()
	if ac, ok := sm.attachedClients[sessionID]; ok && ac.conn == conn {
		delete(sm.attachedClients, sessionID)
	}
	sm.mu.Unlock()
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

func (sm *SessionManager) AdoptSessions(manifest *UpgradeManifest) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	for _, us := range manifest.Sessions {
		sessState, ok := sm.state.Sessions[us.ID]
		if !ok {
			sm.log.Warn("manifest references unknown session", "id", us.ID)
			continue
		}

		logPath := filepath.Join(sm.paths.LogDir, us.ID+".log")
		ptySess, err := grpty.AdoptSession(grpty.AdoptOpts{
			ID:         us.ID,
			Fd:         uintptr(us.Fd),
			PID:        us.PID,
			LogPath:    logPath,
			MaxLogSize: 100 * 1024 * 1024,
		})
		if err != nil {
			sm.log.Warn("failed to adopt session", "id", us.ID, "err", err)
			sessState.Status = StatusStopped
			continue
		}

		sm.sessions[us.ID] = ptySess
		go sm.watchSession(us.ID, ptySess)
		sm.log.Info("adopted session", "id", us.ID, "pid", us.PID)
	}

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
func (sm *SessionManager) Create(name, agentName, repoPath, baseBranch, prompt string, rows, cols uint16) (*SessionState, error) {
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
	if username == "" {
		username = "user"
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
	if prompt != "" {
		expandedArgs = append(expandedArgs, prompt)
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

	sessState, ok := sm.state.Sessions[id]
	if !ok {
		sm.mu.Unlock()
		return fmt.Errorf("session %q not found", id)
	}

	ac, hasClient := sm.attachedClients[id]
	if hasClient {
		delete(sm.attachedClients, id)
	}

	if ptySess, ok := sm.sessions[id]; ok {
		ptySess.Detach()
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
	err := sm.saveState()
	sm.mu.Unlock()

	if hasClient {
		ac.kick()
	}

	return err
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

	for id, sess := range sm.sessions {
		select {
		case <-sess.Done():
		case <-time.After(5 * time.Second):
			sm.log.Warn("force killing session", "id", id)
			_ = sess.ForceKill()
		}
	}
}

// Run starts the daemon: acquires PID file, listens on the Unix socket,
// serves connections, and blocks until SIGTERM/SIGINT or an upgrade signal.
func Run(cfg *config.Config, paths config.Paths, configFile, adoptFrom string) error {
	if err := paths.EnsureDirs(); err != nil {
		return err
	}

	logFile, err := os.OpenFile(paths.DaemonLog, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open daemon log: %w", err)
	}
	defer logFile.Close()
	log := slog.New(slog.NewJSONHandler(logFile, &slog.HandlerOptions{Level: slog.LevelInfo}))

	sm := NewSessionManager(cfg, paths, log)
	sm.configFile = configFile
	sm.upgradeCh = make(chan struct{}, 1)

	var l net.Listener

	if adoptFrom != "" {
		manifest, err := ReadManifest(adoptFrom)
		if err != nil {
			return fmt.Errorf("read upgrade manifest: %w", err)
		}
		os.Remove(adoptFrom)

		f := os.NewFile(uintptr(manifest.ListenerFd), "listener")
		l, err = net.FileListener(f)
		f.Close()
		if err != nil {
			return fmt.Errorf("adopt listener from fd %d: %w", manifest.ListenerFd, err)
		}

		if err := sm.LoadState(); err != nil {
			log.Warn("failed to load state", "err", err)
		}
		if err := sm.AdoptSessions(manifest); err != nil {
			log.Warn("failed to adopt sessions", "err", err)
		}

		log.Info("daemon upgraded", "adopted_sessions", len(manifest.Sessions), "pid", os.Getpid())
	} else {
		if err := AcquirePIDFile(paths.PIDFile); err != nil {
			return err
		}

		var listenErr error
		l, listenErr = Listen(paths.SocketPath)
		if listenErr != nil {
			ReleasePIDFile(paths.PIDFile)
			return listenErr
		}

		if err := sm.LoadState(); err != nil {
			log.Warn("failed to load state", "err", err)
		}

		log.Info("daemon started", "socket", paths.SocketPath, "pid", os.Getpid())
	}
	defer l.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv := NewServer(l, func(ctx context.Context, conn net.Conn) {
		HandleConnection(ctx, conn, sm, log)
	}, log)

	go srv.Serve(ctx)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	select {
	case <-sigCh:
		log.Info("shutting down")
		cancel()
		sm.StopAll(ctx)
		srv.Shutdown()
		os.Remove(paths.SocketPath)
		ReleasePIDFile(paths.PIDFile)

	case <-sm.upgradeCh:
		log.Info("preparing upgrade")

		unixL, ok := l.(*net.UnixListener)
		if !ok {
			log.Error("listener is not a unix listener, cannot upgrade")
			return fmt.Errorf("upgrade failed: listener type mismatch")
		}
		listenerFile, err := unixL.File()
		if err != nil {
			log.Error("get listener fd", "err", err)
			return fmt.Errorf("upgrade failed: %w", err)
		}
		listenerFd := listenerFile.Fd()

		manifest, err := sm.PrepareUpgrade(listenerFd, configFile)
		if err != nil {
			listenerFile.Close()
			log.Error("prepare upgrade", "err", err)
			return fmt.Errorf("upgrade failed: %w", err)
		}

		manifestPath, err := WriteManifest(paths.RuntimeDir, manifest)
		if err != nil {
			listenerFile.Close()
			log.Error("write manifest", "err", err)
			return fmt.Errorf("upgrade failed: %w", err)
		}

		log.Info("exec-ing new binary", "manifest", manifestPath, "sessions", len(manifest.Sessions))

		if err := ExecUpgrade(manifestPath, configFile); err != nil {
			listenerFile.Close()
			os.Remove(manifestPath)
			log.Error("exec failed", "err", err)
			return fmt.Errorf("upgrade exec failed: %w", err)
		}
	}

	return nil
}
