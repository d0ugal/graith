package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/d0ugal/graith/internal/git"
)

// ghpr.go is the GitHub reader for the PR-watch loop. It shells out to the `gh`
// CLI (inheriting the user's gh auth) rather than embedding an HTTP client. All
// calls run with a short timeout and GH_PROMPT_DISABLED so the daemon's gh can
// never block on interactive auth.

const ghTimeout = 5 * time.Second

// prData is the resolved PR plus its CI and comment state for one poll.
type prData struct {
	Number         int
	State          string // open | draft | merged | closed
	URL            string
	ReviewDecision string // approved | changes_requested | review_required | ""
	HeadRefOid     string
	CIState        string   // passing | failing | pending
	FailingChecks  []string // human-readable "name" of each failing check
	IssueComments  []ghComment
	ReviewComments []ghComment
	// CommentsOK is false if any comment fetch degraded (timeout/error), so the
	// caller does not prime comment cursors from a partial read (which would
	// later dump the whole backlog as "new").
	CommentsOK bool
}

type ghComment struct {
	ID      int64  `json:"id"`
	User    ghUser `json:"user"`
	Body    string `json:"body"`
	Path    string `json:"path,omitempty"`
	Line    int    `json:"line,omitempty"`
	HTMLURL string `json:"html_url,omitempty"`
}

type ghUser struct {
	Login string `json:"login"`
}

// ghRunner runs a gh command and returns trimmed stdout. Swapped in tests.
var ghRunner = func(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1")
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// ghAvailable reports whether the gh binary is on PATH.
func ghAvailable() bool {
	_, err := exec.LookPath("gh")
	return err == nil
}

var githubRemoteRe = regexp.MustCompile(`github\.com[:/]+([^/]+)/(.+?)(?:\.git)?$`)

// parseGitHubRemote extracts "owner/repo" from a git remote URL, or returns
// ok=false for non-GitHub remotes (the PR-watch loop then permanently skips
// that repo).
func parseGitHubRemote(remoteURL string) (slug string, ok bool) {
	remoteURL = strings.TrimSpace(remoteURL)
	m := githubRemoteRe.FindStringSubmatch(remoteURL)
	if m == nil {
		return "", false
	}
	owner, repo := m[1], strings.TrimSuffix(m[2], ".git")
	if owner == "" || repo == "" {
		return "", false
	}
	return owner + "/" + repo, true
}

// repoSlug resolves the GitHub owner/repo for a worktree from its origin remote.
func repoSlug(worktreePath string) (string, bool) {
	url, err := git.RunOutput(worktreePath, "remote", "get-url", "origin")
	if err != nil {
		return "", false
	}
	return parseGitHubRemote(url)
}

// effectiveBranch returns the branch to resolve a PR against: the recorded
// SessionState.Branch when set, otherwise the live HEAD of the worktree. It is
// empty for detached/no-branch worktrees (caller then skips the session).
func effectiveBranch(branch, worktreePath string) string {
	if branch != "" {
		return branch
	}
	if worktreePath == "" {
		return ""
	}
	head, err := git.RunOutput(worktreePath, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(head)
}

// prListItem is the JSON shape of `gh pr list --json ...`.
type prListItem struct {
	Number         int    `json:"number"`
	State          string `json:"state"` // OPEN | CLOSED | MERGED
	IsDraft        bool   `json:"isDraft"`
	URL            string `json:"url"`
	ReviewDecision string `json:"reviewDecision"`
	HeadRefOid     string `json:"headRefOid"`
}

// prCheck is the JSON shape of one `gh pr checks --json ...` item.
type prCheck struct {
	Name   string `json:"name"`
	State  string `json:"state"`  // raw conclusion/status, e.g. SUCCESS, FAILURE, PENDING
	Bucket string `json:"bucket"` // pass | fail | pending | skipping | cancel
	Link   string `json:"link"`
}

// resolvePR finds the PR for a branch and fills in CI + comment state. It
// returns ok=false (no error) when there is simply no PR for the branch.
func resolvePR(ctx context.Context, slug, branch, worktreePath string) (prData, bool, error) {
	cctx, cancel := context.WithTimeout(ctx, ghTimeout)
	defer cancel()

	out, err := ghRunner(cctx, worktreePath,
		"pr", "list", "--repo", slug, "--head", branch, "--state", "all",
		"--json", "number,state,isDraft,url,reviewDecision,headRefOid", "--limit", "1")
	if err != nil {
		return prData{}, false, fmt.Errorf("gh pr list: %w", err)
	}
	var items []prListItem
	if err := json.Unmarshal([]byte(out), &items); err != nil {
		return prData{}, false, fmt.Errorf("parse pr list: %w", err)
	}
	if len(items) == 0 {
		return prData{}, false, nil
	}
	it := items[0]

	d := prData{
		Number:         it.Number,
		State:          normalizePRState(it.State, it.IsDraft),
		URL:            it.URL,
		ReviewDecision: strings.ToLower(it.ReviewDecision),
		HeadRefOid:     it.HeadRefOid,
	}

	// CI checks + comments — only meaningful while the PR is open.
	d.CommentsOK = true
	if d.State == "open" || d.State == "draft" {
		d.CIState, d.FailingChecks = fetchChecks(ctx, slug, it.Number, worktreePath)
		var issueOK, reviewOK bool
		d.IssueComments, issueOK = fetchComments(ctx, slug, it.Number, worktreePath, "issues")
		d.ReviewComments, reviewOK = fetchComments(ctx, slug, it.Number, worktreePath, "pulls")
		d.CommentsOK = issueOK && reviewOK
	}
	return d, true, nil
}

func normalizePRState(state string, isDraft bool) string {
	switch strings.ToUpper(state) {
	case "MERGED":
		return "merged"
	case "CLOSED":
		return "closed"
	default:
		if isDraft {
			return "draft"
		}
		return "open"
	}
}

// fetchChecks returns the aggregate CI state and the names of failing checks.
// It uses `gh pr checks` whose `bucket` field is GitHub's own categorisation,
// avoiding the heterogeneous statusCheckRollup union. NEUTRAL/SKIPPED are
// pass-like and never counted as failures.
func fetchChecks(ctx context.Context, slug string, number int, worktreePath string) (state string, failing []string) {
	cctx, cancel := context.WithTimeout(ctx, ghTimeout)
	defer cancel()

	out, err := ghRunner(cctx, worktreePath,
		"pr", "checks", fmt.Sprintf("%d", number), "--repo", slug,
		"--json", "name,state,bucket,link")
	if err != nil {
		// gh pr checks exits non-zero when checks are failing; it still prints
		// JSON on stdout, so try to parse what we got before giving up.
		if out == "" {
			return "", nil
		}
	}
	var checks []prCheck
	if err := json.Unmarshal([]byte(out), &checks); err != nil {
		return "", nil
	}
	if len(checks) == 0 {
		return "", nil
	}
	anyPending := false
	for _, c := range checks {
		switch ciBucket(c) {
		case "fail":
			failing = append(failing, c.Name)
		case "pending":
			anyPending = true
		}
	}
	switch {
	case len(failing) > 0:
		return "failing", failing
	case anyPending:
		return "pending", nil
	default:
		return "passing", nil
	}
}

// ciBucket categorises a check as fail | pending | pass, preferring gh's own
// bucket and falling back to the raw state. SKIPPED/NEUTRAL/CANCELLED are
// pass-like (not actionable failures that should wake an agent).
func ciBucket(c prCheck) string {
	switch strings.ToLower(c.Bucket) {
	case "fail":
		return "fail"
	case "pending":
		return "pending"
	case "pass", "skipping", "cancel":
		return "pass"
	}
	switch strings.ToUpper(c.State) {
	case "FAILURE", "TIMED_OUT", "ACTION_REQUIRED", "STARTUP_FAILURE":
		return "fail"
	case "QUEUED", "IN_PROGRESS", "PENDING", "WAITING", "REQUESTED":
		return "pending"
	default: // SUCCESS, NEUTRAL, SKIPPED, CANCELLED, ...
		return "pass"
	}
}

// fetchComments reads a paginated comment surface ("issues" or "pulls"). The
// bool is false if the fetch degraded (error or unparseable), so the caller can
// avoid priming a cursor from a partial read. An empty-but-ok result is
// (nil, true).
func fetchComments(ctx context.Context, slug string, number int, worktreePath, surface string) ([]ghComment, bool) {
	cctx, cancel := context.WithTimeout(ctx, ghTimeout)
	defer cancel()

	path := fmt.Sprintf("repos/%s/%s/%d/comments?per_page=100", slug, surface, number)
	out, err := ghRunner(cctx, worktreePath, "api", "--paginate", path)
	if err != nil {
		return nil, false
	}
	if out == "" {
		return nil, true
	}
	var comments []ghComment
	if err := json.Unmarshal([]byte(out), &comments); err != nil {
		return nil, false
	}
	return comments, true
}
