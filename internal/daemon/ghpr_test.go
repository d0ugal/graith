package daemon

import (
	"context"
	"errors"
	"os/exec"
	"testing"
)

func TestParseGitHubRemote(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want string
		ok   bool
	}{
		{"ssh croft", "git@github.com:croft/loch.git", "croft/loch", true},
		{"https croft", "https://github.com/croft/loch.git", "croft/loch", true},
		{"https no suffix", "https://github.com/croft/loch", "croft/loch", true},
		{"ssh no suffix", "git@github.com:croft/loch", "croft/loch", true},
		{"non-github glen", "git@gitlab.com:croft/glen.git", "", false},
		{"empty haar", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := parseGitHubRemote(c.url)
			if ok != c.ok || got != c.want {
				t.Errorf("parseGitHubRemote(%q) = (%q,%v), want (%q,%v)", c.url, got, ok, c.want, c.ok)
			}
		})
	}
}

func TestNormalizePRState(t *testing.T) {
	cases := []struct {
		state   string
		isDraft bool
		want    string
	}{
		{"OPEN", false, "open"},
		{"OPEN", true, "draft"},
		{"MERGED", false, "merged"},
		{"CLOSED", false, "closed"},
		{"open", true, "draft"},
	}
	for _, c := range cases {
		if got := normalizePRState(c.state, c.isDraft); got != c.want {
			t.Errorf("normalizePRState(%q,%v) = %q, want %q", c.state, c.isDraft, got, c.want)
		}
	}
}

func TestCIBucket(t *testing.T) {
	// NEUTRAL/SKIPPED/CANCELLED must be pass-like — they must never wake an agent.
	cases := []struct {
		name  string
		check prCheck
		want  string
	}{
		{"bucket fail", prCheck{Bucket: "fail"}, "fail"},
		{"bucket pending", prCheck{Bucket: "pending"}, "pending"},
		{"bucket skipping is pass", prCheck{Bucket: "skipping"}, "pass"},
		{"bucket cancel is pass", prCheck{Bucket: "cancel"}, "pass"},
		{"state FAILURE", prCheck{State: "FAILURE"}, "fail"},
		{"state TIMED_OUT", prCheck{State: "TIMED_OUT"}, "fail"},
		{"state NEUTRAL is pass", prCheck{State: "NEUTRAL"}, "pass"},
		{"state SKIPPED is pass", prCheck{State: "SKIPPED"}, "pass"},
		{"state IN_PROGRESS", prCheck{State: "IN_PROGRESS"}, "pending"},
		{"state SUCCESS", prCheck{State: "SUCCESS"}, "pass"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ciBucket(c.check); got != c.want {
				t.Errorf("ciBucket(%+v) = %q, want %q", c.check, got, c.want)
			}
		})
	}
}

func TestFetchChecksAggregate(t *testing.T) {
	orig := ghRunner
	defer func() { ghRunner = orig }()

	cases := []struct {
		name        string
		json        string
		wantState   string
		wantFail    []string
		wantPending int
		wantPassed  int
		wantTotal   int
	}{
		{
			name:       "braw all pass",
			json:       `[{"name":"build","bucket":"pass"},{"name":"lint","bucket":"pass"}]`,
			wantState:  "passing",
			wantPassed: 2,
			wantTotal:  2,
		},
		{
			name:       "thrawn one fail",
			json:       `[{"name":"build","bucket":"pass"},{"name":"lint","bucket":"fail"}]`,
			wantState:  "failing",
			wantFail:   []string{"lint"},
			wantPassed: 1,
			wantTotal:  2,
		},
		{
			name:        "thrawn fail while others still running",
			json:        `[{"name":"build","bucket":"pending"},{"name":"lint","bucket":"fail"},{"name":"vet","bucket":"pending"}]`,
			wantState:   "failing",
			wantFail:    []string{"lint"},
			wantPending: 2,
			wantPassed:  0,
			wantTotal:   3,
		},
		{
			name:       "neep skipped is not a failure",
			json:       `[{"name":"build","bucket":"pass"},{"name":"deploy","bucket":"skipping"}]`,
			wantState:  "passing",
			wantPassed: 2,
			wantTotal:  2,
		},
		{
			name:        "haar pending",
			json:        `[{"name":"build","bucket":"pending"},{"name":"lint","bucket":"pass"}]`,
			wantState:   "pending",
			wantPending: 1,
			wantPassed:  1,
			wantTotal:   2,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ghRunner = func(ctx context.Context, ghBin, dir string, args ...string) (string, error) {
				return c.json, nil
			}

			state, fail, pending, passed, total := fetchChecks(context.Background(), "gh", "croft/loch", 1, "", 0)
			if state != c.wantState {
				t.Errorf("state = %q, want %q", state, c.wantState)
			}

			if len(fail) != len(c.wantFail) {
				t.Errorf("failing = %v, want %v", fail, c.wantFail)
			}

			if pending != c.wantPending {
				t.Errorf("pending = %d, want %d", pending, c.wantPending)
			}

			if passed != c.wantPassed {
				t.Errorf("passed = %d, want %d", passed, c.wantPassed)
			}

			if total != c.wantTotal {
				t.Errorf("total = %d, want %d", total, c.wantTotal)
			}
		})
	}
}

func TestFetchCommentsSlurpFlattensPages(t *testing.T) {
	orig := ghRunner
	defer func() { ghRunner = orig }()
	// gh api --paginate --slurp wraps each page in an outer array.
	ghRunner = func(ctx context.Context, ghBin, dir string, args ...string) (string, error) {
		return `[[{"id":1,"user":{"login":"ailsa"},"body":"a"}],[{"id":2,"user":{"login":"hamish"},"body":"b"}]]`, nil
	}

	comments, ok := fetchComments(context.Background(), "gh", "croft/loch", 1, "", "issues", 0)
	if !ok {
		t.Fatal("expected ok=true")
	}

	if len(comments) != 2 || comments[0].ID != 1 || comments[1].ID != 2 {
		t.Errorf("expected 2 flattened comments, got %+v", comments)
	}
}

// TestFetchCommentsDecodesAuthorAssociation guards the single field the whole
// author-trust gate depends on (issue #1039): if the `json:"author_association"`
// struct tag ever drifts, the field decodes empty, every author is treated as
// untrusted, and all comment delivery silently breaks. This proves the field
// survives a realistic gh API payload.
func TestFetchCommentsDecodesAuthorAssociation(t *testing.T) {
	orig := ghRunner
	defer func() { ghRunner = orig }()

	ghRunner = func(ctx context.Context, ghBin, dir string, args ...string) (string, error) {
		// One --slurp page with the association field present, as the GitHub REST
		// API returns it.
		return `[[
			{"id":1,"user":{"login":"canny"},"body":"looks braw","author_association":"MEMBER"},
			{"id":2,"user":{"login":"scunner"},"body":"dreich","author_association":"NONE"}
		]]`, nil
	}

	comments, ok := fetchComments(context.Background(), "gh", "croft/loch", 1, "", "issues", 0)
	if !ok {
		t.Fatal("expected ok=true")
	}

	if len(comments) != 2 {
		t.Fatalf("expected 2 comments, got %d", len(comments))
	}

	if comments[0].AuthorAssociation != "MEMBER" {
		t.Errorf("author_association not decoded for comment 0: got %q, want MEMBER", comments[0].AuthorAssociation)
	}

	if comments[1].AuthorAssociation != "NONE" {
		t.Errorf("author_association not decoded for comment 1: got %q, want NONE", comments[1].AuthorAssociation)
	}
}

func TestFetchCommentsDegradedReportsNotOK(t *testing.T) {
	orig := ghRunner
	defer func() { ghRunner = orig }()

	ghRunner = func(ctx context.Context, ghBin, dir string, args ...string) (string, error) {
		return "", context.DeadlineExceeded
	}

	comments, ok := fetchComments(context.Background(), "gh", "croft/loch", 1, "", "issues", 0)
	if ok {
		t.Error("degraded fetch should report ok=false")
	}

	if comments != nil {
		t.Errorf("degraded fetch should return nil, got %v", comments)
	}
}

func TestResolvePRParsesMergeable(t *testing.T) {
	orig := ghRunner
	defer func() { ghRunner = orig }()

	calls := 0
	ghRunner = func(ctx context.Context, ghBin, dir string, args ...string) (string, error) {
		calls++
		if calls == 1 { // gh pr list
			return `[{"number":4,"state":"OPEN","isDraft":false,"url":"https://github.com/croft/loch/pull/4","headRefOid":"sha1","mergeable":"CONFLICTING"}]`, nil
		}

		return `[]`, nil // checks/comments
	}

	d, found, err := resolvePR(context.Background(), "gh", "croft/loch", "bide", "", 0)
	if err != nil || !found {
		t.Fatalf("expected found PR, got found=%v err=%v", found, err)
	}

	if d.Mergeable != "CONFLICTING" {
		t.Errorf("Mergeable = %q, want CONFLICTING", d.Mergeable)
	}
}

func TestResolvePRNoPR(t *testing.T) {
	orig := ghRunner
	defer func() { ghRunner = orig }()

	ghRunner = func(ctx context.Context, ghBin, dir string, args ...string) (string, error) {
		return `[]`, nil
	}

	_, found, err := resolvePR(context.Background(), "gh", "croft/loch", "bide", "", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if found {
		t.Error("expected found=false for empty pr list")
	}
}

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
	ghRunner = func(ctx context.Context, ghBin, dir string, args ...string) (string, error) {
		return "", errors.New("gh boom")
	}

	if _, _, err := resolvePR(context.Background(), "gh", "croft/loch", "bide", "", 0); err == nil {
		t.Error("expected error when gh pr list fails")
	}

	// Unparseable JSON → parse error.
	ghRunner = func(ctx context.Context, ghBin, dir string, args ...string) (string, error) {
		return "not json", nil
	}

	if _, _, err := resolvePR(context.Background(), "gh", "croft/loch", "bide", "", 0); err == nil {
		t.Error("expected parse error for malformed pr list JSON")
	}
}

func TestFetchChecks_Degraded_Cov(t *testing.T) {
	orig := ghRunner
	defer func() { ghRunner = orig }()

	// Error with empty output → no state, no failing.
	ghRunner = func(ctx context.Context, ghBin, dir string, args ...string) (string, error) {
		return "", errors.New("no checks")
	}

	if state, fail, _, _, total := fetchChecks(context.Background(), "gh", "croft/loch", 1, "", 0); state != "" || fail != nil || total != 0 {
		t.Errorf("error+empty output should give ('',nil,total=0), got (%q,%v,total=%d)", state, fail, total)
	}

	// Malformed JSON → ('',nil).
	ghRunner = func(ctx context.Context, ghBin, dir string, args ...string) (string, error) {
		return "{bad", nil
	}

	if state, _, _, _, _ := fetchChecks(context.Background(), "gh", "croft/loch", 1, "", 0); state != "" {
		t.Errorf("malformed checks JSON should give '', got %q", state)
	}

	// Empty checks array → ('',nil).
	ghRunner = func(ctx context.Context, ghBin, dir string, args ...string) (string, error) {
		return "[]", nil
	}

	if state, fail, _, _, total := fetchChecks(context.Background(), "gh", "croft/loch", 1, "", 0); state != "" || fail != nil || total != 0 {
		t.Errorf("empty checks should give ('',nil,total=0), got (%q,%v,total=%d)", state, fail, total)
	}

	// Non-zero exit but JSON still on stdout (gh pr checks does this when red).
	ghRunner = func(ctx context.Context, ghBin, dir string, args ...string) (string, error) {
		return `[{"name":"build","bucket":"fail"}]`, errors.New("exit 1")
	}

	if state, fail, _, passed, total := fetchChecks(context.Background(), "gh", "croft/loch", 1, "", 0); state != "failing" || len(fail) != 1 || passed != 0 || total != 1 {
		t.Errorf("failing checks should still parse despite exit 1, got (%q,%v,passed=%d,total=%d)", state, fail, passed, total)
	}
}

func TestFetchComments_EmptyAndBad_Cov(t *testing.T) {
	orig := ghRunner
	defer func() { ghRunner = orig }()

	// Empty output → (nil, true): a real, empty comment set.
	ghRunner = func(ctx context.Context, ghBin, dir string, args ...string) (string, error) {
		return "", nil
	}

	if comments, ok := fetchComments(context.Background(), "gh", "croft/loch", 1, "", "issues", 0); !ok || comments != nil {
		t.Errorf("empty output should be (nil,true), got (%v,%v)", comments, ok)
	}

	// Unparseable → (nil, false): degraded.
	ghRunner = func(ctx context.Context, ghBin, dir string, args ...string) (string, error) {
		return "not json", nil
	}

	if comments, ok := fetchComments(context.Background(), "gh", "croft/loch", 1, "", "pulls", 0); ok || comments != nil {
		t.Errorf("bad JSON should be (nil,false), got (%v,%v)", comments, ok)
	}
}
