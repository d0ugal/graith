package mcp

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/daemon"
	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type testEnv struct {
	srv        *Server
	daemonSrv  *daemon.Server
	cancel     context.CancelFunc
	socketPath string
	repo       string
}

func setup(t *testing.T) *testEnv {
	t.Helper()
	tmpDir := t.TempDir()

	repo := filepath.Join(tmpDir, "repo")
	if err := os.MkdirAll(repo, 0o750); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}

	gitRun(t, repo, "init", "-b", "main")
	gitRun(t, repo, "commit", "--allow-empty", "-m", "init")

	socketDir, _ := os.MkdirTemp("/tmp", "graith-mcp-test-*")

	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })

	socketPath := filepath.Join(socketDir, "gr.sock")

	paths := config.Paths{
		SocketPath: socketPath,
		StateFile:  filepath.Join(tmpDir, "state.json"),
		LogDir:     filepath.Join(tmpDir, "logs"),
		DataDir:    filepath.Join(tmpDir, "data"),
		RuntimeDir: tmpDir,
		MessagesDB: filepath.Join(tmpDir, "messages.db"),
	}
	if err := os.MkdirAll(paths.LogDir, 0o750); err != nil {
		t.Fatalf("mkdir log dir: %v", err)
	}

	if err := os.MkdirAll(paths.DataDir, 0o750); err != nil {
		t.Fatalf("mkdir data dir: %v", err)
	}

	cfg := config.Default()
	cfg.FetchOnCreate = false
	cfg.Agents["echo"] = config.Agent{
		Command:    "sh",
		Args:       []string{"-c", "echo 'ready'; exec cat"},
		ResumeArgs: []string{"-c", "echo 'resumed'; exec cat"},
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	sm := daemon.NewSessionManager(cfg, paths, log)

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

	daemonSrv := daemon.NewServer(l, func(ctx context.Context, conn net.Conn) {
		daemon.HandleConnection(ctx, conn, sm, log)
	}, log)
	go func() { _ = daemonSrv.Serve(ctx) }()
	go sm.RunDetectionLoop(ctx)

	mcpSrv := NewServer(cfg, paths, "")

	return &testEnv{
		srv:        mcpSrv,
		daemonSrv:  daemonSrv,
		cancel:     cancel,
		socketPath: socketPath,
		repo:       repo,
	}
}

func (e *testEnv) teardown() {
	e.cancel()
	e.daemonSrv.Shutdown()
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

func TestListSessionsEmpty(t *testing.T) {
	env := setup(t)
	defer env.teardown()

	ctx := context.Background()

	_, out, err := env.srv.listSessions(ctx, &gomcp.CallToolRequest{}, ListSessionsInput{})
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}

	if len(out.Sessions) != 0 {
		t.Errorf("expected 0 sessions, got %d", len(out.Sessions))
	}
}

func TestCreateAndListSessions(t *testing.T) {
	env := setup(t)
	defer env.teardown()

	ctx := context.Background()

	_, created, err := env.srv.createSession(ctx, &gomcp.CallToolRequest{}, CreateSessionInput{
		Name:  "braw-session",
		Agent: "echo",
		Repo:  env.repo,
		Base:  "main",
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	if created.Name != "braw-session" {
		t.Errorf("name = %q, want %q", created.Name, "braw-session")
	}

	if created.Agent != "echo" {
		t.Errorf("agent = %q, want %q", created.Agent, "echo")
	}

	if created.Status != "running" {
		t.Errorf("status = %q, want %q", created.Status, "running")
	}

	if created.ID == "" {
		t.Error("expected non-empty session ID")
	}

	_, list, err := env.srv.listSessions(ctx, &gomcp.CallToolRequest{}, ListSessionsInput{})
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}

	if len(list.Sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(list.Sessions))
	}

	if list.Sessions[0].Name != "braw-session" {
		t.Errorf("listed name = %q, want %q", list.Sessions[0].Name, "braw-session")
	}
}

func TestSessionStatus(t *testing.T) {
	env := setup(t)
	defer env.teardown()

	ctx := context.Background()

	_, created, err := env.srv.createSession(ctx, &gomcp.CallToolRequest{}, CreateSessionInput{
		Name:  "ken-session",
		Agent: "echo",
		Repo:  env.repo,
		Base:  "main",
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	_, status, err := env.srv.sessionStatus(ctx, &gomcp.CallToolRequest{}, SessionStatusInput{
		Session: "ken-session",
	})
	if err != nil {
		t.Fatalf("session status by name: %v", err)
	}

	if status.ID != created.ID {
		t.Errorf("id = %q, want %q", status.ID, created.ID)
	}

	if status.Status != "running" {
		t.Errorf("status = %q, want %q", status.Status, "running")
	}

	_, status, err = env.srv.sessionStatus(ctx, &gomcp.CallToolRequest{}, SessionStatusInput{
		Session: created.ID,
	})
	if err != nil {
		t.Fatalf("session status by ID: %v", err)
	}

	if status.Name != "ken-session" {
		t.Errorf("name = %q, want %q", status.Name, "ken-session")
	}
}

func TestSessionStatusNotFound(t *testing.T) {
	env := setup(t)
	defer env.teardown()

	ctx := context.Background()

	_, _, err := env.srv.sessionStatus(ctx, &gomcp.CallToolRequest{}, SessionStatusInput{
		Session: "thrawn",
	})
	if err == nil {
		t.Fatal("expected error for nonexistent session")
	}
}

func TestPublishAndReadMessages(t *testing.T) {
	env := setup(t)
	defer env.teardown()

	ctx := context.Background()

	_, pub, err := env.srv.publishMessage(ctx, &gomcp.CallToolRequest{}, PublishMessageInput{
		Topic:  "blether",
		Body:   "hello from MCP",
		Sender: "canny-agent",
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}

	if pub.Stream != "blether" {
		t.Errorf("stream = %q, want %q", pub.Stream, "blether")
	}

	if pub.ID == "" {
		t.Error("expected non-empty message ID")
	}

	_, pub2, err := env.srv.publishMessage(ctx, &gomcp.CallToolRequest{}, PublishMessageInput{
		Topic: "blether",
		Body:  "second message",
	})
	if err != nil {
		t.Fatalf("publish second: %v", err)
	}

	if pub2.Seq <= pub.Seq {
		t.Errorf("second seq %d should be > first seq %d", pub2.Seq, pub.Seq)
	}

	_, msgs, err := env.srv.readMessages(ctx, &gomcp.CallToolRequest{}, ReadMessagesInput{
		Topic: "blether",
		All:   true,
	})
	if err != nil {
		t.Fatalf("read messages: %v", err)
	}

	if len(msgs.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs.Messages))
	}

	if msgs.Messages[0].Body != "hello from MCP" {
		t.Errorf("first body = %q, want %q", msgs.Messages[0].Body, "hello from MCP")
	}

	if msgs.Messages[0].SenderName != "canny-agent" {
		t.Errorf("sender = %q, want %q", msgs.Messages[0].SenderName, "canny-agent")
	}

	if msgs.Messages[1].Body != "second message" {
		t.Errorf("second body = %q, want %q", msgs.Messages[1].Body, "second message")
	}
}

func TestReadMessagesEmpty(t *testing.T) {
	env := setup(t)
	defer env.teardown()

	ctx := context.Background()

	_, msgs, err := env.srv.readMessages(ctx, &gomcp.CallToolRequest{}, ReadMessagesInput{
		Topic: "neep",
		All:   true,
	})
	if err != nil {
		t.Fatalf("read messages: %v", err)
	}

	if len(msgs.Messages) != 0 {
		t.Errorf("expected 0 messages, got %d", len(msgs.Messages))
	}
}

func TestReadMessagesRespectsContextCancellation(t *testing.T) {
	env := setup(t)
	defer env.teardown()

	// Publish a message so there is something in the stream to read.
	_, _, err := env.srv.publishMessage(context.Background(), &gomcp.CallToolRequest{}, PublishMessageInput{
		Topic: "dreich",
		Body:  "you shall not read me",
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}

	// A cancelled context must make readMessages bail out with the context
	// error instead of blocking in its read loop forever.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err = env.srv.readMessages(ctx, &gomcp.CallToolRequest{}, ReadMessagesInput{
		Topic: "dreich",
		All:   true,
	})
	if err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	}

	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestSubscribeReceivesMessage(t *testing.T) {
	env := setup(t)
	defer env.teardown()

	ctx := context.Background()

	errCh := make(chan error, 1)
	outCh := make(chan SubscribeOutput, 1)

	go func() {
		_, out, err := env.srv.subscribe(ctx, &gomcp.CallToolRequest{}, SubscribeInput{
			Topic:      "kirk",
			Subscriber: "test-sub",
		})
		if err != nil {
			errCh <- err
			return
		}

		outCh <- out
	}()

	_, _, err := env.srv.publishMessage(ctx, &gomcp.CallToolRequest{}, PublishMessageInput{
		Topic: "kirk",
		Body:  "subscribed message",
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}

	select {
	case err := <-errCh:
		t.Fatalf("subscribe error: %v", err)
	case out := <-outCh:
		if out.Message.Body != "subscribed message" {
			t.Errorf("body = %q, want %q", out.Message.Body, "subscribed message")
		}
	}
}

func TestCreateSessionDefaultAgent(t *testing.T) {
	env := setup(t)
	defer env.teardown()

	env.srv.cfg.DefaultAgent = "echo"

	ctx := context.Background()

	_, created, err := env.srv.createSession(ctx, &gomcp.CallToolRequest{}, CreateSessionInput{
		Name: "bonnie",
		Repo: env.repo,
		Base: "main",
	})
	if err != nil {
		t.Fatalf("create session without agent: %v", err)
	}

	if created.Agent != "echo" {
		t.Errorf("agent = %q, want %q (config default)", created.Agent, "echo")
	}
}

func TestCreateSessionNoRepo(t *testing.T) {
	env := setup(t)
	defer env.teardown()

	ctx := context.Background()

	_, created, err := env.srv.createSession(ctx, &gomcp.CallToolRequest{}, CreateSessionInput{
		Name:   "croft-session",
		Agent:  "echo",
		NoRepo: true,
	})
	if err != nil {
		t.Fatalf("create no-repo session: %v", err)
	}

	if created.Name != "croft-session" {
		t.Errorf("name = %q, want %q", created.Name, "croft-session")
	}

	if created.Branch != "" {
		t.Errorf("branch = %q, want empty", created.Branch)
	}
}

func TestPublishMessageWithThread(t *testing.T) {
	env := setup(t)
	defer env.teardown()

	ctx := context.Background()

	_, pub, err := env.srv.publishMessage(ctx, &gomcp.CallToolRequest{}, PublishMessageInput{
		Topic:    "kirk-blether",
		Body:     "thread starter",
		ThreadID: "thread-1",
		ReplyTo:  "reply-topic",
	})
	if err != nil {
		t.Fatalf("publish threaded: %v", err)
	}

	if pub.ID == "" {
		t.Error("expected non-empty message ID")
	}

	_, msgs, err := env.srv.readMessages(ctx, &gomcp.CallToolRequest{}, ReadMessagesInput{
		Topic:    "kirk-blether",
		All:      true,
		ThreadID: "thread-1",
	})
	if err != nil {
		t.Fatalf("read threaded: %v", err)
	}

	if len(msgs.Messages) != 1 {
		t.Fatalf("expected 1 threaded message, got %d", len(msgs.Messages))
	}

	if msgs.Messages[0].ThreadID != "thread-1" {
		t.Errorf("thread_id = %q, want %q", msgs.Messages[0].ThreadID, "thread-1")
	}
}
