// Package release_test exercises the release publishing helpers that live
// outside the Go source tree (scripts/publish-push.sh) and guards the
// goreleaser workflow against regressing the fix for issue #769: the
// publish-repo job losing a release to a rejected, non-fast-forward push.
package release_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/testutil"
)

// repoRoot returns the graith repository root, derived from this test file's
// location so it works regardless of the caller's working directory.
func repoRoot(t *testing.T) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// internal/release/publish_push_test.go -> repo root is two dirs up.
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func publishPushScript(t *testing.T) string {
	t.Helper()

	path := filepath.Join(repoRoot(t), "scripts", "publish-push.sh")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("publish-push.sh not found: %v", err)
	}

	return path
}

// git runs a git command in dir and fails the test on error.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()

	cmd := testutil.GitCommand(args...)
	cmd.Dir = dir

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s failed: %v\n%s", strings.Join(args, " "), dir, err, out)
	}

	return string(out)
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()

	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// runScript runs publish-push.sh against workDir and returns combined output
// plus the process error (nil on success). Extra args (e.g. a max-attempts
// override) are appended after the commit message.
func runScript(t *testing.T, script, workDir, message string, extra ...string) (string, error) {
	t.Helper()

	args := append([]string{script, workDir, message}, extra...)
	cmd := exec.Command("bash", args...)

	cmd.Env = testutil.GitEnv()
	out, err := cmd.CombinedOutput()

	return string(out), err
}

// newOrigin creates a bare "remote" repo with an initial commit on main and
// returns its path. It also returns a helper checkout used to seed history.
func newOrigin(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	origin := filepath.Join(root, "graith-repo.git")
	git(t, root, "init", "-b", "main", "--bare", origin)

	// Seed the bare repo with one commit via a throwaway checkout.
	seed := filepath.Join(root, "seed")
	git(t, root, "clone", origin, seed)
	writeFile(t, seed, "index.html", "initial\n")
	git(t, seed, "add", "-A")
	git(t, seed, "commit", "-m", "initial")
	git(t, seed, "push", "origin", "main")

	return origin
}

// clone checks out origin into a fresh dir (mirrors the workflow's checkout of
// graith-repo into repo/).
func clone(t *testing.T, origin string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "repo")
	git(t, filepath.Dir(dir), "clone", origin, dir)

	return dir
}

// TestPublishPushFastForward: a clean checkout with a change pushes on the
// first attempt.
func TestPublishPushFastForward(t *testing.T) {
	script := publishPushScript(t)
	origin := newOrigin(t)
	work := clone(t, origin)

	writeFile(t, work, "packages.txt", "graith 1.0.0\n")

	out, err := runScript(t, script, work, "chore: publish braw")
	if err != nil {
		t.Fatalf("script failed: %v\n%s", err, out)
	}

	if !strings.Contains(out, "Pushed on attempt 1.") {
		t.Fatalf("expected first-attempt push, got:\n%s", out)
	}

	log := git(t, work, "log", "--oneline", "origin/main")
	if !strings.Contains(log, "chore: publish braw") {
		t.Fatalf("commit not on origin/main:\n%s", log)
	}
}

// TestPublishPushRebasesOnNonFastForward is the core regression test for
// issue #769: when origin/main advances between checkout and push, the script
// must rebase and retry rather than lose the release.
func TestPublishPushRebasesOnNonFastForward(t *testing.T) {
	script := publishPushScript(t)
	origin := newOrigin(t)
	work := clone(t, origin)

	// A competing writer advances origin/main after `work` was checked out,
	// touching a different file so the rebase is conflict-free.
	other := clone(t, origin)
	writeFile(t, other, "other.txt", "concurrent write\n")
	git(t, other, "add", "-A")
	git(t, other, "commit", "-m", "chore: concurrent thrawn commit")
	git(t, other, "push", "origin", "main")

	// Now `work` is behind: a naive push would be rejected non-fast-forward.
	writeFile(t, work, "packages.txt", "graith 1.0.0\n")

	out, err := runScript(t, script, work, "chore: publish canny release")
	if err != nil {
		t.Fatalf("script failed to recover from non-fast-forward: %v\n%s", err, out)
	}

	if !strings.Contains(out, "rebasing onto origin/main") {
		t.Fatalf("expected a rebase+retry, got:\n%s", out)
	}

	// Both the concurrent commit and our publish must survive on origin/main.
	git(t, work, "fetch", "origin", "main")

	log := git(t, work, "log", "--oneline", "origin/main")
	if !strings.Contains(log, "chore: publish canny release") {
		t.Fatalf("publish commit lost after rebase:\n%s", log)
	}

	if !strings.Contains(log, "chore: concurrent thrawn commit") {
		t.Fatalf("concurrent commit lost after rebase:\n%s", log)
	}
}

// TestPublishPushNoChanges: with nothing staged, the script is a clean no-op
// (no commit, no push, exit 0) — matching the workflow's original behavior.
func TestPublishPushNoChanges(t *testing.T) {
	script := publishPushScript(t)
	origin := newOrigin(t)
	work := clone(t, origin)

	before := git(t, work, "rev-parse", "HEAD")

	out, err := runScript(t, script, work, "chore: publish nothing")
	if err != nil {
		t.Fatalf("script failed on no-op: %v\n%s", err, out)
	}

	if !strings.Contains(out, "No repo changes to publish.") {
		t.Fatalf("expected no-op message, got:\n%s", out)
	}

	after := git(t, work, "rev-parse", "HEAD")
	if before != after {
		t.Fatalf("expected no new commit; HEAD moved %s -> %s", before, after)
	}
}

// TestPublishPushExhaustsAttempts: when the push keeps getting rejected, the
// script fails loudly (exit 1) rather than silently losing the release — the
// fail-closed behavior that keeps issue #769 from recurring quietly. With
// max-attempts=1 it also proves the final attempt does not waste a rebase.
func TestPublishPushExhaustsAttempts(t *testing.T) {
	script := publishPushScript(t)
	origin := newOrigin(t)
	work := clone(t, origin)

	// Advance origin (conflict-free) so `work`'s push is non-fast-forward.
	other := clone(t, origin)
	writeFile(t, other, "other.txt", "concurrent\n")
	git(t, other, "add", "-A")
	git(t, other, "commit", "-m", "chore: dreich concurrent commit")
	git(t, other, "push", "origin", "main")

	writeFile(t, work, "packages.txt", "graith 1.0.0\n")

	out, err := runScript(t, script, work, "chore: publish fash", "1")
	if err == nil {
		t.Fatalf("expected non-zero exit on exhausted attempts, got success:\n%s", out)
	}

	if !strings.Contains(out, "Failed to push after 1 attempts.") {
		t.Fatalf("expected exhaustion failure message, got:\n%s", out)
	}
	// The single (final) attempt must not have attempted a rebase.
	if strings.Contains(out, "rebasing onto origin/main") {
		t.Fatalf("final attempt should not rebase, got:\n%s", out)
	}
}

// TestPublishPushAbortsOnConflictingRebase: if origin/main rewrote the same file
// we did, the rebase can't apply cleanly. The script must abort the rebase and
// fail closed rather than leave the checkout mid-rebase or drop the other change.
func TestPublishPushAbortsOnConflictingRebase(t *testing.T) {
	script := publishPushScript(t)
	origin := newOrigin(t)
	work := clone(t, origin)

	// A competing writer rewrites the SAME file we're about to change.
	other := clone(t, origin)
	writeFile(t, other, "packages.txt", "graith 2.0.0 from other\n")
	git(t, other, "add", "-A")
	git(t, other, "commit", "-m", "chore: thrawn conflicting commit")
	git(t, other, "push", "origin", "main")

	writeFile(t, work, "packages.txt", "graith 1.0.0 from us\n")

	out, err := runScript(t, script, work, "chore: publish scunner")
	if err == nil {
		t.Fatalf("expected non-zero exit on conflicting rebase, got success:\n%s", out)
	}

	if !strings.Contains(out, "conflicting change") {
		t.Fatalf("expected conflict-abort message, got:\n%s", out)
	}
	// The checkout must not be left mid-rebase.
	if _, statErr := os.Stat(filepath.Join(work, ".git", "rebase-merge")); statErr == nil {
		t.Fatal("checkout left in a rebase-in-progress state after abort")
	}
}

// TestPublishPushScriptIsExecutable: the workflow invokes ./scripts/publish-push.sh
// directly, so the committed file must carry the executable bit (the tests
// otherwise run it via `bash`, which would mask a lost exec bit).
func TestPublishPushScriptIsExecutable(t *testing.T) {
	info, err := os.Stat(publishPushScript(t))
	if err != nil {
		t.Fatalf("stat script: %v", err)
	}

	if info.Mode()&0o111 == 0 {
		t.Errorf("scripts/publish-push.sh is not executable (mode %v)", info.Mode())
	}
}

// TestGoreleaserWorkflowHasPublishGuards locks in the workflow-level fixes for
// issue #769: a serializing concurrency guard scoped to the publish-repo job and
// use of the rebase+retry helper for the push.
func TestGoreleaserWorkflowHasPublishGuards(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot(t), ".github", "workflows", "goreleaser.yml"))
	if err != nil {
		t.Fatalf("read goreleaser.yml: %v", err)
	}

	yaml := string(data)

	for _, want := range []string{
		"group: publish-repo",
		"cancel-in-progress: false",
		"scripts/publish-push.sh",
	} {
		if !strings.Contains(yaml, want) {
			t.Errorf("goreleaser.yml missing expected guard: %q", want)
		}
	}

	// The old, guard-less direct push must not linger alongside the helper.
	if strings.Contains(yaml, "git push origin main") {
		t.Error("goreleaser.yml still contains an unguarded `git push origin main`")
	}

	// The concurrency guard must be scoped to the publish-repo job (indented as
	// a job key), not sitting in a comment or on the wrong job. Assert the guard
	// keys appear after the `publish-repo:` job header at job-body indentation.
	if !hasJobScopedConcurrency(yaml) {
		t.Error("concurrency guard is not scoped to the publish-repo job body")
	}
}

// hasJobScopedConcurrency reports whether goreleaser.yml declares a
// `concurrency:` block (with group + cancel-in-progress) inside the
// `publish-repo:` job — i.e. as a real job key, not a comment or top-level key.
func hasJobScopedConcurrency(yaml string) bool {
	lines := strings.Split(yaml, "\n")
	inPublishJob := false
	sawConcurrency, sawGroup, sawCancel := false, false, false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue // ignore comments
		}
		// Job headers sit at two-space indentation under `jobs:`.
		if line == "  publish-repo:" {
			inPublishJob = true
			continue
		}

		if inPublishJob && strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "   ") && strings.HasSuffix(trimmed, ":") {
			// A new two-space job header ends the publish-repo job.
			break
		}

		if !inPublishJob {
			continue
		}

		switch {
		case line == "    concurrency:":
			sawConcurrency = true
		case sawConcurrency && trimmed == "group: publish-repo":
			sawGroup = true
		case sawConcurrency && trimmed == "cancel-in-progress: false":
			sawCancel = true
		}
	}

	return sawConcurrency && sawGroup && sawCancel
}
