package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

func TestDescendantsOfDeepTree(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "ben", Name: "ben"},
		{ID: "bairn", Name: "bairn", ParentID: "ben"},
		{ID: "canny", Name: "canny", ParentID: "ben"},
		{ID: "wee-bairn", Name: "wee-bairn", ParentID: "bairn"},
		{ID: "wee-canny", Name: "wee-canny", ParentID: "bairn"},
		{ID: "wee-skelf", Name: "wee-skelf", ParentID: "wee-bairn"},
		{ID: "thrawn", Name: "thrawn"},
	}

	descendants := descendantsOf(sessions, "ben")

	found := make(map[string]bool)
	for _, d := range descendants {
		found[d.ID] = true
	}

	for _, expected := range []string{"bairn", "canny", "wee-bairn", "wee-canny", "wee-skelf"} {
		if !found[expected] {
			t.Errorf("missing expected descendant %q", expected)
		}
	}

	if found["ben"] {
		t.Error("ben should not be in descendants")
	}

	if found["thrawn"] {
		t.Error("thrawn session should not be in descendants")
	}

	if len(descendants) != 5 {
		t.Errorf("expected 5 descendants, got %d", len(descendants))
	}
}

func TestDescendantsOfNoChildren(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "ben", Name: "ben"},
		{ID: "thrawn", Name: "thrawn", ParentID: "someone-else"},
	}

	descendants := descendantsOf(sessions, "ben")
	if len(descendants) != 0 {
		t.Errorf("expected 0 descendants, got %d", len(descendants))
	}
}

func TestDescendantsOfFlatTree(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "ben", Name: "ben"},
		{ID: "bairn", Name: "bairn", ParentID: "ben"},
		{ID: "canny", Name: "canny", ParentID: "ben"},
	}

	descendants := descendantsOf(sessions, "ben")
	if len(descendants) != 2 {
		t.Errorf("expected 2 descendants, got %d", len(descendants))
	}
}

func TestDescendantsOfCycleBackToRoot(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "ben", Name: "ben"},
		{ID: "bairn", Name: "bairn", ParentID: "ben"},
		{ID: "wee-bairn", Name: "wee-bairn", ParentID: "bairn"},
	}
	// Simulate a cycle: wee-bairn points back to ben via its bairn
	sessions = append(sessions, protocol.SessionInfo{ID: "whin", Name: "whin", ParentID: "wee-bairn"})
	// Add ben as a "child" of whin to create a cycle
	sessions[0].ParentID = "whin"

	descendants := descendantsOf(sessions, "ben")

	found := make(map[string]bool)
	for _, d := range descendants {
		found[d.ID] = true
	}

	if found["ben"] {
		t.Error("ben should not appear in its own descendants (cycle guard)")
	}

	if !found["bairn"] || !found["wee-bairn"] || !found["whin"] {
		t.Error("all reachable non-root descendants should be included")
	}
}

func TestDescendantsOfSelfParent(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "skelf", Name: "skelf", ParentID: "skelf"},
	}

	descendants := descendantsOf(sessions, "skelf")
	if len(descendants) != 0 {
		t.Errorf("self-parented session should not appear in own descendants, got %d", len(descendants))
	}
}

func TestResolveBodyCovFromArgs(t *testing.T) {
	got, err := resolveBody([]string{"blether", "awa"}, "")
	if err != nil {
		t.Fatalf("resolveBody: %v", err)
	}

	if got != "blether awa" {
		t.Errorf("got %q, want %q", got, "blether awa")
	}
}

func TestResolveBodyCovFromFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "body.txt")
	if err := os.WriteFile(f, []byte("frae the bothy"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := resolveBody(nil, f)
	if err != nil {
		t.Fatalf("resolveBody: %v", err)
	}

	if got != "frae the bothy" {
		t.Errorf("got %q, want %q", got, "frae the bothy")
	}
}

func TestResolveBodyCovFileError(t *testing.T) {
	_, err := resolveBody(nil, filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Error("expected error reading missing body file")
	}
}

func TestResolveBodyCovFilePrecedenceOverArgs(t *testing.T) {
	f := filepath.Join(t.TempDir(), "body.txt")
	if err := os.WriteFile(f, []byte("file wins"), 0o600); err != nil {
		t.Fatal(err)
	}

	// When both a file and args are given, the file takes precedence.
	got, err := resolveBody([]string{"ignored"}, f)
	if err != nil {
		t.Fatal(err)
	}

	if got != "file wins" {
		t.Errorf("got %q, want %q", got, "file wins")
	}
}

func TestDetectSenderCovFromEnv(t *testing.T) {
	t.Setenv("GRAITH_SESSION_ID", "braw-id")
	t.Setenv("GRAITH_SESSION_NAME", "braw-name")

	id, name := detectSender()
	if id != "braw-id" {
		t.Errorf("id = %q, want braw-id", id)
	}

	if name != "braw-name" {
		t.Errorf("name = %q, want braw-name", name)
	}
}

func TestDetectSenderCovPIDFallback(t *testing.T) {
	// detectSender reads these via os.Getenv, so an empty value is
	// indistinguishable from unset — clearing them exercises the pid fallback.
	t.Setenv("GRAITH_SESSION_ID", "")
	t.Setenv("GRAITH_SESSION_NAME", "")

	id, name := detectSender()
	if !strings.HasPrefix(id, "pid:") {
		t.Errorf("id = %q, want pid: prefix", id)
	}

	if name != "" {
		t.Errorf("name = %q, want empty", name)
	}
}

func TestMsgPubCovRequiresTopic(t *testing.T) {
	prev := msgPubStream
	prevFile := msgPubFile

	msgPubStream = ""
	msgPubFile = ""

	defer func() { msgPubStream, msgPubFile = prev, prevFile }()

	// A body arg is supplied so resolveBody succeeds; the missing --topic is
	// what must be reported.
	err := msgPubCmd.RunE(msgPubCmd, []string{"blether"})
	if err == nil || !strings.Contains(err.Error(), "--topic is required") {
		t.Errorf("expected --topic required error, got %v", err)
	}
}

func TestMsgSubCovRequiresTopic(t *testing.T) {
	prev := msgSubStream
	msgSubStream = ""

	defer func() { msgSubStream = prev }()

	err := msgSubCmd.RunE(msgSubCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--topic is required") {
		t.Errorf("expected --topic required error, got %v", err)
	}
}

func TestMsgAckCovRequiresTopic(t *testing.T) {
	prev := msgAckStream
	msgAckStream = ""

	defer func() { msgAckStream = prev }()

	err := msgAckCmd.RunE(msgAckCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--topic is required") {
		t.Errorf("expected --topic required error, got %v", err)
	}
}

func TestMsgSendCovArgsValidation(t *testing.T) {
	prevChildren, prevParent := msgSendChildren, msgSendParent

	defer func() { msgSendChildren, msgSendParent = prevChildren, prevParent }()

	t.Run("normal requires 1 or 2 args", func(t *testing.T) {
		msgSendChildren, msgSendParent = false, false

		if err := msgSendCmd.Args(msgSendCmd, nil); err == nil {
			t.Error("expected error with zero args in normal mode")
		}

		if err := msgSendCmd.Args(msgSendCmd, []string{"braw", "hello"}); err != nil {
			t.Errorf("two args should be valid: %v", err)
		}

		if err := msgSendCmd.Args(msgSendCmd, []string{"a", "b", "c"}); err == nil {
			t.Error("expected error with three args in normal mode")
		}
	})

	t.Run("children mode allows at most 1 arg", func(t *testing.T) {
		msgSendChildren, msgSendParent = true, false

		if err := msgSendCmd.Args(msgSendCmd, nil); err != nil {
			t.Errorf("zero args should be valid in --children mode: %v", err)
		}

		if err := msgSendCmd.Args(msgSendCmd, []string{"body only"}); err != nil {
			t.Errorf("one arg should be valid in --children mode: %v", err)
		}

		if err := msgSendCmd.Args(msgSendCmd, []string{"a", "b"}); err == nil {
			t.Error("expected error with two args in --children mode")
		}
	})
}

func TestMsgSendCovValidArgsFunction(t *testing.T) {
	prevChildren, prevParent := msgSendChildren, msgSendParent

	defer func() { msgSendChildren, msgSendParent = prevChildren, prevParent }()

	// In --children / --parent mode, session-name completion is skipped.
	msgSendChildren, msgSendParent = true, false

	names, directive := msgSendCmd.ValidArgsFunction(msgSendCmd, nil, "")
	if names != nil {
		t.Errorf("expected no completions in --children mode, got %v", names)
	}

	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("directive = %v, want NoFileComp", directive)
	}

	// With an argument already present in normal mode, session completion
	// short-circuits (no daemon connection attempted).
	msgSendChildren, msgSendParent = false, false

	names2, _ := msgSendCmd.ValidArgsFunction(msgSendCmd, []string{"braw"}, "")
	if names2 != nil {
		t.Errorf("expected nil completions when arg present, got %v", names2)
	}
}

func TestPrintMessageCovPlain(t *testing.T) {
	payload := mustJSON(t, map[string]any{
		"seq":         int64(3),
		"sender_name": "canny",
		"body":        "aye, ready",
		"created_at":  "2026-07-09T10:00:00Z",
	})

	got := captureMsgStdout(t, func() { printMessage(payload) })

	if !strings.Contains(got, "canny") || !strings.Contains(got, "aye, ready") {
		t.Errorf("printMessage output missing sender/body: %q", got)
	}
}

func TestPrintMessageCovFallsBackToSenderID(t *testing.T) {
	// No sender_name -> falls back to sender_id.
	payload := mustJSON(t, map[string]any{
		"sender_id":  "pid:1234",
		"body":       "frae a pid",
		"created_at": "2026-07-09T10:00:00Z",
	})

	got := captureMsgStdout(t, func() { printMessage(payload) })

	if !strings.Contains(got, "pid:1234") {
		t.Errorf("expected sender_id fallback in %q", got)
	}
}

func TestPrintMessageCovSystemNotification(t *testing.T) {
	payload := mustJSON(t, map[string]any{
		"sender_name": "graithd",
		"body":        "a wee reminder",
		"created_at":  "2026-07-09T10:00:00Z",
		"system":      true,
	})

	got := captureMsgStdout(t, func() { printMessage(payload) })

	if !strings.Contains(got, "automated notification") {
		t.Errorf("system messages must be marked as automated: %q", got)
	}
}

func TestPrintMessageCovLongThreadTruncated(t *testing.T) {
	// A thread ID longer than 12 chars must be truncated to 12 in the display.
	payload := mustJSON(t, map[string]any{
		"body":       "threaded",
		"created_at": "2026-07-09T10:00:00Z",
		"thread_id":  "abcdefghijklmnopqrstuvwxyz",
	})

	got := captureMsgStdout(t, func() { printMessage(payload) })

	if !strings.Contains(got, "thread:abcdefghijkl]") {
		t.Errorf("expected thread id truncated to 12 chars, got %q", got)
	}

	if strings.Contains(got, "abcdefghijklm") {
		t.Errorf("thread id should have been truncated, got %q", got)
	}
}

func TestPrintMessageCovShortThreadNotTruncated(t *testing.T) {
	payload := mustJSON(t, map[string]any{
		"body":       "short thread",
		"created_at": "2026-07-09T10:00:00Z",
		"thread_id":  "abc",
	})

	got := captureMsgStdout(t, func() { printMessage(payload) })

	if !strings.Contains(got, "thread:abc]") {
		t.Errorf("expected full short thread id, got %q", got)
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()

	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}

	return data
}

// captureMsgStdout captures fmt.Printf output (printMessage writes directly to
// os.Stdout).
func captureMsgStdout(t *testing.T, fn func()) string {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	orig := os.Stdout
	os.Stdout = w

	done := make(chan string, 1)

	go func() {
		var sb strings.Builder

		buf := make([]byte, 4096)

		for {
			n, rerr := r.Read(buf)
			if n > 0 {
				sb.Write(buf[:n])
			}

			if rerr != nil {
				break
			}
		}

		done <- sb.String()
	}()

	var result string

	// Restore os.Stdout and drain the reader even if fn() aborts via t.Fatalf
	// (runtime.Goexit) or panic.
	func() {
		defer func() {
			_ = w.Close()
			os.Stdout = orig
			result = <-done
		}()

		fn()
	}()

	return result
}

// withStdinPipe swaps os.Stdin for a pipe pre-filled with content for the
// duration of fn, restoring the real stdin afterwards. resolveBody reads from
// stdin only when it isn't a char device, so a pipe forces that branch
// deterministically rather than inheriting the test runner's stdin.
func withStdinPipe(t *testing.T, content string, fn func()) {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	orig := os.Stdin
	os.Stdin = r

	defer func() { os.Stdin = orig }()

	// Write the body then close the writer so io.ReadAll sees EOF.
	go func() {
		_, _ = w.WriteString(content)
		_ = w.Close()
	}()

	fn()

	_ = r.Close()
}

// TestResolveBodyCov2StdinPipe verifies resolveBody reads the message body from
// stdin when it isn't a terminal and neither a file nor args were given.
func TestResolveBodyCov2StdinPipe(t *testing.T) {
	var (
		got    string
		gotErr error
	)

	withStdinPipe(t, "blether frae the pipe", func() {
		got, gotErr = resolveBody(nil, "")
	})

	if gotErr != nil {
		t.Fatalf("resolveBody: %v", gotErr)
	}

	if got != "blether frae the pipe" {
		t.Errorf("got %q, want %q", got, "blether frae the pipe")
	}
}

// TestResolveBodyCov2CharDeviceErrors verifies that when stdin is a char device
// (a terminal-like /dev/null) and no body was supplied any other way, resolveBody
// reports the "body required" error rather than blocking on a read.
func TestResolveBodyCov2CharDeviceErrors(t *testing.T) {
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer func() { _ = devNull.Close() }()

	orig := os.Stdin
	os.Stdin = devNull

	defer func() { os.Stdin = orig }()

	_, err = resolveBody(nil, "")
	if err == nil || !strings.Contains(err.Error(), "message body required") {
		t.Errorf("expected 'message body required' error, got %v", err)
	}
}

// TestResolveCurrentSessionInfoCov2NoSessionID verifies the early guard: with
// GRAITH_SESSION_ID unset, resolveCurrentSessionInfo errors before it ever
// touches the client, so it can be called with a nil connection.
func TestResolveCurrentSessionInfoCov2NoSessionID(t *testing.T) {
	t.Setenv("GRAITH_SESSION_ID", "")

	_, err := resolveCurrentSessionInfo(nil)
	if err == nil || !strings.Contains(err.Error(), "GRAITH_SESSION_ID is not set") {
		t.Errorf("expected GRAITH_SESSION_ID guard error, got %v", err)
	}
}

// TestResolveBodyCov2FileBeatsStdin verifies a file body is used even when stdin
// also has content — the file branch returns before stdin is ever consulted.
func TestResolveBodyCov2FileBeatsStdin(t *testing.T) {
	f := filepath.Join(t.TempDir(), "body.txt")
	if err := os.WriteFile(f, []byte("frae the file"), 0o600); err != nil {
		t.Fatal(err)
	}

	var got string

	withStdinPipe(t, "frae stdin (should be ignored)", func() {
		var err error

		got, err = resolveBody(nil, f)
		if err != nil {
			t.Fatalf("resolveBody: %v", err)
		}
	})

	if got != "frae the file" {
		t.Errorf("got %q, want the file body", got)
	}
}

// --- gr msg jail helpers ---

func TestBuildJailReleaseMsg(t *testing.T) {
	// A positional id.
	if m, err := buildJailReleaseMsg([]string{"jail_abc"}, false, ""); err != nil || m.ID != "jail_abc" || m.All {
		t.Fatalf("id form: %+v err=%v", m, err)
	}

	// --all --author (with a leading @ trimmed).
	m, err := buildJailReleaseMsg(nil, true, "@scunner")
	if err != nil || !m.All || m.Author != "scunner" {
		t.Fatalf("all form: %+v err=%v", m, err)
	}

	// --all without --author is an error.
	if _, err := buildJailReleaseMsg(nil, true, ""); err == nil {
		t.Fatal("--all without --author should error")
	}

	// Neither id nor --all is an error.
	if _, err := buildJailReleaseMsg(nil, false, ""); err == nil {
		t.Fatal("no id and no --all should error")
	}
}

func TestRenderJailList(t *testing.T) {
	var b strings.Builder

	renderJailList(&b, []protocol.JailedCommentInfo{
		{ID: "jail_1", PRNumber: 5, Author: "scunner", Association: "NONE", Surface: "conversation", TargetName: "wynd", JailedAt: "t0"},
		{ID: "jail_2", PRNumber: 6, Author: "fash", Surface: "inline review", TargetSession: "sid-only", ReleasedAt: "t1"},
	})

	got := b.String()
	for _, want := range []string{"jail_1", "@scunner", "#5", "conversation", "wynd", "jailed", "jail_2", "sid-only", "released"} {
		if !strings.Contains(got, want) {
			t.Errorf("list output missing %q; got:\n%s", want, got)
		}
	}

	// The list must never carry a body column.
	if strings.Contains(got, "BODY") {
		t.Errorf("list should not have a body column, got:\n%s", got)
	}
}

func TestRenderJailShow(t *testing.T) {
	got := renderJailShow(protocol.JailedCommentInfo{
		ID: "jail_1", PRNumber: 5, Branch: "wynd", Author: "scunner", Association: "NONE",
		Surface: "inline review", Path: "main.go", Line: 42, TargetName: "wynd",
		JailedAt: "t0", Body: "the comment body",
	})

	for _, want := range []string{"jail_1", "#5 (wynd)", "@scunner", "main.go:42", "the comment body", "jailed"} {
		if !strings.Contains(got, want) {
			t.Errorf("show output missing %q; got:\n%s", want, got)
		}
	}

	// A released comment shows its release time.
	rel := renderJailShow(protocol.JailedCommentInfo{ID: "jail_2", ReleasedAt: "t9", Body: "x"})
	if !strings.Contains(rel, "released at t9") {
		t.Errorf("released show should note the release time, got:\n%s", rel)
	}
}

func TestRenderJailReleased(t *testing.T) {
	if got := renderJailReleased(nil); !strings.Contains(got, "No jailed comments released") {
		t.Errorf("empty release should say none released, got %q", got)
	}

	got := renderJailReleased([]protocol.JailedCommentInfo{
		{ID: "jail_1", PRNumber: 5, Author: "scunner", TargetName: "wynd"},
		{ID: "jail_2", PRNumber: 5, Author: "scunner", TargetSession: "sid"},
	})
	for _, want := range []string{"Released 2 comment(s)", "jail_1", "@scunner", "wynd", "jail_2", "sid"} {
		if !strings.Contains(got, want) {
			t.Errorf("released output missing %q; got:\n%s", want, got)
		}
	}
}

func TestJailTargetFallback(t *testing.T) {
	if got := jailTarget(protocol.JailedCommentInfo{TargetName: "wynd", TargetSession: "sid"}); got != "wynd" {
		t.Errorf("prefer name, got %q", got)
	}

	if got := jailTarget(protocol.JailedCommentInfo{TargetSession: "sid"}); got != "sid" {
		t.Errorf("fall back to session id, got %q", got)
	}
}
