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

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/detector"
	"github.com/d0ugal/graith/internal/git"
	grpty "github.com/d0ugal/graith/internal/pty"
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
	messages        *MsgStore
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

func (sm *SessionManager) SetMsgStore(ms *MsgStore) {
	sm.messages = ms
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

// Create starts a new agent session, either in a git worktree or as a
// standalone scratch session (when noRepo is true).
func (sm *SessionManager) Create(name, agentName, repoPath, baseBranch, prompt string, noRepo bool, rows, cols uint16) (SessionState, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	agent, ok := sm.cfg.Agents[agentName]
	if !ok {
		return SessionState{}, fmt.Errorf("unknown agent %q", agentName)
	}

	id := generateID()

	var repoRoot, repoName, worktreePath, branchName string

	if noRepo {
		worktreePath = filepath.Join(sm.paths.DataDir, "scratch", id)
		if err := os.MkdirAll(worktreePath, 0o700); err != nil {
			return SessionState{}, fmt.Errorf("create scratch dir: %w", err)
		}
	} else {
		if !git.IsInsideGitRepo(repoPath) {
			return SessionState{}, fmt.Errorf("not inside a git repository: %s (use --no-repo for sessions without a repo)", repoPath)
		}

		var err error
		repoRoot, err = git.RepoRootPath(repoPath)
		if err != nil {
			return SessionState{}, fmt.Errorf("find repo root: %w", err)
		}

		if baseBranch == "" {
			baseBranch, err = git.DiscoverDefaultBranch(repoRoot)
			if err != nil {
				return SessionState{}, err
			}
		}

		repoName = filepath.Base(repoRoot)

		username := sm.cfg.GitHubUsername
		if username == "" {
			username, _ = git.DiscoverGitHubUsername(repoRoot)
		}
		if username == "" {
			username = "user"
		}

		branchPrefix, _ := config.Expand(sm.cfg.BranchPrefix, config.TemplateVars{Username: username})
		branchName = fmt.Sprintf("%s/%s-%s", branchPrefix, name, id)

		worktreePath = filepath.Join(sm.paths.DataDir, "worktrees", repoHash(repoRoot), id)

		if err := git.SetupSession(repoRoot, worktreePath, branchName, baseBranch, sm.cfg.FetchOnCreate); err != nil {
			return SessionState{}, fmt.Errorf("setup git session: %w", err)
		}
	}

	agentSessionID := ""
	if agentName == "claude" {
		b := make([]byte, 16)
		_, _ = rand.Read(b)
		agentSessionID = fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
	}

	username := sm.cfg.GitHubUsername
	if username == "" && repoRoot != "" {
		username, _ = git.DiscoverGitHubUsername(repoRoot)
	}
	if username == "" {
		username = "user"
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
		if noRepo {
			os.RemoveAll(worktreePath)
		} else {
			_ = git.TeardownSession(repoRoot, worktreePath, branchName)
		}
		return SessionState{}, fmt.Errorf("expand agent args: %w", err)
	}
	if prompt != "" {
		expandedArgs = append(expandedArgs, prompt)
	}

	logPath := filepath.Join(sm.paths.LogDir, id+".log")

	env := make(map[string]string, len(agent.Env)+3)
	for k, v := range agent.Env {
		env[k] = v
	}
	env["GRAITH_SESSION_ID"] = id
	env["GRAITH_SESSION_NAME"] = name
	env["GRAITH_WORKTREE_PATH"] = worktreePath

	ptySess, err := grpty.NewSession(grpty.SessionOpts{
		ID:         id,
		Command:    agent.Command,
		Args:       expandedArgs,
		Dir:        worktreePath,
		Env:        env,
		Rows:       rows,
		Cols:       cols,
		LogPath:    logPath,
		MaxLogSize: 100 * 1024 * 1024,
	})
	if err != nil {
		if noRepo {
			os.RemoveAll(worktreePath)
		} else {
			_ = git.TeardownSession(repoRoot, worktreePath, branchName)
		}
		return SessionState{}, fmt.Errorf("start pty session: %w", err)
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

	return *sessState, nil
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
		if err := sm.saveState(); err != nil {
			sm.log.Error("failed to save state after session exit", "id", id, "err", err)
		}
	}

	sm.log.Info("session exited", "id", id, "exit_code", sess.ExitCode())
}

// Resume restarts a stopped session using the agent's resume_args.
func (sm *SessionManager) Resume(id string, rows, cols uint16) (SessionState, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sessState, ok := sm.state.Sessions[id]
	if !ok {
		return SessionState{}, fmt.Errorf("session %q not found", id)
	}
	if sessState.Status == StatusRunning {
		return *sessState, nil
	}

	agent, ok := sm.cfg.Agents[sessState.Agent]
	if !ok {
		return SessionState{}, fmt.Errorf("unknown agent %q", sessState.Agent)
	}

	args := agent.ResumeArgs
	if len(args) == 0 {
		args = agent.Args
	}

	username := sm.cfg.GitHubUsername
	if username == "" {
		username, _ = git.DiscoverGitHubUsername(sessState.RepoPath)
	}
	if username == "" {
		username = "user"
	}

	vars := config.TemplateVars{
		Username:       username,
		AgentSessionID: sessState.AgentSessionID,
		SessionName:    sessState.Name,
		SessionID:      sessState.ID,
		WorktreePath:   sessState.WorktreePath,
	}
	expandedArgs, err := config.ExpandSlice(args, vars)
	if err != nil {
		return SessionState{}, fmt.Errorf("expand resume args: %w", err)
	}

	logPath := filepath.Join(sm.paths.LogDir, id+".log")

	env := make(map[string]string, len(agent.Env)+3)
	for k, v := range agent.Env {
		env[k] = v
	}
	env["GRAITH_SESSION_ID"] = id
	env["GRAITH_SESSION_NAME"] = sessState.Name
	env["GRAITH_WORKTREE_PATH"] = sessState.WorktreePath

	ptySess, err := grpty.NewSession(grpty.SessionOpts{
		ID:         id,
		Command:    agent.Command,
		Args:       expandedArgs,
		Dir:        sessState.WorktreePath,
		Env:        env,
		Rows:       rows,
		Cols:       cols,
		LogPath:    logPath,
		MaxLogSize: 100 * 1024 * 1024,
	})
	if err != nil {
		return SessionState{}, fmt.Errorf("start pty session: %w", err)
	}

	sessState.Status = StatusRunning
	sessState.ExitCode = nil
	sessState.PID = ptySess.Cmd.Process.Pid
	sessState.AgentStatus = ""

	sm.sessions[id] = ptySess
	go sm.watchSession(id, ptySess)

	if err := sm.saveState(); err != nil {
		sm.log.Error("failed to save state", "err", err)
	}

	return *sessState, nil
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

	// Snapshot PTY session and remove from map under the lock so no concurrent
	// access is possible, then release the lock before blocking waits.
	ptySess, hasPTY := sm.sessions[id]
	if hasPTY {
		delete(sm.sessions, id)
	}

	repoPath := sessState.RepoPath
	worktreePath := sessState.WorktreePath
	branch := sessState.Branch

	delete(sm.state.Sessions, id)
	err := sm.saveState()
	sm.mu.Unlock()

	// Blocking operations outside the lock.
	if hasPTY {
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
	}

	if repoPath != "" {
		_ = git.TeardownSession(repoPath, worktreePath, branch)
	} else if worktreePath != "" {
		_ = os.RemoveAll(worktreePath)
	}
	_ = os.Remove(filepath.Join(sm.paths.LogDir, id+".log"))

	if hasClient {
		ac.kick()
	}

	return err
}

// Stop sends SIGTERM to a session's process without removing the session or worktree.
func (sm *SessionManager) Stop(id string) error {
	sm.mu.Lock()
	sessState, ok := sm.state.Sessions[id]
	sm.mu.Unlock()
	if !ok {
		return fmt.Errorf("session %q not found", id)
	}
	if sessState.Status != StatusRunning {
		return fmt.Errorf("session %q is not running", id)
	}

	ptySess, ok := sm.GetPTY(id)
	if !ok {
		return fmt.Errorf("session %q has no PTY", id)
	}

	if err := ptySess.Kill(); err != nil {
		return fmt.Errorf("send SIGTERM: %w", err)
	}
	return nil
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

// List returns copies of all known session states.
func (sm *SessionManager) List() []SessionState {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	list := make([]SessionState, 0, len(sm.state.Sessions))
	for _, s := range sm.state.Sessions {
		list = append(list, *s)
	}
	return list
}

// Get returns a copy of a session state by ID.
func (sm *SessionManager) Get(id string) (SessionState, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	s, ok := sm.state.Sessions[id]
	if !ok {
		return SessionState{}, false
	}
	return *s, ok
}

// GetPTY returns the live PTY session by ID.
func (sm *SessionManager) GetPTY(id string) (*grpty.Session, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	s, ok := sm.sessions[id]
	return s, ok
}

// FindByName looks up a session by exact name, then by prefix match.
func (sm *SessionManager) FindByName(name string) (SessionState, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	for _, s := range sm.state.Sessions {
		if s.Name == name {
			return *s, true
		}
	}

	for _, s := range sm.state.Sessions {
		if len(name) > 0 && len(s.Name) >= len(name) && s.Name[:len(name)] == name {
			return *s, true
		}
	}

	return SessionState{}, false
}

// StopAll gracefully terminates all running sessions.
func (sm *SessionManager) StopAll(ctx context.Context) {
	sm.mu.Lock()
	type snapshot struct {
		id   string
		sess *grpty.Session
	}
	sessions := make([]snapshot, 0, len(sm.sessions))
	for id, sess := range sm.sessions {
		sessions = append(sessions, snapshot{id, sess})
	}
	sm.mu.Unlock()

	for _, s := range sessions {
		if !s.sess.Exited() {
			sm.log.Info("stopping session", "id", s.id)
			_ = s.sess.Kill()
		}
	}

	for _, s := range sessions {
		select {
		case <-s.sess.Done():
		case <-time.After(5 * time.Second):
			sm.log.Warn("force killing session", "id", s.id)
			_ = s.sess.ForceKill()
		}
	}
}

// RunDetectionLoop periodically scans PTY scrollback to detect agent status
// (active, needs approval, ready) for all running sessions.
func (sm *SessionManager) RunDetectionLoop(ctx context.Context) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sm.detectAgentStatuses()
		}
	}
}

func (sm *SessionManager) detectAgentStatuses() {
	sm.mu.RLock()
	type target struct {
		id           string
		agent        string
		pty          *grpty.Session
		worktreePath string
		baseBranch   string
		repoPath     string
	}
	var targets []target
	for id, s := range sm.state.Sessions {
		if s.Status != StatusRunning {
			continue
		}
		if ptySess, ok := sm.sessions[id]; ok {
			targets = append(targets, target{
				id: id, agent: s.Agent, pty: ptySess,
				worktreePath: s.WorktreePath, baseBranch: s.BaseBranch, repoPath: s.RepoPath,
			})
		}
	}
	sm.mu.RUnlock()

	var toAutoStop []string

	for _, t := range targets {
		tail, err := t.pty.Scrollback.Tail(20)
		if err != nil || len(tail) == 0 {
			continue
		}

		d := detector.New(t.agent)
		status := string(d.Detect(string(tail)))

		var dirty bool
		var unpushed int
		if t.worktreePath != "" && t.repoPath != "" {
			if d, err := git.HasUncommittedChanges(t.worktreePath); err == nil {
				dirty = d
			}
			if t.baseBranch != "" {
				if n, err := git.UnpushedCommitCount(t.worktreePath, t.baseBranch); err == nil {
					unpushed = n
				}
			}
		}

		sm.mu.Lock()
		if s, ok := sm.state.Sessions[t.id]; ok {
			s.AgentStatus = status
			s.GitDirty = dirty
			s.GitUnpushed = unpushed

			if sm.checkIdleSession(s) {
				toAutoStop = append(toAutoStop, t.id)
			}
		}
		sm.mu.Unlock()
	}

	for _, id := range toAutoStop {
		sm.mu.RLock()
		s, ok := sm.state.Sessions[id]
		if !ok {
			sm.mu.RUnlock()
			continue
		}
		_, hasClient := sm.attachedClients[id]
		stillIdle := !hasClient && s.AgentStatus == "ready"
		name := s.Name
		idleSince := s.IdleSince
		sm.mu.RUnlock()

		if !stillIdle {
			continue
		}

		var idleDur time.Duration
		if idleSince != nil {
			idleDur = time.Since(*idleSince)
		}
		sm.log.Info("auto-stopping idle session", "session", name, "id", id, "idle_duration", idleDur.Round(time.Second))
		if err := sm.Stop(id); err != nil {
			sm.log.Error("failed to auto-stop session", "id", id, "err", err)
		}
	}
}

// checkIdleSession updates the idle tracking for a session and returns true if it
// should be auto-stopped. Caller must hold sm.mu.
func (sm *SessionManager) checkIdleSession(s *SessionState) bool {
	_, hasClient := sm.attachedClients[s.ID]
	isIdle := !hasClient && s.AgentStatus == "ready"

	if isIdle {
		if s.IdleSince == nil {
			now := time.Now()
			s.IdleSince = &now
		}
	} else {
		s.IdleSince = nil
	}

	if s.IdleSince != nil {
		agentCfg := sm.cfg.Agents[s.Agent]
		timeout := agentCfg.IdleTimeoutDuration()
		if timeout > 0 && time.Since(*s.IdleSince) >= timeout {
			return true
		}
	}
	return false
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
	defer func() { _ = logFile.Close() }()
	log := slog.New(slog.NewJSONHandler(logFile, &slog.HandlerOptions{Level: slog.LevelInfo}))

	sm := NewSessionManager(cfg, paths, log)
	sm.configFile = configFile
	sm.upgradeCh = make(chan struct{}, 1)

	msgStore, err := NewMsgStore(paths.MessagesDB)
	if err != nil {
		return fmt.Errorf("open message store: %w", err)
	}
	defer func() { _ = msgStore.Close() }()
	sm.messages = msgStore

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

	go func() { _ = srv.Serve(ctx) }()
	go sm.RunDetectionLoop(ctx)

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
