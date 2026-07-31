package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"

	"github.com/d0ugal/graith/internal/config"
	grpty "github.com/d0ugal/graith/internal/pty"
)

const (
	terminalHistoryRowsForWiringTest = 1234
	logLinesForWiringTest            = 77
)

func terminalHistoryTestConfig() *config.Config {
	cfg := config.Default()
	cfg.FetchOnCreate = false
	cfg.DefaultAgent = "sleeper"
	cfg.Limits.LogLines = logLinesForWiringTest
	cfg.Terminal.HistoryRows = terminalHistoryRowsForWiringTest
	cfg.Agents["sleeper"] = config.Agent{
		NonInteractiveArgs: []string{},
		Command:            "sleep",
		Args:               []string{"60"},
		ResumeArgs:         []string{"60"},
		ForkArgs:           []string{"60"},
	}

	return cfg
}

type terminalHistoryPTYRecorder struct {
	t *testing.T

	mu   sync.Mutex
	rows []int
}

func installTerminalHistoryPTYRecorder(t *testing.T) *terminalHistoryPTYRecorder {
	t.Helper()

	recorder := &terminalHistoryPTYRecorder{t: t}
	orig := newPTYSession
	newPTYSession = func(opts grpty.SessionOpts) (*grpty.Session, error) {
		recorder.mu.Lock()
		recorder.rows = append(recorder.rows, opts.TerminalHistoryRows)
		recorder.mu.Unlock()

		return orig(opts)
	}

	t.Cleanup(func() { newPTYSession = orig })

	return recorder
}

func (r *terminalHistoryPTYRecorder) assertRows(want ...int) {
	r.t.Helper()

	r.mu.Lock()
	got := append([]int(nil), r.rows...)
	r.mu.Unlock()

	if len(got) != len(want) {
		r.t.Fatalf("captured TerminalHistoryRows = %v, want %v", got, want)
	}

	for i := range want {
		if got[i] != want[i] {
			r.t.Fatalf("captured TerminalHistoryRows = %v, want %v", got, want)
		}
	}
}

func assertUsesConfiguredTerminalHistoryRows(t *testing.T, got int) {
	t.Helper()

	if got != terminalHistoryRowsForWiringTest {
		t.Fatalf("TerminalHistoryRows = %d, want terminal.history_rows %d (not limits.log_lines %d)",
			got, terminalHistoryRowsForWiringTest, logLinesForWiringTest)
	}
}

func TestPTYLaunchUsesTerminalHistoryRows(t *testing.T) {
	tests := map[string]struct {
		run func(t *testing.T, sm *SessionManager, recorder *terminalHistoryPTYRecorder)
	}{
		"create": {
			run: func(t *testing.T, sm *SessionManager, recorder *terminalHistoryPTYRecorder) {
				created, err := sm.Create(CreateOpts{Name: "braw", AgentName: "sleeper", NoRepo: true, Rows: 24, Cols: 80})
				if err != nil {
					t.Fatalf("Create() error = %v", err)
				}

				t.Cleanup(func() { stopAndClosePTY(sm, created.ID) })

				recorder.assertRows(terminalHistoryRowsForWiringTest)
			},
		},
		"resume": {
			run: func(t *testing.T, sm *SessionManager, recorder *terminalHistoryPTYRecorder) {
				worktree := t.TempDir()
				sm.state.Sessions["canny"] = &SessionState{
					ID: "canny", Name: "canny", Agent: "sleeper",
					Status: StatusStopped, WorktreePath: worktree, CWD: worktree,
				}

				resumed, err := sm.Resume("canny", 24, 80)
				if err != nil {
					t.Fatalf("Resume() error = %v", err)
				}

				t.Cleanup(func() { stopAndClosePTY(sm, resumed.ID) })

				recorder.assertRows(terminalHistoryRowsForWiringTest)
			},
		},
		"fork": {
			run: func(t *testing.T, sm *SessionManager, recorder *terminalHistoryPTYRecorder) {
				repoDir := initTempGitRepo(t)
				sm.state.Sessions["dreich"] = &SessionState{
					ID: "dreich", Name: "dreich", Agent: "sleeper",
					Status: StatusRunning, RepoPath: repoDir, RepoName: "croft",
					WorktreePath: repoDir, CWD: repoDir, Branch: "main", BaseBranch: "main",
				}

				forked, err := sm.Fork("bothy", "dreich", 24, 80)
				if err != nil {
					t.Fatalf("Fork() error = %v", err)
				}

				t.Cleanup(func() { stopAndClosePTY(sm, forked.ID) })

				recorder.assertRows(terminalHistoryRowsForWiringTest)
			},
		},
		"orchestrator": {
			run: func(t *testing.T, sm *SessionManager, recorder *terminalHistoryPTYRecorder) {
				sm.cfg.Orchestrator.Enabled = true
				sm.cfg.Orchestrator.Agent = "sleeper"
				sm.cfg.Sandbox = config.SandboxConfig{Enabled: true, Backend: "safehouse", Command: "true"}
				sm.sandboxResolver = func(string) (bool, error) { return true, nil }
				sm.paths.SocketPath = filepath.Join(sm.paths.RuntimeDir, "graith.sock")

				created, err := sm.createOrchestrator(context.Background())
				if err != nil {
					t.Fatalf("createOrchestrator() error = %v", err)
				}

				t.Cleanup(func() { stopAndClosePTY(sm, created.ID) })

				recorder.assertRows(terminalHistoryRowsForWiringTest)
			},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			sm := newSMWithConfig(t, terminalHistoryTestConfig())
			recorder := installTerminalHistoryPTYRecorder(t)

			test.run(t, sm, recorder)
		})
	}
}

func TestAdoptSessionsUsesTerminalHistoryRows(t *testing.T) {
	sm := newSMWithConfig(t, terminalHistoryTestConfig())

	cmd := exec.Command("sleep", "30")

	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	reaped := false

	t.Cleanup(func() {
		if !reaped {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			_ = cmd.Wait()
		}
	})

	startTime, err := grpty.ProcessStartTime(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}

	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = writeEnd.Close() })

	fd, err := syscall.Dup(int(readEnd.Fd()))
	_ = readEnd.Close()

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = syscall.Close(fd) })

	logPath := filepath.Join(sm.paths.LogDir, "thrawn.log")
	scrollbackFD := openUpgradeScrollbackFD(t, logPath)
	t.Cleanup(func() { _ = syscall.Close(scrollbackFD) })

	sm.state.Sessions["thrawn"] = &SessionState{
		ID: "thrawn", Name: "thrawn", Agent: "sleeper",
		Status: StatusRunning, PID: cmd.Process.Pid, PIDStartTime: startTime,
	}

	var gotRows int

	sm.adoptSession = func(opts grpty.AdoptOpts) (*grpty.Session, error) {
		gotRows = opts.TerminalHistoryRows

		return newDaemonPTYSessionWithFactory(grpty.SessionOpts{
			ID: opts.ID, Command: "sleep", Args: []string{"60"},
			Dir: sm.paths.DataDir, Rows: opts.DefaultRows, Cols: opts.DefaultCols,
			LogPath: opts.LogPath, MaxLogSize: opts.MaxLogSize,
			Logger: sm.log, TerminalHistoryRows: opts.TerminalHistoryRows,
		})
	}

	result, err := sm.adoptSessions(&UpgradeManifest{Sessions: []UpgradeSession{{
		ID: "thrawn", Fd: fd, HasPTY: true, ScrollbackFd: scrollbackFD,
		PID: cmd.Process.Pid, PIDStartTime: startTime,
	}}}, nil, nil, nil, false)
	if err != nil {
		t.Fatalf("adoptSessions() error = %v", err)
	}

	if len(result.UnresolvedSessions) != 0 || len(result.ResolvedSessions) != 1 {
		t.Fatalf("adoptSessions() result = %+v, want one resolved session", result)
	}

	t.Cleanup(func() { stopAndClosePTY(sm, "thrawn") })

	assertUsesConfiguredTerminalHistoryRows(t, gotRows)

	reaped = true
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_ = cmd.Wait()
}
