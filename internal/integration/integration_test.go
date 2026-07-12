//go:build integration

package integration

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/client"
	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/daemon"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/d0ugal/graith/internal/testutil"
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

func setup(t *testing.T, mutators ...func(*config.Config)) *testEnv {
	t.Helper()
	testutil.IsolateGit(t)
	tmpDir := t.TempDir()

	repo := filepath.Join(tmpDir, "repo")
	_ = os.MkdirAll(repo, 0o750)
	gitRun(t, repo, "init", "-b", "main")
	gitRun(t, repo, "commit", "--allow-empty", "-m", "init")

	// Use /tmp for the socket to stay under macOS 104-byte path limit.
	socketDir, _ := os.MkdirTemp("/tmp", "graith-test-*")

	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })

	socketPath := filepath.Join(socketDir, "gr.sock")
	paths := config.Paths{
		SocketPath: socketPath,
		StateFile:  filepath.Join(tmpDir, "state.json"),
		LogDir:     filepath.Join(tmpDir, "logs"),
		DataDir:    filepath.Join(tmpDir, "data"),
		RuntimeDir: tmpDir,
		MessagesDB: filepath.Join(tmpDir, "messages.db"),

		HumanTokenFile: filepath.Join(tmpDir, "data", "human.token"),
	}
	_ = os.MkdirAll(paths.LogDir, 0o750)
	_ = os.MkdirAll(paths.DataDir, 0o750)

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

	for _, m := range mutators {
		m(cfg)
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	sm := daemon.NewSessionManager(cfg, paths, log)

	// Mirror what daemon.Run does at startup: provision the local human
	// credential and remember it so the harness authenticates as the human
	// (the real CLI reads this token and sends it on every control message).
	if err := sm.EnsureHumanToken(); err != nil {
		t.Fatalf("ensure human token: %v", err)
	}

	tokenBytes, err := os.ReadFile(paths.HumanTokenFile)
	if err != nil {
		t.Fatalf("read human token: %v", err)
	}

	integHumanToken = strings.TrimSpace(string(tokenBytes))

	msgStore, err := daemon.NewMsgStore(paths.MessagesDB)
	if err != nil {
		t.Fatalf("open message store: %v", err)
	}

	t.Cleanup(func() { _ = msgStore.Close() })
	sm.SetMsgStore(msgStore)

	l, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	srv := daemon.NewServer(l, func(ctx context.Context, conn net.Conn) {
		daemon.HandleConnection(ctx, conn, daemon.ConnOrigin{}, sm, log)
	}, log)
	go func() { _ = srv.Serve(ctx) }()
	go sm.RunDetectionLoop(ctx)
	go sm.RunTriggerLoop(ctx)
	go sm.RunFileWatchLoop(ctx)

	return &testEnv{sm: sm, srv: srv, cancel: cancel, socket: socketPath, tmpDir: tmpDir, repo: repo}
}

func (e *testEnv) teardown() {
	e.cancel()

	for _, c := range e.conns {
		_ = c.Close()
	}

	e.srv.Shutdown()
	// Stop all sessions and wait for their exit watchers to finish. Without
	// this, a watcher launched on session exit can still be writing state or
	// publishing a status change when the t.Cleanup hooks close the message
	// store and remove the temp dir, causing "database is closed" errors and
	// "directory not empty" cleanup failures.
	e.sm.StopAll(context.Background())
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

// integHumanToken is the local human credential of the most recently created
// test daemon. The harness runs sequentially (no t.Parallel), one daemon per
// test, so a package-level value is safe and lets the pervasive free-function
// sendControl authenticate as the human without threading a token everywhere.
var integHumanToken string

func sendControl(t *testing.T, w *protocol.FrameWriter, msgType string, payload any) {
	t.Helper()

	data, err := protocol.EncodeControlWithToken(msgType, payload, integHumanToken)
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

	cmd := testutil.GitCommand(args...)
	cmd.Dir = dir

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
		Name: "braw", Agent: "echo", RepoPath: env.repo, Base: "main",
	})

	resp := readControl(t, r)
	if resp.Type == "error" {
		var e protocol.ErrorMsg

		_ = protocol.DecodePayload(resp, &e)
		t.Fatalf("create error: %s", e.Message)
	}

	if resp.Type != "created" {
		t.Fatalf("expected created, got %s", resp.Type)
	}

	var info protocol.SessionInfo

	_ = protocol.DecodePayload(resp, &info)

	if info.Name != "braw" {
		t.Errorf("name = %q, want %q", info.Name, "braw")
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

	_ = protocol.DecodePayload(listResp, &list)

	if len(list.Sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(list.Sessions))
	}

	if list.Sessions[0].Name != "braw" {
		t.Errorf("list name = %q, want %q", list.Sessions[0].Name, "braw")
	}
}

func TestCreateNoRepo(t *testing.T) {
	env := setup(t)
	defer env.teardown()

	r, w := env.connect(t)
	handshake(t, r, w)

	sendControl(t, w, "create", protocol.CreateMsg{
		Name: "kirk", Agent: "echo", NoRepo: true,
	})

	resp := readControl(t, r)
	if resp.Type == "error" {
		var e protocol.ErrorMsg

		_ = protocol.DecodePayload(resp, &e)
		t.Fatalf("create error: %s", e.Message)
	}

	var info protocol.SessionInfo

	_ = protocol.DecodePayload(resp, &info)

	if info.Name != "kirk" {
		t.Errorf("name = %q, want %q", info.Name, "kirk")
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
		Name: "fash", Agent: "echo", RepoPath: env.tmpDir,
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
		Name: "bide", Agent: "echo", RepoPath: env.repo, Base: "main",
	})
	createResp := readControl(t, r)

	var info protocol.SessionInfo

	_ = protocol.DecodePayload(createResp, &info)
	sessionID := info.ID

	sendControl(t, w, "stop", protocol.StopMsg{SessionID: sessionID})

	stopResp := readControl(t, r)
	if stopResp.Type == "error" {
		var e protocol.ErrorMsg

		_ = protocol.DecodePayload(stopResp, &e)
		t.Fatalf("stop error: %s", e.Message)
	}

	time.Sleep(200 * time.Millisecond)

	sendControl(t, w, "list", struct{}{})
	listResp := readControl(t, r)

	var list protocol.SessionListMsg

	_ = protocol.DecodePayload(listResp, &list)

	for _, s := range list.Sessions {
		if s.ID == sessionID && s.Status != "stopped" {
			t.Errorf("expected stopped, got %s", s.Status)
		}
	}

	sendControl(t, w, "resume", protocol.ResumeMsg{SessionID: sessionID})

	resumeResp := readControl(t, r)
	if resumeResp.Type == "error" {
		var e protocol.ErrorMsg

		_ = protocol.DecodePayload(resumeResp, &e)
		t.Fatalf("resume error: %s", e.Message)
	}

	var resumed protocol.SessionInfo

	_ = protocol.DecodePayload(resumeResp, &resumed)

	if resumed.Status != "running" {
		t.Errorf("resumed status = %q, want %q", resumed.Status, "running")
	}
}

func TestResumeInvalidatesPreviousSessionToken(t *testing.T) {
	env := setup(t)
	defer env.teardown()

	r, w := env.connect(t)
	handshake(t, r, w)

	sendControl(t, w, "create", protocol.CreateMsg{
		Name: "bide-token", Agent: "echo", RepoPath: env.repo, Base: "main",
	})
	createResp := readControl(t, r)

	var info protocol.SessionInfo

	_ = protocol.DecodePayload(createResp, &info)

	created, ok := env.sm.Get(info.ID)
	if !ok {
		t.Fatalf("created session %q not found", info.ID)
	}

	oldToken := created.Token

	sendControl(t, w, "stop", protocol.StopMsg{SessionID: info.ID})

	if resp := readControl(t, r); resp.Type == "error" {
		var e protocol.ErrorMsg

		_ = protocol.DecodePayload(resp, &e)
		t.Fatalf("stop error: %s", e.Message)
	}

	sendControl(t, w, "resume", protocol.ResumeMsg{SessionID: info.ID})

	if resp := readControl(t, r); resp.Type == "error" {
		var e protocol.ErrorMsg

		_ = protocol.DecodePayload(resp, &e)
		t.Fatalf("resume error: %s", e.Message)
	}

	resumed, ok := env.sm.Get(info.ID)
	if !ok {
		t.Fatalf("resumed session %q not found", info.ID)
	}

	if resumed.Token == "" || resumed.Token == oldToken {
		t.Fatalf("token was not rotated: old=%q new=%q", oldToken, resumed.Token)
	}

	if got := env.sm.SessionForToken(oldToken); got != "" {
		t.Errorf("pre-resume token resolves to %q, want rejected", got)
	}

	if got := env.sm.SessionForToken(resumed.Token); got != info.ID {
		t.Errorf("new token resolves to %q, want %q", got, info.ID)
	}
}

func TestRename(t *testing.T) {
	env := setup(t)
	defer env.teardown()

	r, w := env.connect(t)
	handshake(t, r, w)

	sendControl(t, w, "create", protocol.CreateMsg{
		Name: "auld", Agent: "echo", RepoPath: env.repo, Base: "main",
	})
	createResp := readControl(t, r)

	var info protocol.SessionInfo

	_ = protocol.DecodePayload(createResp, &info)

	sendControl(t, w, "rename", protocol.RenameMsg{SessionID: info.ID, NewName: "bonnie"})

	renameResp := readControl(t, r)
	if renameResp.Type == "error" {
		var e protocol.ErrorMsg

		_ = protocol.DecodePayload(renameResp, &e)
		t.Fatalf("rename error: %s", e.Message)
	}

	sendControl(t, w, "list", struct{}{})
	listResp := readControl(t, r)

	var list protocol.SessionListMsg

	_ = protocol.DecodePayload(listResp, &list)

	found := false

	for _, s := range list.Sessions {
		if s.ID == info.ID {
			found = true

			if s.Name != "bonnie" {
				t.Errorf("name = %q, want %q", s.Name, "bonnie")
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
		Name: "dreich", Agent: "echo", RepoPath: env.repo, Base: "main",
	})
	createResp := readControl(t, r)

	var info protocol.SessionInfo

	_ = protocol.DecodePayload(createResp, &info)

	sendControl(t, w, "delete", protocol.DeleteMsg{SessionID: info.ID})

	delResp := readControl(t, r)
	if delResp.Type == "error" {
		var e protocol.ErrorMsg

		_ = protocol.DecodePayload(delResp, &e)
		t.Fatalf("delete error: %s", e.Message)
	}

	sendControl(t, w, "list", struct{}{})
	listResp := readControl(t, r)

	var list protocol.SessionListMsg

	_ = protocol.DecodePayload(listResp, &list)

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
		Name: "skelf", Agent: "echo", NoRepo: true,
	})
	createResp := readControl(t, r)

	var info protocol.SessionInfo

	_ = protocol.DecodePayload(createResp, &info)

	scratchDir := filepath.Join(env.tmpDir, "data", "scratch", info.ID)

	// A default (soft) delete preserves the scratch dir for the recovery window;
	// only a purge (hard delete) reclaims it. Purge here to assert teardown.
	sendControl(t, w, "delete", protocol.DeleteMsg{SessionID: info.ID, Purge: true})

	delResp := readControl(t, r)
	if delResp.Type == "error" {
		var e protocol.ErrorMsg

		_ = protocol.DecodePayload(delResp, &e)
		t.Fatalf("delete error: %s", e.Message)
	}

	if _, err := os.Stat(scratchDir); !os.IsNotExist(err) {
		t.Errorf("scratch dir %s should be removed after purge", scratchDir)
	}
}

// TestSoftDeletePreservesAndRestores exercises the recovery window end-to-end
// through a real daemon: delete hides the session but keeps its scratch dir,
// restore brings it back to the live list, and purge then reclaims it.
func TestSoftDeletePreservesAndRestores(t *testing.T) {
	env := setup(t)
	defer env.teardown()

	r, w := env.connect(t)
	handshake(t, r, w)

	sendControl(t, w, "create", protocol.CreateMsg{
		Name: "bide", Agent: "echo", NoRepo: true,
	})

	var info protocol.SessionInfo

	_ = protocol.DecodePayload(readControl(t, r), &info)

	scratchDir := filepath.Join(env.tmpDir, "data", "scratch", info.ID)

	// Soft delete: hidden from the live list, scratch preserved.
	sendControl(t, w, "delete", protocol.DeleteMsg{SessionID: info.ID})

	if resp := readControl(t, r); resp.Type == "error" {
		t.Fatalf("soft delete failed: %v", resp)
	}

	sendControl(t, w, "list", struct{}{})

	var live protocol.SessionListMsg

	_ = protocol.DecodePayload(readControl(t, r), &live)

	if len(live.Sessions) != 0 {
		t.Errorf("soft-deleted session should be hidden from live list, got %d", len(live.Sessions))
	}

	if _, err := os.Stat(scratchDir); err != nil {
		t.Errorf("scratch dir should be preserved after soft delete: %v", err)
	}

	// It shows up in the deleted list.
	sendControl(t, w, "list", protocol.ListMsg{Deleted: true})

	var deleted protocol.SessionListMsg

	_ = protocol.DecodePayload(readControl(t, r), &deleted)

	if len(deleted.Sessions) != 1 || deleted.Sessions[0].ID != info.ID {
		t.Fatalf("expected soft-deleted session in --deleted list, got %+v", deleted.Sessions)
	}

	// Restore: back in the live list.
	sendControl(t, w, "restore", protocol.RestoreMsg{SessionID: info.ID})

	if resp := readControl(t, r); resp.Type == "error" {
		t.Fatalf("restore failed: %v", resp)
	}

	sendControl(t, w, "list", struct{}{})

	var relive protocol.SessionListMsg

	_ = protocol.DecodePayload(readControl(t, r), &relive)

	if len(relive.Sessions) != 1 || relive.Sessions[0].ID != info.ID {
		t.Errorf("restored session should be back in the live list, got %+v", relive.Sessions)
	}

	// Purge: gone for good, scratch reclaimed.
	sendControl(t, w, "delete", protocol.DeleteMsg{SessionID: info.ID, Purge: true})

	if resp := readControl(t, r); resp.Type == "error" {
		t.Fatalf("purge failed: %v", resp)
	}

	if _, err := os.Stat(scratchDir); !os.IsNotExist(err) {
		t.Errorf("scratch dir %s should be removed after purge", scratchDir)
	}
}

func TestMessaging(t *testing.T) {
	env := setup(t)
	defer env.teardown()

	r, w := env.connect(t)
	handshake(t, r, w)

	sendControl(t, w, "msg_pub", protocol.MsgPubMsg{
		Stream: "blether", Body: "hello from test", SenderName: "test",
	})

	pubResp := readControl(t, r)
	if pubResp.Type == "error" {
		var e protocol.ErrorMsg

		_ = protocol.DecodePayload(pubResp, &e)
		t.Fatalf("pub error: %s", e.Message)
	}

	sendControl(t, w, "msg_pub", protocol.MsgPubMsg{
		Stream: "blether", Body: "second message", SenderName: "test",
	})
	readControl(t, r)

	sendControl(t, w, "msg_sub", protocol.MsgSubMsg{
		Stream: "blether",
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

		_ = protocol.DecodePayload(topicsResp, &e)
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
			Name: fmt.Sprintf("croft-%c", rune('a'+i)), Agent: "echo",
			RepoPath: env.repo, Base: "main",
		})

		resp := readControl(t, r)
		if resp.Type == "error" {
			var e protocol.ErrorMsg

			_ = protocol.DecodePayload(resp, &e)
			t.Fatalf("create %d error: %s", i, e.Message)
		}
	}

	sendControl(t, w, "list", struct{}{})
	listResp := readControl(t, r)

	var list protocol.SessionListMsg

	_ = protocol.DecodePayload(listResp, &list)

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
		Name: "canny", Agent: "echo", RepoPath: env.repo, Base: "main",
	})
	createResp := readControl(t, r)

	var info protocol.SessionInfo

	_ = protocol.DecodePayload(createResp, &info)

	sendControl(t, w, "resume", protocol.ResumeMsg{SessionID: info.ID})

	resumeResp := readControl(t, r)
	if resumeResp.Type == "error" {
		t.Fatal("resume of running session should succeed (no-op)")
	}

	var resumed protocol.SessionInfo

	_ = protocol.DecodePayload(resumeResp, &resumed)

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
		Name: "neep", Agent: "echo", RepoPath: env.repo, Base: "main",
	})
	createResp := readControl(t, r)

	var info protocol.SessionInfo

	_ = protocol.DecodePayload(createResp, &info)

	sendControl(t, w, "attach", protocol.AttachMsg{SessionID: info.ID})

	attachResp := readControl(t, r)
	if attachResp.Type == "error" {
		var e protocol.ErrorMsg

		_ = protocol.DecodePayload(attachResp, &e)
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
		Name: "whin", Agent: "echo", RepoPath: env.repo, Base: "main",
	})
	createResp := readControl(t, r1)

	var info protocol.SessionInfo

	_ = protocol.DecodePayload(createResp, &info)

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

	_ = protocol.DecodePayload(kickResp, &d)

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
		Name: "speir", Agent: "echo", RepoPath: env.repo, Base: "main",
	})

	createResp := readControl(t, r)
	if createResp.Type == "error" {
		var e protocol.ErrorMsg

		_ = protocol.DecodePayload(createResp, &e)
		t.Fatalf("create error: %s", e.Message)
	}

	var info protocol.SessionInfo

	_ = protocol.DecodePayload(createResp, &info)

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
		Input:     "hello-from-speir",
	})

	typeResp := readControl(t, r)
	if typeResp.Type == "error" {
		var e protocol.ErrorMsg

		_ = protocol.DecodePayload(typeResp, &e)
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
			if strings.Contains(string(tail), "hello-from-speir") {
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
		Name: "bothy", Agent: "sleeper", NoRepo: true,
	})

	createResp := readControl(t, r)
	if createResp.Type == "error" {
		var e protocol.ErrorMsg

		_ = protocol.DecodePayload(createResp, &e)
		t.Fatalf("create error: %s", e.Message)
	}

	var info protocol.SessionInfo

	_ = protocol.DecodePayload(createResp, &info)

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
		Input:     "bothy-input",
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
			if strings.Contains(string(tail), "got:bothy-input") {
				found = true
				break
			}
		}

		time.Sleep(50 * time.Millisecond)
	}

	if !found {
		t.Error("sleeper agent did not read input — SIGWINCH poke failed to wake the process")
	}

	// Wait for the sleeper process to fully exit before teardown closes the
	// message store, avoiding a race where the exit handler publishes a
	// status change to a closed DB.
	select {
	case <-ptySess.Done():
	case <-time.After(5 * time.Second):
		t.Error("timed out waiting for sleeper to exit")
	}
}

func TestTypeExitedSessionFails(t *testing.T) {
	env := setup(t)
	defer env.teardown()

	r, w := env.connect(t)
	handshake(t, r, w)

	sendControl(t, w, "create", protocol.CreateMsg{
		Name: "haar", Agent: "echo", RepoPath: env.repo, Base: "main",
	})
	createResp := readControl(t, r)

	var info protocol.SessionInfo

	_ = protocol.DecodePayload(createResp, &info)

	// Stop the session so the process exits.
	sendControl(t, w, "stop", protocol.StopMsg{SessionID: info.ID})

	stopResp := readControl(t, r)
	if stopResp.Type == "error" {
		var e protocol.ErrorMsg

		_ = protocol.DecodePayload(stopResp, &e)
		t.Fatalf("stop error: %s", e.Message)
	}

	// Wait for the process to actually exit. The echo agent exits almost
	// immediately, so the watcher may already have removed the PTY from the
	// live session map — that just means it has already exited, so only wait
	// on Done() while the PTY handle is still tracked.
	if ptySess, ok := env.sm.GetPTY(info.ID); ok {
		select {
		case <-ptySess.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for session to exit")
		}
	}

	// Type into the exited session — should get an error.
	sendControl(t, w, "type", protocol.TypeMsg{
		SessionID: info.ID,
		Input:     "fash",
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
		Name: "thrawn", Agent: "nonexistent-agent", RepoPath: env.repo, Base: "main",
	})

	resp := readControl(t, r)
	if resp.Type != "error" {
		t.Fatalf("expected error for unknown agent, got %s", resp.Type)
	}
}

// TestEnsureDaemonReachesFailClosedDaemon is the composed end-to-end regression
// test for PR #1066: it drives the real client.EnsureDaemon against a real
// HandleConnection running behind a Unix listener with a human token provisioned
// — the exact cross-layer interaction the v0.67.1 regression wedged, which the
// per-layer unit tests only exercise in halves.
//
// It validates the combined post-fix behaviour: the probe recognises the live
// fail-closed daemon and returns its connection instead of trying (and, under
// go test, failing) to autostart a doomed second one. It does NOT isolate either
// half of the fix — the two protections are complementary, so with a human token
// present the probe still passes if only one is removed (handshake_ok covers the
// daemon exemption; error-as-alive covers the client side). Each half is locked
// independently by the unit tests (TestHandshakeTokenlessLocalAllowedWithHumanToken
// on the daemon, TestDaemonRespondsTrueOnAuthError on the client); this test
// guards their end-to-end composition.
func TestEnsureDaemonReachesFailClosedDaemon(t *testing.T) {
	env := setup(t)
	defer env.teardown()

	// Isolate from any ambient session token so resolveClientToken exercises the
	// human-token fallback (the human-CLI path).
	t.Setenv("GRAITH_TOKEN", "")

	// A served daemon always has a human token (setup provisions one), so this is
	// the production shape of the regression.
	t.Run("human token present", func(t *testing.T) {
		paths := config.Paths{
			SocketPath:     env.socket,
			HumanTokenFile: filepath.Join(env.tmpDir, "data", "human.token"),
		}

		conn, err := client.EnsureDaemon(paths, "")
		if err != nil {
			t.Fatalf("EnsureDaemon against a live fail-closed daemon: %v", err)
		}

		if conn == nil {
			t.Fatal("EnsureDaemon returned a nil connection for a live daemon")
		}

		_ = conn.Close()
	})

	// A sandboxed agent that can read neither GRAITH_TOKEN nor human.token probes
	// tokenless. Fix A (handshake exempt from the auth gate) means the daemon
	// still answers handshake_ok, so the probe reaches it rather than wedging.
	t.Run("tokenless probe", func(t *testing.T) {
		paths := config.Paths{
			SocketPath:     env.socket,
			HumanTokenFile: filepath.Join(env.tmpDir, "data", "absent.token"),
		}

		conn, err := client.EnsureDaemon(paths, "")
		if err != nil {
			t.Fatalf("tokenless EnsureDaemon against a live fail-closed daemon: %v", err)
		}

		if conn == nil {
			t.Fatal("EnsureDaemon returned a nil connection for a live daemon")
		}

		_ = conn.Close()
	})
}
