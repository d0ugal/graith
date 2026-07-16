//go:build integration

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"github.com/d0ugal/graith/internal/daemon"
	"github.com/d0ugal/graith/internal/protocol"
)

// writeFakeAgent writes an executable shell script that ignores all arguments
// (so it survives both the headless launch flags graith prepends and the PTY
// resume args), emits one stream-json init line, then sleeps — staying alive so
// the session is "running" whether launched headless or as a PTY.
func writeFakeAgent(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "fakeclaude.sh")
	script := "#!/bin/sh\n" +
		"echo '{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"braw\"}'\n" +
		"sleep 600\n"

	if err := os.WriteFile(path, []byte(script), 0o755); err != nil { //nolint:gosec // test fixture must be executable
		t.Fatalf("write fake agent: %v", err)
	}

	return path
}

func headlessEnv(t *testing.T) (*testEnv, string) {
	t.Helper()

	agentPath := writeFakeAgent(t)
	capable := true

	env := setup(t, func(cfg *config.Config) {
		cfg.Headless.Experimental = true
		cfg.Agents["headlessy"] = config.Agent{
			Command:         agentPath,
			HeadlessCapable: &capable,
		}
	})

	return env, agentPath
}

func waitSessionStatus(t *testing.T, env *testEnv, id string, want daemon.SessionStatus) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s, ok := env.sm.Get(id); ok && s.Status == want {
			return
		}

		time.Sleep(20 * time.Millisecond)
	}

	s, _ := env.sm.Get(id)
	t.Fatalf("session %q status = %q, want %q", id, s.Status, want)
}

// TestHeadlessConvertOnAttach drives the full phase-5 flow over the wire: a
// headless session refuses a bare attach with convert_required, an
// attach_convert stops the headless process and relaunches it as an interactive
// PTY, and the driver kind flips to "pty" — after which a normal attach
// succeeds (issue #1137).
func TestHeadlessConvertOnAttach(t *testing.T) {
	env, _ := headlessEnv(t)
	defer env.teardown()

	r, w := env.connect(t)
	handshake(t, r, w)

	sendControl(t, w, "create", protocol.CreateMsg{
		Name: "braw", Agent: "headlessy", RepoPath: env.repo, Base: "main",
		Prompt: "do the thing", Headless: true,
	})

	resp := readControl(t, r)
	if resp.Type != "created" {
		var e protocol.ErrorMsg

		_ = protocol.DecodePayload(resp, &e)
		t.Fatalf("create: got %s (%s), want created", resp.Type, e.Message)
	}

	var info protocol.SessionInfo

	_ = protocol.DecodePayload(resp, &info)
	id := info.ID

	// The session launched headless.
	if s, ok := env.sm.Get(id); !ok || s.DriverKind != daemon.DriverHeadless {
		t.Fatalf("session should be headless at creation, got %+v", s)
	}

	waitSessionStatus(t, env, id, daemon.StatusRunning)

	// A bare attach to a headless session asks the client to confirm conversion.
	sendControl(t, w, "attach", protocol.AttachMsg{SessionID: id})

	cr := readControl(t, r)
	if cr.Type != "convert_required" {
		t.Fatalf("attach to headless: got %s, want convert_required", cr.Type)
	}

	var crMsg protocol.ConvertRequiredMsg

	_ = protocol.DecodePayload(cr, &crMsg)
	if crMsg.Name != "braw" {
		t.Fatalf("convert_required name = %q, want braw", crMsg.Name)
	}

	// Confirming converts: the headless process is stopped and the session is
	// relaunched interactively.
	sendControl(t, w, "attach_convert", protocol.AttachConvertMsg{SessionID: id})

	conv := readControl(t, r)
	if conv.Type != "converted" {
		var e protocol.ErrorMsg

		_ = protocol.DecodePayload(conv, &e)
		t.Fatalf("attach_convert: got %s (%s), want converted", conv.Type, e.Message)
	}

	// The driver flipped to PTY and the session is running again.
	s, ok := env.sm.Get(id)
	if !ok || s.DriverKind != daemon.DriverPTY {
		t.Fatalf("after convert, DriverKind = %q, want pty", s.DriverKind)
	}

	if s.Status != daemon.StatusRunning {
		t.Fatalf("after convert, status = %q, want running", s.Status)
	}

	// GetPTY now returns a live interactive driver, so a normal attach succeeds.
	sendControl(t, w, "attach", protocol.AttachMsg{SessionID: id})

	att := readControl(t, r)
	if att.Type != "attached" {
		var e protocol.ErrorMsg

		_ = protocol.DecodePayload(att, &e)
		t.Fatalf("attach after convert: got %s (%s), want attached", att.Type, e.Message)
	}
}

// TestHeadlessConvertStoppedSession converts a headless session that has already
// exited: no live process to stop, but the driver still flips to PTY and the
// session relaunches interactively.
func TestHeadlessConvertStoppedSession(t *testing.T) {
	env, _ := headlessEnv(t)
	defer env.teardown()

	r, w := env.connect(t)
	handshake(t, r, w)

	sendControl(t, w, "create", protocol.CreateMsg{
		Name: "canny", Agent: "headlessy", RepoPath: env.repo, Base: "main",
		Prompt: "do the thing", Headless: true,
	})

	resp := readControl(t, r)
	if resp.Type != "created" {
		var e protocol.ErrorMsg

		_ = protocol.DecodePayload(resp, &e)
		t.Fatalf("create: got %s (%s), want created", resp.Type, e.Message)
	}

	var info protocol.SessionInfo

	_ = protocol.DecodePayload(resp, &info)
	id := info.ID

	waitSessionStatus(t, env, id, daemon.StatusRunning)

	// Stop the headless session so it's stopped (not running) before convert.
	if err := env.sm.Stop(id); err != nil {
		t.Fatalf("stop: %v", err)
	}

	waitSessionStatus(t, env, id, daemon.StatusStopped)

	sendControl(t, w, "attach_convert", protocol.AttachConvertMsg{SessionID: id})

	conv := readControl(t, r)
	if conv.Type != "converted" {
		var e protocol.ErrorMsg

		_ = protocol.DecodePayload(conv, &e)
		t.Fatalf("attach_convert on stopped: got %s (%s), want converted", conv.Type, e.Message)
	}

	s, ok := env.sm.Get(id)
	if !ok || s.DriverKind != daemon.DriverPTY {
		t.Fatalf("after convert, DriverKind = %q, want pty", s.DriverKind)
	}

	if s.Status != daemon.StatusRunning {
		t.Fatalf("after convert, status = %q, want running", s.Status)
	}
}

// TestHeadlessConvertRealClaude is the design-mandated real-Claude compatibility
// check (issue #1137): the convert-to-interactive path relies on `claude
// --resume` rendering an *interrupted* headless transcript (a possibly-dangling
// tool turn) cleanly at the prompt with history intact — a version-dependent
// behaviour that captured fixtures can't prove. It is opt-in (it spends real
// tokens and needs a logged-in `claude` on PATH): set GRAITH_TEST_REAL_CLAUDE=1
// to run it. It creates a headless Claude session, lets it start a turn,
// converts mid-flight, and asserts the relaunched PTY session comes up running.
func TestHeadlessConvertRealClaude(t *testing.T) {
	if os.Getenv("GRAITH_TEST_REAL_CLAUDE") != "1" {
		t.Skip("set GRAITH_TEST_REAL_CLAUDE=1 to run the real-Claude convert compatibility test")
	}

	claudePath, err := exec.LookPath("claude")
	if err != nil {
		t.Skipf("claude not on PATH: %v", err)
	}

	capable := true

	env := setup(t, func(cfg *config.Config) {
		cfg.Headless.Experimental = true
		def := cfg.Agents["claude"]
		def.Command = claudePath
		def.HeadlessCapable = &capable
		cfg.Agents["claude"] = def
	})
	defer env.teardown()

	r, w := env.connect(t)
	handshake(t, r, w)

	sendControl(t, w, "create", protocol.CreateMsg{
		Name: "bide", Agent: "claude", RepoPath: env.repo, Base: "main",
		Prompt: "List the files in this directory, then wait.", Headless: true,
	})

	resp := readControl(t, r)
	if resp.Type != "created" {
		var e protocol.ErrorMsg

		_ = protocol.DecodePayload(resp, &e)
		t.Fatalf("create: got %s (%s), want created", resp.Type, e.Message)
	}

	var info protocol.SessionInfo

	_ = protocol.DecodePayload(resp, &info)
	id := info.ID

	// Give the headless turn a moment to get going, then convert mid-flight so
	// the transcript has an interrupted (possibly dangling) tool turn.
	time.Sleep(2 * time.Second)

	sendControl(t, w, "attach_convert", protocol.AttachConvertMsg{SessionID: id})

	conv := readControl(t, r)
	if conv.Type != "converted" {
		var e protocol.ErrorMsg

		_ = protocol.DecodePayload(conv, &e)
		t.Fatalf("attach_convert: got %s (%s), want converted", conv.Type, e.Message)
	}

	s, ok := env.sm.Get(id)
	if !ok || s.DriverKind != daemon.DriverPTY || s.Status != daemon.StatusRunning {
		t.Fatalf("after real-Claude convert: %+v (want pty + running)", s)
	}

	// The resumed PTY must render its scrollback (history intact after an
	// interrupted transcript) rather than crash on `--resume`.
	sendControl(t, w, "attach", protocol.AttachMsg{SessionID: id})

	att := readControl(t, r)
	if att.Type != "attached" {
		var e protocol.ErrorMsg

		_ = protocol.DecodePayload(att, &e)
		t.Fatalf("attach after real-Claude convert: got %s (%s), want attached", att.Type, e.Message)
	}
}
