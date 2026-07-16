package headless

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRealClaudeControlProtocol exercises the full headless control-protocol
// path against a REAL `claude` binary: it drives a turn that must ask for a
// Write permission (routed over --permission-prompt-tool stdio as a can_use_tool
// control request), answers it via OnPermission, and asserts the tool ran and
// the session reached a clean terminal result and exited.
//
// It is skipped unless GRAITH_HEADLESS_REAL_CLAUDE=1 because it spawns the real
// CLI (network, auth, cost) — the control protocol is SDK-internal and
// undocumented, so this is the design's pinned real-compatibility check
// (docs/design/2026-07-13-headless-stream-json-design.md), not a CI gate.
func TestRealClaudeControlProtocol(t *testing.T) {
	if os.Getenv("GRAITH_HEADLESS_REAL_CLAUDE") != "1" {
		t.Skip("set GRAITH_HEADLESS_REAL_CLAUDE=1 to run against a real claude binary")
	}

	dir := t.TempDir()
	target := filepath.Join(dir, "made_by_claude.txt")

	gotReq := make(chan PermissionRequest, 4)
	onPerm := func(r PermissionRequest) PermissionDecision {
		gotReq <- r

		return PermissionDecision{Allow: true}
	}

	// headlessArgs equivalent (kept in sync with internal/daemon/driver.go).
	args := []string{
		"-p",
		"--output-format", "stream-json",
		"--input-format", "stream-json",
		"--verbose",
		"--permission-prompt-tool", "stdio",
	}

	s, err := New(Opts{
		ID:           "braw",
		Command:      "claude",
		Args:         args,
		Dir:          dir,
		LogPath:      filepath.Join(dir, "scrollback.log"),
		Control:      true,
		OnPermission: onPerm,
		Prompt:       "Use the Write tool to create the file " + target + " containing the word neep. Do nothing else.",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	t.Cleanup(s.Close)

	select {
	case req := <-gotReq:
		if !strings.EqualFold(req.ToolName, "Write") {
			t.Fatalf("expected a Write permission ask, got %q", req.ToolName)
		}
	case <-time.After(120 * time.Second):
		t.Fatalf("no can_use_tool request arrived; scrollback:\n%s", s.ScreenPreview())
	}

	waitDone(t, s, 120*time.Second)

	snap := s.Snapshot()
	if snap.Degraded {
		t.Fatalf("session degraded; scrollback:\n%s", s.ScreenPreview())
	}

	if snap.Result == nil || snap.Result.IsError {
		t.Fatalf("expected a clean terminal result; got %+v\n%s", snap.Result, s.ScreenPreview())
	}

	if _, err := os.Stat(target); err != nil {
		t.Fatalf("Write tool did not create %s after an allow: %v", target, err)
	}

	if !s.Exited() {
		t.Fatal("headless session should have exited after the terminal result (stdin closed)")
	}
}
