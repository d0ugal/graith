//go:build integration

package integration

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/daemon"
	"github.com/d0ugal/graith/internal/protocol"
)

type testEnv struct {
	sm     *daemon.SessionManager
	srv    *daemon.Server
	cancel context.CancelFunc
	socket string
	tmpDir string
	repo   string
	conns  []net.Conn
}

func setup(t *testing.T) *testEnv {
	t.Helper()
	tmpDir := t.TempDir()

	repo := filepath.Join(tmpDir, "repo")
	os.MkdirAll(repo, 0o755)
	gitRun(t, repo, "init", "-b", "main")
	gitRun(t, repo, "commit", "--allow-empty", "-m", "init")

	// Use /tmp for the socket to stay under macOS 104-byte path limit.
	socketDir, _ := os.MkdirTemp("/tmp", "graith-test-*")
	t.Cleanup(func() { os.RemoveAll(socketDir) })
	socketPath := filepath.Join(socketDir, "gr.sock")
	paths := config.Paths{
		SocketPath: socketPath,
		StateFile:  filepath.Join(tmpDir, "state.json"),
		LogDir:     filepath.Join(tmpDir, "logs"),
		DataDir:    filepath.Join(tmpDir, "data"),
		RuntimeDir: tmpDir,
		MessagesDB: filepath.Join(tmpDir, "messages.db"),
	}
	os.MkdirAll(paths.LogDir, 0o755)
	os.MkdirAll(paths.DataDir, 0o755)

	cfg := config.Default()
	cfg.FetchOnCreate = false
	cfg.Agents["echo"] = config.Agent{
		Command:    "sh",
		Args:       []string{"-c", "echo 'ready'; exec cat"},
		ResumeArgs: []string{"-c", "echo 'resumed'; exec cat"},
	}
	cfg.Agents["sleeper"] = config.Agent{
		Command: "bash",
		Args:    []string{"-c", "trap 'read line; echo got:$line; exit' WINCH; echo ready; sleep 600 & wait"},
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	sm := daemon.NewSessionManager(cfg, paths, log)

	msgStore, err := daemon.NewMsgStore(paths.MessagesDB)
	if err != nil {
		t.Fatalf("open message store: %v", err)
	}
	t.Cleanup(func() { msgStore.Close() })
	sm.SetMsgStore(msgStore)

	docStore, err := daemon.NewDocStore(filepath.Join(tmpDir, "docstore.db"))
	if err != nil {
		t.Fatalf("open doc store: %v", err)
	}
	t.Cleanup(func() { docStore.Close() })
	sm.SetDocStore(docStore)

	l, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	srv := daemon.NewServer(l, func(ctx context.Context, conn net.Conn) {
		daemon.HandleConnection(ctx, conn, sm, log)
	}, log)
	go srv.Serve(ctx)
	go sm.RunDetectionLoop(ctx)

	return &testEnv{sm: sm, srv: srv, cancel: cancel, socket: socketPath, tmpDir: tmpDir, repo: repo}
}

func (e *testEnv) teardown() {
	e.cancel()
	for _, c := range e.conns {
		c.Close()
	}
	e.srv.Shutdown()
}

func (e *testEnv) connect(t *testing.T) (*protocol.FrameReader, *protocol.FrameWriter) {
	t.Helper()
	conn, err := net.Dial("unix", e.socket)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	e.conns = append(e.conns, conn)
	return protocol.NewFrameReader(conn), protocol.NewFrameWriter(conn)
}

func sendControl(t *testing.T, w *protocol.FrameWriter, msgType string, payload any) {
	t.Helper()
	data, err := protocol.EncodeControl(msgType, payload)
	if err != nil {
		t.Fatalf("encode %s: %v", msgType, err)
	}
	if err := w.WriteFrame(protocol.ChannelControl, data); err != nil {
		t.Fatalf("write %s: %v", msgType, err)
	}
}

func readControl(t *testing.T, r *protocol.FrameReader) protocol.Envelope {
	t.Helper()
	for {
		frame, err := r.ReadFrame()
		if err != nil {
			t.Fatalf("read frame: %v", err)
		}
		if frame.Channel == protocol.ChannelControl {
			env, err := protocol.DecodeControl(frame.Payload)
			if err != nil {
				t.Fatalf("decode control: %v", err)
			}
			return env
		}
	}
}

func handshake(t *testing.T, r *protocol.FrameReader, w *protocol.FrameWriter) {
	t.Helper()
	sendControl(t, w, "handshake", protocol.HandshakeMsg{
		Version: "1.0", ClientID: "test", TerminalSize: [2]uint16{80, 24},
	})
	resp := readControl(t, r)
	if resp.Type != "handshake_ok" {
		t.Fatalf("expected handshake_ok, got %s", resp.Type)
	}
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	allArgs := append([]string{"-c", "commit.gpgsign=false"}, args...)
	cmd := exec.Command("git", allArgs...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestCreateAndList(t *testing.T) {
	env := setup(t)
	defer env.teardown()

	r, w := env.connect(t)
	handshake(t, r, w)

	sendControl(t, w, "create", protocol.CreateMsg{
		Name: "test-session", Agent: "echo", RepoPath: env.repo, Base: "main",
	})
	resp := readControl(t, r)
	if resp.Type == "error" {
		var e protocol.ErrorMsg
		protocol.DecodePayload(resp, &e)
		t.Fatalf("create error: %s", e.Message)
	}
	if resp.Type != "created" {
		t.Fatalf("expected created, got %s", resp.Type)
	}

	var info protocol.SessionInfo
	protocol.DecodePayload(resp, &info)
	if info.Name != "test-session" {
		t.Errorf("name = %q, want %q", info.Name, "test-session")
	}
	if info.Agent != "echo" {
		t.Errorf("agent = %q, want %q", info.Agent, "echo")
	}
	if info.Status != "running" {
		t.Errorf("status = %q, want %q", info.Status, "running")
	}

	sendControl(t, w, "list", struct{}{})
	listResp := readControl(t, r)
	var list protocol.SessionListMsg
	protocol.DecodePayload(listResp, &list)
	if len(list.Sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(list.Sessions))
	}
	if list.Sessions[0].Name != "test-session" {
		t.Errorf("list name = %q, want %q", list.Sessions[0].Name, "test-session")
	}
}

func TestCreateNoRepo(t *testing.T) {
	env := setup(t)
	defer env.teardown()

	r, w := env.connect(t)
	handshake(t, r, w)

	sendControl(t, w, "create", protocol.CreateMsg{
		Name: "scratch-session", Agent: "echo", NoRepo: true,
	})
	resp := readControl(t, r)
	if resp.Type == "error" {
		var e protocol.ErrorMsg
		protocol.DecodePayload(resp, &e)
		t.Fatalf("create error: %s", e.Message)
	}

	var info protocol.SessionInfo
	protocol.DecodePayload(resp, &info)
	if info.Name != "scratch-session" {
		t.Errorf("name = %q, want %q", info.Name, "scratch-session")
	}
	if info.RepoName != "" {
		t.Errorf("repo_name = %q, want empty", info.RepoName)
	}
	if info.Branch != "" {
		t.Errorf("branch = %q, want empty", info.Branch)
	}
}

func TestCreateWithoutRepoFails(t *testing.T) {
	env := setup(t)
	defer env.teardown()

	r, w := env.connect(t)
	handshake(t, r, w)

	sendControl(t, w, "create", protocol.CreateMsg{
		Name: "should-fail", Agent: "echo", RepoPath: env.tmpDir,
	})
	resp := readControl(t, r)
	if resp.Type != "error" {
		t.Fatalf("expected error for non-git dir, got %s", resp.Type)
	}
}

func TestStopAndResume(t *testing.T) {
	env := setup(t)
	defer env.teardown()

	r, w := env.connect(t)
	handshake(t, r, w)

	sendControl(t, w, "create", protocol.CreateMsg{
		Name: "resume-test", Agent: "echo", RepoPath: env.repo, Base: "main",
	})
	createResp := readControl(t, r)
	var info protocol.SessionInfo
	protocol.DecodePayload(createResp, &info)
	sessionID := info.ID

	sendControl(t, w, "stop", protocol.StopMsg{SessionID: sessionID})
	stopResp := readControl(t, r)
	if stopResp.Type == "error" {
		var e protocol.ErrorMsg
		protocol.DecodePayload(stopResp, &e)
		t.Fatalf("stop error: %s", e.Message)
	}

	time.Sleep(200 * time.Millisecond)

	sendControl(t, w, "list", struct{}{})
	listResp := readControl(t, r)
	var list protocol.SessionListMsg
	protocol.DecodePayload(listResp, &list)
	for _, s := range list.Sessions {
		if s.ID == sessionID && s.Status != "stopped" {
			t.Errorf("expected stopped, got %s", s.Status)
		}
	}

	sendControl(t, w, "resume", protocol.ResumeMsg{SessionID: sessionID})
	resumeResp := readControl(t, r)
	if resumeResp.Type == "error" {
		var e protocol.ErrorMsg
		protocol.DecodePayload(resumeResp, &e)
		t.Fatalf("resume error: %s", e.Message)
	}

	var resumed protocol.SessionInfo
	protocol.DecodePayload(resumeResp, &resumed)
	if resumed.Status != "running" {
		t.Errorf("resumed status = %q, want %q", resumed.Status, "running")
	}
}

func TestRename(t *testing.T) {
	env := setup(t)
	defer env.teardown()

	r, w := env.connect(t)
	handshake(t, r, w)

	sendControl(t, w, "create", protocol.CreateMsg{
		Name: "old-name", Agent: "echo", RepoPath: env.repo, Base: "main",
	})
	createResp := readControl(t, r)
	var info protocol.SessionInfo
	protocol.DecodePayload(createResp, &info)

	sendControl(t, w, "rename", protocol.RenameMsg{SessionID: info.ID, NewName: "new-name"})
	renameResp := readControl(t, r)
	if renameResp.Type == "error" {
		var e protocol.ErrorMsg
		protocol.DecodePayload(renameResp, &e)
		t.Fatalf("rename error: %s", e.Message)
	}

	sendControl(t, w, "list", struct{}{})
	listResp := readControl(t, r)
	var list protocol.SessionListMsg
	protocol.DecodePayload(listResp, &list)
	found := false
	for _, s := range list.Sessions {
		if s.ID == info.ID {
			found = true
			if s.Name != "new-name" {
				t.Errorf("name = %q, want %q", s.Name, "new-name")
			}
		}
	}
	if !found {
		t.Error("session not found after rename")
	}
}

func TestDelete(t *testing.T) {
	env := setup(t)
	defer env.teardown()

	r, w := env.connect(t)
	handshake(t, r, w)

	sendControl(t, w, "create", protocol.CreateMsg{
		Name: "to-delete", Agent: "echo", RepoPath: env.repo, Base: "main",
	})
	createResp := readControl(t, r)
	var info protocol.SessionInfo
	protocol.DecodePayload(createResp, &info)

	sendControl(t, w, "delete", protocol.DeleteMsg{SessionID: info.ID})
	delResp := readControl(t, r)
	if delResp.Type == "error" {
		var e protocol.ErrorMsg
		protocol.DecodePayload(delResp, &e)
		t.Fatalf("delete error: %s", e.Message)
	}

	sendControl(t, w, "list", struct{}{})
	listResp := readControl(t, r)
	var list protocol.SessionListMsg
	protocol.DecodePayload(listResp, &list)
	if len(list.Sessions) != 0 {
		t.Errorf("expected 0 sessions after delete, got %d", len(list.Sessions))
	}
}

func TestDeleteNoRepoCleansScratchDir(t *testing.T) {
	env := setup(t)
	defer env.teardown()

	r, w := env.connect(t)
	handshake(t, r, w)

	sendControl(t, w, "create", protocol.CreateMsg{
		Name: "scratch-delete", Agent: "echo", NoRepo: true,
	})
	createResp := readControl(t, r)
	var info protocol.SessionInfo
	protocol.DecodePayload(createResp, &info)

	scratchDir := filepath.Join(env.tmpDir, "data", "scratch", info.ID)

	sendControl(t, w, "delete", protocol.DeleteMsg{SessionID: info.ID})
	delResp := readControl(t, r)
	if delResp.Type == "error" {
		var e protocol.ErrorMsg
		protocol.DecodePayload(delResp, &e)
		t.Fatalf("delete error: %s", e.Message)
	}

	if _, err := os.Stat(scratchDir); !os.IsNotExist(err) {
		t.Errorf("scratch dir %s should be removed after delete", scratchDir)
	}
}

func TestMessaging(t *testing.T) {
	env := setup(t)
	defer env.teardown()

	r, w := env.connect(t)
	handshake(t, r, w)

	sendControl(t, w, "msg_pub", protocol.MsgPubMsg{
		Stream: "test-topic", Body: "hello from test", SenderName: "test",
	})
	pubResp := readControl(t, r)
	if pubResp.Type == "error" {
		var e protocol.ErrorMsg
		protocol.DecodePayload(pubResp, &e)
		t.Fatalf("pub error: %s", e.Message)
	}

	sendControl(t, w, "msg_pub", protocol.MsgPubMsg{
		Stream: "test-topic", Body: "second message", SenderName: "test",
	})
	readControl(t, r)

	sendControl(t, w, "msg_sub", protocol.MsgSubMsg{
		Stream: "test-topic",
	})
	msg1 := readControl(t, r)
	if msg1.Type != "msg_message" {
		t.Fatalf("expected msg_message, got %s", msg1.Type)
	}
	msg2 := readControl(t, r)
	if msg2.Type != "msg_message" {
		t.Fatalf("expected msg_message, got %s", msg2.Type)
	}
	done := readControl(t, r)
	if done.Type != "msg_done" {
		t.Fatalf("expected msg_done, got %s", done.Type)
	}

	sendControl(t, w, "msg_topics", protocol.MsgTopicsMsg{})
	topicsResp := readControl(t, r)
	if topicsResp.Type == "error" {
		var e protocol.ErrorMsg
		protocol.DecodePayload(topicsResp, &e)
		t.Fatalf("topics error: %s", e.Message)
	}
}

func TestMultipleSessions(t *testing.T) {
	env := setup(t)
	defer env.teardown()

	r, w := env.connect(t)
	handshake(t, r, w)

	for i := range 3 {
		sendControl(t, w, "create", protocol.CreateMsg{
			Name: fmt.Sprintf("session-%c", rune('a'+i)), Agent: "echo",
			RepoPath: env.repo, Base: "main",
		})
		resp := readControl(t, r)
		if resp.Type == "error" {
			var e protocol.ErrorMsg
			protocol.DecodePayload(resp, &e)
			t.Fatalf("create %d error: %s", i, e.Message)
		}
	}

	sendControl(t, w, "list", struct{}{})
	listResp := readControl(t, r)
	var list protocol.SessionListMsg
	protocol.DecodePayload(listResp, &list)
	if len(list.Sessions) != 3 {
		t.Errorf("expected 3 sessions, got %d", len(list.Sessions))
	}
}

func TestResumeAlreadyRunning(t *testing.T) {
	env := setup(t)
	defer env.teardown()

	r, w := env.connect(t)
	handshake(t, r, w)

	sendControl(t, w, "create", protocol.CreateMsg{
		Name: "running-resume", Agent: "echo", RepoPath: env.repo, Base: "main",
	})
	createResp := readControl(t, r)
	var info protocol.SessionInfo
	protocol.DecodePayload(createResp, &info)

	sendControl(t, w, "resume", protocol.ResumeMsg{SessionID: info.ID})
	resumeResp := readControl(t, r)
	if resumeResp.Type == "error" {
		t.Fatal("resume of running session should succeed (no-op)")
	}
	var resumed protocol.SessionInfo
	protocol.DecodePayload(resumeResp, &resumed)
	if resumed.Status != "running" {
		t.Errorf("status = %q, want running", resumed.Status)
	}
}

func TestDeleteNonexistent(t *testing.T) {
	env := setup(t)
	defer env.teardown()

	r, w := env.connect(t)
	handshake(t, r, w)

	sendControl(t, w, "delete", protocol.DeleteMsg{SessionID: "nonexistent"})
	resp := readControl(t, r)
	if resp.Type != "error" {
		t.Errorf("expected error for nonexistent delete, got %s", resp.Type)
	}
}

func TestStopNonexistent(t *testing.T) {
	env := setup(t)
	defer env.teardown()

	r, w := env.connect(t)
	handshake(t, r, w)

	sendControl(t, w, "stop", protocol.StopMsg{SessionID: "nonexistent"})
	resp := readControl(t, r)
	if resp.Type != "error" {
		t.Errorf("expected error for nonexistent stop, got %s", resp.Type)
	}
}

func TestAttachAndDetach(t *testing.T) {
	env := setup(t)
	defer env.teardown()

	r, w := env.connect(t)
	handshake(t, r, w)

	sendControl(t, w, "create", protocol.CreateMsg{
		Name: "attach-test", Agent: "echo", RepoPath: env.repo, Base: "main",
	})
	createResp := readControl(t, r)
	var info protocol.SessionInfo
	protocol.DecodePayload(createResp, &info)

	sendControl(t, w, "attach", protocol.AttachMsg{SessionID: info.ID})
	attachResp := readControl(t, r)
	if attachResp.Type == "error" {
		var e protocol.ErrorMsg
		protocol.DecodePayload(attachResp, &e)
		t.Fatalf("attach error: %s", e.Message)
	}
	if attachResp.Type != "attached" {
		t.Fatalf("expected attached, got %s", attachResp.Type)
	}

	sendControl(t, w, "detach", struct{}{})
	detachResp := readControl(t, r)
	if detachResp.Type != "detached" {
		t.Fatalf("expected detached, got %s", detachResp.Type)
	}
}

func TestAttachKicksPreviousClient(t *testing.T) {
	env := setup(t)
	defer env.teardown()

	r1, w1 := env.connect(t)
	handshake(t, r1, w1)

	sendControl(t, w1, "create", protocol.CreateMsg{
		Name: "kick-test", Agent: "echo", RepoPath: env.repo, Base: "main",
	})
	createResp := readControl(t, r1)
	var info protocol.SessionInfo
	protocol.DecodePayload(createResp, &info)

	sendControl(t, w1, "attach", protocol.AttachMsg{SessionID: info.ID})
	attachResp := readControl(t, r1)
	if attachResp.Type != "attached" {
		t.Fatalf("expected attached, got %s", attachResp.Type)
	}

	r2, w2 := env.connect(t)
	handshake(t, r2, w2)
	sendControl(t, w2, "attach", protocol.AttachMsg{SessionID: info.ID})

	kickResp := readControl(t, r1)
	if kickResp.Type != "detached" {
		t.Fatalf("expected old client to get detached, got %s", kickResp.Type)
	}
	var d protocol.DetachedMsg
	protocol.DecodePayload(kickResp, &d)
	if d.Reason != "replaced" {
		t.Errorf("detach reason = %q, want %q", d.Reason, "replaced")
	}

	newAttach := readControl(t, r2)
	if newAttach.Type != "attached" {
		t.Fatalf("expected new client attached, got %s", newAttach.Type)
	}
}

func TestTypeDeliversInput(t *testing.T) {
	env := setup(t)
	defer env.teardown()

	r, w := env.connect(t)
	handshake(t, r, w)

	sendControl(t, w, "create", protocol.CreateMsg{
		Name: "type-test", Agent: "echo", RepoPath: env.repo, Base: "main",
	})
	createResp := readControl(t, r)
	if createResp.Type == "error" {
		var e protocol.ErrorMsg
		protocol.DecodePayload(createResp, &e)
		t.Fatalf("create error: %s", e.Message)
	}
	var info protocol.SessionInfo
	protocol.DecodePayload(createResp, &info)

	// Wait for the echo agent ("echo 'ready'; exec cat") to start.
	ptySess, ok := env.sm.GetPTY(info.ID)
	if !ok {
		t.Fatal("PTY session not found")
	}
	ready := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if tail, err := ptySess.Scrollback.TailBytes(4096); err == nil {
			if strings.Contains(string(tail), "ready") {
				ready = true
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !ready {
		t.Fatal("timed out waiting for echo agent to become ready")
	}

	// Send type command — cat should echo the text back.
	sendControl(t, w, "type", protocol.TypeMsg{
		SessionID: info.ID,
		Input:     "hello-from-type-test",
	})
	typeResp := readControl(t, r)
	if typeResp.Type == "error" {
		var e protocol.ErrorMsg
		protocol.DecodePayload(typeResp, &e)
		t.Fatalf("type error: %s", e.Message)
	}
	if typeResp.Type != "typed" {
		t.Fatalf("expected typed, got %s", typeResp.Type)
	}

	// Verify the text appears in the scrollback (echoed by cat).
	found := false
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if tail, err := ptySess.Scrollback.TailBytes(4096); err == nil {
			if strings.Contains(string(tail), "hello-from-type-test") {
				found = true
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !found {
		t.Error("typed text not found in scrollback — input was not delivered to the session")
	}
}

func TestTypeWakesSleepingAgent(t *testing.T) {
	env := setup(t)
	defer env.teardown()

	r, w := env.connect(t)
	handshake(t, r, w)

	// The "sleeper" agent only reads stdin inside a SIGWINCH trap handler.
	// Without the Poke (SIGWINCH), typed input would never be consumed.
	sendControl(t, w, "create", protocol.CreateMsg{
		Name: "wake-test", Agent: "sleeper", NoRepo: true,
	})
	createResp := readControl(t, r)
	if createResp.Type == "error" {
		var e protocol.ErrorMsg
		protocol.DecodePayload(createResp, &e)
		t.Fatalf("create error: %s", e.Message)
	}
	var info protocol.SessionInfo
	protocol.DecodePayload(createResp, &info)

	ptySess, ok := env.sm.GetPTY(info.ID)
	if !ok {
		t.Fatal("PTY session not found")
	}
	ready := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if tail, err := ptySess.Scrollback.TailBytes(4096); err == nil {
			if strings.Contains(string(tail), "ready") {
				ready = true
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !ready {
		t.Fatal("timed out waiting for sleeper agent to become ready")
	}

	sendControl(t, w, "type", protocol.TypeMsg{
		SessionID: info.ID,
		Input:     "wake-test-input",
	})
	typeResp := readControl(t, r)
	if typeResp.Type != "typed" {
		t.Fatalf("expected typed, got %s", typeResp.Type)
	}

	// The sleeper only reads stdin in its SIGWINCH trap. If the Poke
	// didn't send SIGWINCH, the input would never appear in output.
	found := false
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if tail, err := ptySess.Scrollback.TailBytes(4096); err == nil {
			if strings.Contains(string(tail), "got:wake-test-input") {
				found = true
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !found {
		t.Error("sleeper agent did not read input — SIGWINCH poke failed to wake the process")
	}
}

func TestTypeExitedSessionFails(t *testing.T) {
	env := setup(t)
	defer env.teardown()

	r, w := env.connect(t)
	handshake(t, r, w)

	sendControl(t, w, "create", protocol.CreateMsg{
		Name: "type-exited", Agent: "echo", RepoPath: env.repo, Base: "main",
	})
	createResp := readControl(t, r)
	var info protocol.SessionInfo
	protocol.DecodePayload(createResp, &info)

	// Stop the session so the process exits.
	sendControl(t, w, "stop", protocol.StopMsg{SessionID: info.ID})
	stopResp := readControl(t, r)
	if stopResp.Type == "error" {
		var e protocol.ErrorMsg
		protocol.DecodePayload(stopResp, &e)
		t.Fatalf("stop error: %s", e.Message)
	}

	// Wait for the process to actually exit.
	ptySess, ok := env.sm.GetPTY(info.ID)
	if !ok {
		t.Fatal("PTY session not found")
	}
	select {
	case <-ptySess.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for session to exit")
	}

	// Type into the exited session — should get an error.
	sendControl(t, w, "type", protocol.TypeMsg{
		SessionID: info.ID,
		Input:     "should-fail",
	})
	typeResp := readControl(t, r)
	if typeResp.Type != "error" {
		t.Errorf("expected error for type into exited session, got %s", typeResp.Type)
	}
}

func TestUnknownAgent(t *testing.T) {
	env := setup(t)
	defer env.teardown()

	r, w := env.connect(t)
	handshake(t, r, w)

	sendControl(t, w, "create", protocol.CreateMsg{
		Name: "bad-agent", Agent: "nonexistent-agent", RepoPath: env.repo, Base: "main",
	})
	resp := readControl(t, r)
	if resp.Type != "error" {
		t.Fatalf("expected error for unknown agent, got %s", resp.Type)
	}
}

func TestDocStoreRoundTrip(t *testing.T) {
	env := setup(t)
	defer env.teardown()

	r, w := env.connect(t)
	handshake(t, r, w)

	// Put a document.
	sendControl(t, w, "store_put", protocol.StorePutMsg{
		Repo:        "/test/repo",
		Key:         "design/api.md",
		Body:        "# API Design",
		ContentType: "text/markdown",
		AuthorID:    "test",
		AuthorName:  "tester",
	})
	resp := readControl(t, r)
	if resp.Type != "store_ok" {
		t.Fatalf("store_put: expected store_ok, got %s", resp.Type)
	}

	// Get the document back.
	sendControl(t, w, "store_get", protocol.StoreGetMsg{
		Repo: "/test/repo",
		Key:  "design/api.md",
	})
	resp = readControl(t, r)
	if resp.Type != "store_get_response" {
		t.Fatalf("store_get: expected store_get_response, got %s", resp.Type)
	}
	var getResp protocol.StoreGetResponseMsg
	protocol.DecodePayload(resp, &getResp)
	if !getResp.Found {
		t.Fatal("store_get: expected found=true")
	}
	if getResp.Document == nil {
		t.Fatal("store_get: expected non-nil document")
	}
	if getResp.Document.Body != "# API Design" {
		t.Errorf("store_get: body = %q, want %q", getResp.Document.Body, "# API Design")
	}
	if getResp.Document.ContentType != "text/markdown" {
		t.Errorf("store_get: content_type = %q, want %q", getResp.Document.ContentType, "text/markdown")
	}

	// List documents with prefix.
	sendControl(t, w, "store_list", protocol.StoreListMsg{
		Repo:   "/test/repo",
		Prefix: "design/",
	})
	resp = readControl(t, r)
	if resp.Type != "store_list_response" {
		t.Fatalf("store_list: expected store_list_response, got %s", resp.Type)
	}
	var listResp protocol.StoreListResponseMsg
	protocol.DecodePayload(resp, &listResp)
	if len(listResp.Documents) != 1 {
		t.Fatalf("store_list: expected 1 document, got %d", len(listResp.Documents))
	}
	if listResp.Documents[0].Key != "design/api.md" {
		t.Errorf("store_list: key = %q, want %q", listResp.Documents[0].Key, "design/api.md")
	}
	if listResp.Documents[0].Body != "" {
		t.Errorf("store_list: body should be empty (metadata only), got %q", listResp.Documents[0].Body)
	}

	// Delete the document.
	sendControl(t, w, "store_delete", protocol.StoreDeleteMsg{
		Repo: "/test/repo",
		Key:  "design/api.md",
	})
	resp = readControl(t, r)
	if resp.Type != "store_ok" {
		t.Fatalf("store_delete: expected store_ok, got %s", resp.Type)
	}

	// Verify the document is gone.
	sendControl(t, w, "store_get", protocol.StoreGetMsg{
		Repo: "/test/repo",
		Key:  "design/api.md",
	})
	resp = readControl(t, r)
	if resp.Type != "store_get_response" {
		t.Fatalf("store_get (after delete): expected store_get_response, got %s", resp.Type)
	}
	var getResp2 protocol.StoreGetResponseMsg
	protocol.DecodePayload(resp, &getResp2)
	if getResp2.Found {
		t.Fatal("store_get (after delete): expected found=false")
	}
}
