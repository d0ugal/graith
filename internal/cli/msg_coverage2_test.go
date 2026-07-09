package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
