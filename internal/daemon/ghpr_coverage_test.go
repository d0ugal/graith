package daemon

import (
	"context"
	"errors"
	"os/exec"
	"testing"
)

func TestGhAvailable_Cov(t *testing.T) {
	_, lookErr := exec.LookPath("gh")
	if got := ghAvailable(); got != (lookErr == nil) {
		t.Errorf("ghAvailable() = %v, but exec.LookPath err = %v", got, lookErr)
	}
}

func TestRepoSlug_Cov(t *testing.T) {
	tmp := t.TempDir()
	repo := tmp + "/croft"

	gitRun(t, "", "init", "--initial-branch=main", repo)

	// No origin remote yet → not resolvable.
	if _, ok := repoSlug(repo); ok {
		t.Error("repoSlug should be false with no origin remote")
	}

	gitRun(t, repo, "remote", "add", "origin", "git@github.com:croft/loch.git")

	slug, ok := repoSlug(repo)
	if !ok || slug != "croft/loch" {
		t.Errorf("repoSlug = (%q,%v), want (croft/loch,true)", slug, ok)
	}
}

func TestEffectiveBranch_Cov(t *testing.T) {
	// Recorded branch wins outright — no git call.
	if got := effectiveBranch("bide", "/nonexistent"); got != "bide" {
		t.Errorf("recorded branch should be returned verbatim, got %q", got)
	}

	// Empty branch + empty worktree → empty.
	if got := effectiveBranch("", ""); got != "" {
		t.Errorf("empty branch + no worktree should be empty, got %q", got)
	}

	tmp := t.TempDir()
	repo := tmp + "/croft"
	gitRun(t, "", "init", "--initial-branch=main", repo)
	gitRun(t, repo, "commit", "--allow-empty", "-m", "initial")

	// Empty recorded branch resolves via symbolic-ref HEAD.
	if got := effectiveBranch("", repo); got != "main" {
		t.Errorf("effectiveBranch should resolve live HEAD to 'main', got %q", got)
	}

	// Detached HEAD → empty (symbolic-ref fails).
	gitRun(t, repo, "checkout", "--detach", "HEAD")

	if got := effectiveBranch("", repo); got != "" {
		t.Errorf("detached HEAD should resolve to empty, got %q", got)
	}
}

func TestResolvePR_ErrorPaths_Cov(t *testing.T) {
	orig := ghRunner
	defer func() { ghRunner = orig }()

	// gh pr list errors → wrapped error.
	ghRunner = func(ctx context.Context, dir string, args ...string) (string, error) {
		return "", errors.New("gh boom")
	}

	if _, _, err := resolvePR(context.Background(), "croft/loch", "bide", ""); err == nil {
		t.Error("expected error when gh pr list fails")
	}

	// Unparseable JSON → parse error.
	ghRunner = func(ctx context.Context, dir string, args ...string) (string, error) {
		return "not json", nil
	}

	if _, _, err := resolvePR(context.Background(), "croft/loch", "bide", ""); err == nil {
		t.Error("expected parse error for malformed pr list JSON")
	}
}

func TestFetchChecks_Degraded_Cov(t *testing.T) {
	orig := ghRunner
	defer func() { ghRunner = orig }()

	// Error with empty output → no state, no failing.
	ghRunner = func(ctx context.Context, dir string, args ...string) (string, error) {
		return "", errors.New("no checks")
	}

	if state, fail := fetchChecks(context.Background(), "croft/loch", 1, ""); state != "" || fail != nil {
		t.Errorf("error+empty output should give ('',nil), got (%q,%v)", state, fail)
	}

	// Malformed JSON → ('',nil).
	ghRunner = func(ctx context.Context, dir string, args ...string) (string, error) {
		return "{bad", nil
	}

	if state, _ := fetchChecks(context.Background(), "croft/loch", 1, ""); state != "" {
		t.Errorf("malformed checks JSON should give '', got %q", state)
	}

	// Empty checks array → ('',nil).
	ghRunner = func(ctx context.Context, dir string, args ...string) (string, error) {
		return "[]", nil
	}

	if state, fail := fetchChecks(context.Background(), "croft/loch", 1, ""); state != "" || fail != nil {
		t.Errorf("empty checks should give ('',nil), got (%q,%v)", state, fail)
	}

	// Non-zero exit but JSON still on stdout (gh pr checks does this when red).
	ghRunner = func(ctx context.Context, dir string, args ...string) (string, error) {
		return `[{"name":"build","bucket":"fail"}]`, errors.New("exit 1")
	}

	if state, fail := fetchChecks(context.Background(), "croft/loch", 1, ""); state != "failing" || len(fail) != 1 {
		t.Errorf("failing checks should still parse despite exit 1, got (%q,%v)", state, fail)
	}
}

func TestFetchComments_EmptyAndBad_Cov(t *testing.T) {
	orig := ghRunner
	defer func() { ghRunner = orig }()

	// Empty output → (nil, true): a real, empty comment set.
	ghRunner = func(ctx context.Context, dir string, args ...string) (string, error) {
		return "", nil
	}

	if comments, ok := fetchComments(context.Background(), "croft/loch", 1, "", "issues"); !ok || comments != nil {
		t.Errorf("empty output should be (nil,true), got (%v,%v)", comments, ok)
	}

	// Unparseable → (nil, false): degraded.
	ghRunner = func(ctx context.Context, dir string, args ...string) (string, error) {
		return "not json", nil
	}

	if comments, ok := fetchComments(context.Background(), "croft/loch", 1, "", "pulls"); ok || comments != nil {
		t.Errorf("bad JSON should be (nil,false), got (%v,%v)", comments, ok)
	}
}
