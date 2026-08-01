//go:build darwin || linux

package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/tools"
)

func TestContextGitCommandsCancelProcessGroup(t *testing.T) {
	t.Cleanup(tools.Reset)

	originalWaitDelay := contextCommandWaitDelay
	contextCommandWaitDelay = 100 * time.Millisecond

	t.Cleanup(func() { contextCommandWaitDelay = originalWaitDelay })

	tests := map[string]struct {
		run func(context.Context, string) error
	}{
		"plain context command": {
			run: func(ctx context.Context, dir string) error {
				_, _, err := RunContext(ctx, dir, "fetch", "origin")
				return err
			},
		},
		"context command with env": {
			run: func(ctx context.Context, dir string) error {
				_, _, err := RunContextEnv(ctx, dir, []string{"GIT_STUB_BRAW=1"}, "fetch", "origin")
				return err
			},
		},
		"context check command": {
			run: func(ctx context.Context, dir string) error {
				if RunCheckContext(ctx, dir, "fetch", "origin") {
					return nil
				}

				return ctx.Err()
			},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			childPIDPath := filepath.Join(dir, "child.pid")
			alivePath := filepath.Join(dir, "child-alive")
			fakeGit := filepath.Join(dir, "canny-git")

			script := `#!/bin/sh
(
	sleep 30
	printf alive > "$GIT_STUB_ALIVE"
) &
printf '%s\n' "$!" > "$GIT_STUB_CHILD_PID"
wait
`
			if err := os.WriteFile(fakeGit, []byte(script), 0o755); err != nil { //nolint:gosec // G306: stub must be executable for exec
				t.Fatalf("write fake git: %v", err)
			}

			tools.Configure(tools.Config{Git: fakeGit})
			t.Setenv("GIT_STUB_CHILD_PID", childPIDPath)
			t.Setenv("GIT_STUB_ALIVE", alivePath)

			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)

			go func() {
				done <- test.run(ctx, dir)
			}()

			childPID := waitForGitStubChild(t, childPIDPath)

			cancel()

			select {
			case err := <-done:
				if err == nil {
					t.Fatal("git command returned nil after cancellation")
				}
			case <-time.After(500 * time.Millisecond):
				t.Fatal("git command did not return promptly after cancellation")
			}

			waitForProcessExit(t, childPID)

			if _, err := os.Stat(alivePath); err == nil {
				t.Fatal("git descendant survived long enough to write alive marker")
			} else if !os.IsNotExist(err) {
				t.Fatalf("stat child alive marker: %v", err)
			}
		})
	}
}

func TestContextGitCommandSuccessfulWaitDelayIsSuccess(t *testing.T) {
	t.Cleanup(tools.Reset)

	originalWaitDelay := contextCommandWaitDelay
	contextCommandWaitDelay = 100 * time.Millisecond

	t.Cleanup(func() { contextCommandWaitDelay = originalWaitDelay })

	dir := t.TempDir()
	childPIDPath := filepath.Join(dir, "child.pid")
	fakeGit := filepath.Join(dir, "canny-git")

	script := `#!/bin/sh
(
	sleep 30
) &
printf '%s\n' "$!" > "$GIT_STUB_CHILD_PID"
printf 'braw\n'
exit 0
`
	if err := os.WriteFile(fakeGit, []byte(script), 0o755); err != nil { //nolint:gosec // G306: stub must be executable for exec
		t.Fatalf("write fake git: %v", err)
	}

	tools.Configure(tools.Config{Git: fakeGit})
	t.Setenv("GIT_STUB_CHILD_PID", childPIDPath)

	stdout, _, err := RunContext(context.Background(), dir, "status", "--porcelain")
	if err != nil {
		t.Fatalf("successful git command returned error after wait delay: %v", err)
	}

	if stdout != "braw" {
		t.Fatalf("stdout = %q, want braw", stdout)
	}

	childPID := waitForGitStubChild(t, childPIDPath)
	_ = syscall.Kill(childPID, syscall.SIGKILL)
	waitForProcessExit(t, childPID)
}

func TestContextGitCommandTermTrapStillReportsCancellation(t *testing.T) {
	t.Cleanup(tools.Reset)

	dir := t.TempDir()
	startedPath := filepath.Join(dir, "started")
	fakeGit := filepath.Join(dir, "canny-git")

	script := `#!/bin/sh
trap 'exit 0' TERM
printf started > "$GIT_STUB_STARTED"
while :; do
	sleep 1
done
`
	if err := os.WriteFile(fakeGit, []byte(script), 0o755); err != nil { //nolint:gosec // G306: stub must be executable for exec
		t.Fatalf("write fake git: %v", err)
	}

	tools.Configure(tools.Config{Git: fakeGit})
	t.Setenv("GIT_STUB_STARTED", startedPath)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() {
		_, _, err := RunContext(ctx, dir, "status", "--porcelain")
		done <- err
	}()

	waitForFile(t, startedPath)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("git command returned nil after canceled process trapped SIGTERM and exited 0")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("git command did not return promptly after cancellation")
	}
}

func waitForGitStubChild(t *testing.T, childPIDPath string) int {
	t.Helper()

	deadline := time.After(time.Second)

	for {
		if data, err := os.ReadFile(childPIDPath); err == nil {
			pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
			if err != nil {
				t.Fatalf("parse fake git child pid: %v", err)
			}

			return pid
		}

		select {
		case <-deadline:
			t.Fatal("fake git child did not start")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()

	deadline := time.After(time.Second)

	for {
		if _, err := os.Stat(path); err == nil {
			return
		}

		select {
		case <-deadline:
			t.Fatalf("file %s did not appear", path)
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func waitForProcessExit(t *testing.T, pid int) {
	t.Helper()

	deadline := time.After(time.Second)

	for {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}

		if err != nil {
			t.Fatalf("probe fake git child %d: %v", pid, err)
		}

		select {
		case <-deadline:
			t.Fatalf("fake git child %d was still alive after process-group cancellation", pid)
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}
