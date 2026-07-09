package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

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
