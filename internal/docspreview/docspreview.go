// Package docspreview owns the docs-preview screenshots branch and sticky
// comment mutation policy used by GitHub Actions.
package docspreview

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	ScreenshotsBranch = "screenshots"
	StickyMarker      = "<!-- docs-preview -->"
	DefaultMaxAge     = 30 * 24 * time.Hour

	// EmptyTreeSHA is git's well-known empty tree. The GitHub API rejects
	// createTree with an empty tree slice, so destructive rewrites that keep no
	// files commit this tree directly.
	EmptyTreeSHA = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
)

const defaultMaxAttempts = 5

type Logger interface {
	Info(message string)
}

type noopLogger struct{}

func (noopLogger) Info(string) {}

type Repository struct {
	Owner string
	Name  string
}

func (repo Repository) FullName() string {
	if repo.Owner == "" || repo.Name == "" {
		return ""
	}

	return repo.Owner + "/" + repo.Name
}

func ParseRepository(value string) (Repository, error) {
	owner, name, ok := strings.Cut(value, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return Repository{}, fmt.Errorf("repository must be owner/name, got %q", value)
	}

	return Repository{Owner: owner, Name: name}, nil
}

type Event struct {
	Repository  EventRepository `json:"repository"`
	PullRequest *PullRequest    `json:"pull_request"`
}

type EventRepository struct {
	FullName string     `json:"full_name"`
	Name     string     `json:"name"`
	Owner    EventOwner `json:"owner"`
}

type EventOwner struct {
	Login string `json:"login"`
	Name  string `json:"name"`
}

type PullRequest struct {
	Number int         `json:"number"`
	Head   PullRef     `json:"head"`
	Base   PullBaseRef `json:"base"`
}

type PullRef struct {
	Repo *PullRepository `json:"repo"`
}

type PullRepository struct {
	FullName string `json:"full_name"`
}

type PullBaseRef struct {
	Ref string `json:"ref"`
	SHA string `json:"sha"`
}

func RepositoryFromEvent(event Event) (Repository, error) {
	if event.Repository.FullName != "" {
		return ParseRepository(event.Repository.FullName)
	}

	owner := event.Repository.Owner.Login
	if owner == "" {
		owner = event.Repository.Owner.Name
	}

	if owner != "" && event.Repository.Name != "" {
		return Repository{Owner: owner, Name: event.Repository.Name}, nil
	}

	return Repository{}, errors.New("event does not contain repository owner/name")
}

// IsSameRepoPR fails closed for deleted/fork heads. Fork PR tokens are
// read-only even when workflow permissions request writes, and publish runs
// from PR-controlled code for same-repo PRs only.
func IsSameRepoPR(event Event, repo Repository) bool {
	if event.PullRequest == nil || event.PullRequest.Head.Repo == nil {
		return false
	}

	return event.PullRequest.Head.Repo.FullName == repo.FullName()
}

func IsStaleRunDir(path string, now time.Time, maxAge time.Duration) bool {
	segments := strings.Split(path, "/")
	if len(segments) < 3 || !strings.HasPrefix(segments[0], "pr-") {
		return false
	}

	runDir := segments[1]
	if len(runDir) < len("20060102-") || runDir[8] != '-' {
		return false
	}

	runDate, err := time.Parse("20060102", runDir[:8])
	if err != nil {
		return false
	}

	return now.Sub(runDate) > maxAge
}

type BranchClient interface {
	GetRef(ctx context.Context, owner, repo, ref string) (Ref, error)
	CreateRef(ctx context.Context, owner, repo, ref, sha string) error
	UpdateRef(ctx context.Context, owner, repo, ref, sha string) error
	GetCommit(ctx context.Context, owner, repo, sha string) (Commit, error)
}

type TreeReader interface {
	GetTree(ctx context.Context, owner, repo, sha string, recursive bool) (Tree, error)
}

type TreeWriter interface {
	CreateTree(ctx context.Context, owner, repo string, entries []TreeEntry, baseTree string) (string, error)
	CreateCommit(ctx context.Context, owner, repo string, message, tree string, parents []string) (string, error)
}

type BlobWriter interface {
	CreateBlob(ctx context.Context, owner, repo string, content, encoding string) (string, error)
}

type CommentClient interface {
	ListComments(ctx context.Context, owner, repo string, issueNumber int) ([]Comment, error)
	UpdateComment(ctx context.Context, owner, repo string, commentID int64, body string) error
	CreateComment(ctx context.Context, owner, repo string, issueNumber int, body string) error
}

type CleanupClient interface {
	BranchClient
	TreeReader
	TreeWriter
	CommentClient
}

type PruneClient interface {
	BranchClient
	TreeReader
	TreeWriter
}

type PublishClient interface {
	BranchClient
	TreeWriter
	BlobWriter
	CommentClient
}

type Ref struct {
	SHA string
}

type Commit struct {
	SHA     string
	TreeSHA string
	Parents []string
	Message string
}

type Tree struct {
	SHA       string
	Entries   []TreeEntry
	Truncated bool
}

type TreeEntry struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Type string `json:"type"`
	SHA  string `json:"sha"`
}

type Comment struct {
	ID   int64
	Body string
}

type StatusError struct {
	Status  int
	Message string
}

func (err *StatusError) Error() string {
	if err.Message == "" {
		return fmt.Sprintf("GitHub API returned HTTP %d", err.Status)
	}

	return fmt.Sprintf("GitHub API returned HTTP %d: %s", err.Status, err.Message)
}

func statusCode(err error) int {
	var status *StatusError
	if errors.As(err, &status) {
		return status.Status
	}

	return 0
}

func ListBlobsOrFail(ctx context.Context, client TreeReader, repo Repository, treeSHA string) ([]TreeEntry, error) {
	tree, err := client.GetTree(ctx, repo.Owner, repo.Name, treeSHA, true)
	if err != nil {
		return nil, err
	}

	if tree.Truncated {
		return nil, errors.New("screenshots tree was truncated by the GitHub API; refusing to rebuild the branch from a partial listing, which would delete every omitted screenshot")
	}

	blobs := make([]TreeEntry, 0, len(tree.Entries))
	for _, entry := range tree.Entries {
		if entry.Type == "blob" {
			blobs = append(blobs, entry)
		}
	}

	return blobs, nil
}

type BranchTip struct {
	CommitSHA string
	TreeSHA   string
}

type BranchResult struct {
	Outcome  string
	Attempts int
}

type CommitToBranchOptions struct {
	Client         BranchClient
	Logger         Logger
	Repo           Repository
	CreateIfAbsent bool
	MaxAttempts    int
	BuildCommit    func(context.Context, *BranchTip) (string, error)
}

func CommitToBranch(ctx context.Context, options CommitToBranchOptions) (BranchResult, error) {
	if options.Client == nil {
		return BranchResult{}, errors.New("GitHub client is required")
	}

	if options.BuildCommit == nil {
		return BranchResult{}, errors.New("build commit callback is required")
	}

	if options.MaxAttempts == 0 {
		options.MaxAttempts = defaultMaxAttempts
	}

	if options.MaxAttempts < 1 {
		return BranchResult{}, errors.New("max attempts must be positive")
	}

	logger := options.Logger
	if logger == nil {
		logger = noopLogger{}
	}

	var lastErr error

	for attempt := 1; attempt <= options.MaxAttempts; attempt++ {
		// Each 422 means another writer created or advanced the screenshots
		// branch. Re-read and rebuild on the winner tip; non-422 errors are
		// unrelated API failures and must propagate.
		tip, err := getBranchTip(ctx, options.Client, options.Repo)
		if err != nil {
			return BranchResult{}, err
		}

		if tip == nil && !options.CreateIfAbsent {
			return BranchResult{Outcome: "absent", Attempts: attempt}, nil
		}

		newCommitSHA, err := options.BuildCommit(ctx, tip)
		if err != nil {
			return BranchResult{}, err
		}

		if newCommitSHA == "" {
			return BranchResult{Outcome: "noop", Attempts: attempt}, nil
		}

		if tip == nil {
			err = options.Client.CreateRef(ctx, options.Repo.Owner, options.Repo.Name, "refs/heads/"+ScreenshotsBranch, newCommitSHA)
			if err == nil {
				return BranchResult{Outcome: "created", Attempts: attempt}, nil
			}
		} else {
			err = options.Client.UpdateRef(ctx, options.Repo.Owner, options.Repo.Name, "heads/"+ScreenshotsBranch, newCommitSHA)
			if err == nil {
				return BranchResult{Outcome: "updated", Attempts: attempt}, nil
			}
		}

		if statusCode(err) != 422 {
			return BranchResult{}, err
		}

		lastErr = err

		logger.Info(fmt.Sprintf(
			"screenshots branch write lost a race (attempt %d/%d); re-reading tip and retrying",
			attempt,
			options.MaxAttempts,
		))
	}

	message := fmt.Sprintf("could not update the %s branch after %d attempts", ScreenshotsBranch, options.MaxAttempts)
	if lastErr != nil {
		message += ": " + lastErr.Error()
	}

	return BranchResult{}, errors.New(message)
}

func getBranchTip(ctx context.Context, client BranchClient, repo Repository) (*BranchTip, error) {
	ref, err := client.GetRef(ctx, repo.Owner, repo.Name, "heads/"+ScreenshotsBranch)
	if err != nil {
		if statusCode(err) == 404 {
			return nil, nil
		}

		return nil, err
	}

	commit, err := client.GetCommit(ctx, repo.Owner, repo.Name, ref.SHA)
	if err != nil {
		return nil, err
	}

	return &BranchTip{CommitSHA: ref.SHA, TreeSHA: commit.TreeSHA}, nil
}

func BuildRewriteCommit(ctx context.Context, client TreeWriter, repo Repository, kept []TreeEntry, parentSHA, message string) (string, error) {
	treeSHA := EmptyTreeSHA

	if len(kept) > 0 {
		entries := make([]TreeEntry, 0, len(kept))
		for _, entry := range kept {
			entries = append(entries, TreeEntry{
				Path: entry.Path,
				Mode: entry.Mode,
				Type: entry.Type,
				SHA:  entry.SHA,
			})
		}

		var err error

		// Omit base_tree intentionally: cleanup/prune are full-tree rewrites, so
		// every path not in kept is deleted. Callers must list the complete tree
		// first and refuse truncated results.
		treeSHA, err = client.CreateTree(ctx, repo.Owner, repo.Name, entries, "")
		if err != nil {
			return "", err
		}
	}

	return client.CreateCommit(ctx, repo.Owner, repo.Name, message, treeSHA, []string{parentSHA})
}

type CleanupOptions struct {
	Client CleanupClient
	Logger Logger
	Repo   Repository
	Event  Event
}

func Cleanup(ctx context.Context, options CleanupOptions) error {
	logger := options.Logger
	if logger == nil {
		logger = noopLogger{}
	}

	if !IsSameRepoPR(options.Event, options.Repo) {
		logger.Info("Fork PR: no screenshots were published, nothing to clean up.")
		return nil
	}

	if options.Event.PullRequest == nil || options.Event.PullRequest.Number <= 0 {
		return errors.New("pull request number is required")
	}

	pr := options.Event.PullRequest.Number
	prefix := fmt.Sprintf("pr-%d/", pr)
	removed := 0

	result, err := CommitToBranch(ctx, CommitToBranchOptions{
		Client: options.Client,
		Logger: logger,
		Repo:   options.Repo,
		BuildCommit: func(ctx context.Context, tip *BranchTip) (string, error) {
			blobs, err := ListBlobsOrFail(ctx, options.Client, options.Repo, tip.TreeSHA)
			if err != nil {
				return "", err
			}

			kept := make([]TreeEntry, 0, len(blobs))
			for _, blob := range blobs {
				if !strings.HasPrefix(blob.Path, prefix) {
					kept = append(kept, blob)
				}
			}

			if len(kept) == len(blobs) {
				logger.Info(fmt.Sprintf("No screenshots found under %s: nothing to clean up.", prefix))
				return "", nil
			}

			removed = len(blobs) - len(kept)

			return BuildRewriteCommit(
				ctx,
				options.Client,
				options.Repo,
				kept,
				tip.CommitSHA,
				fmt.Sprintf("docs preview: clean up PR #%d", pr),
			)
		},
	})
	if err != nil {
		return err
	}

	switch result.Outcome {
	case "absent":
		logger.Info(fmt.Sprintf("No %s branch: nothing to clean up.", ScreenshotsBranch))
		return nil
	case "updated":
		logger.Info(fmt.Sprintf("Removed %d screenshot(s) for PR #%d.", removed, pr))
	default:
		return nil
	}

	comments, err := options.Client.ListComments(ctx, options.Repo.Owner, options.Repo.Name, pr)
	if err != nil {
		return err
	}

	for _, comment := range comments {
		if strings.Contains(comment.Body, StickyMarker) {
			body := StickyMarker + "\n### \U0001F4F8 Docs preview\n\n_Preview screenshots for this PR were cleaned up after it was closed._"
			return options.Client.UpdateComment(ctx, options.Repo.Owner, options.Repo.Name, comment.ID, body)
		}
	}

	return nil
}

type PruneOptions struct {
	Client PruneClient
	Logger Logger
	Repo   Repository
	Now    time.Time
	MaxAge time.Duration
}

func Prune(ctx context.Context, options PruneOptions) error {
	logger := options.Logger
	if logger == nil {
		logger = noopLogger{}
	}

	now := options.Now
	if now.IsZero() {
		now = time.Now()
	}

	maxAge := options.MaxAge
	if maxAge == 0 {
		maxAge = DefaultMaxAge
	}

	removed := 0

	result, err := CommitToBranch(ctx, CommitToBranchOptions{
		Client: options.Client,
		Logger: logger,
		Repo:   options.Repo,
		BuildCommit: func(ctx context.Context, tip *BranchTip) (string, error) {
			blobs, err := ListBlobsOrFail(ctx, options.Client, options.Repo, tip.TreeSHA)
			if err != nil {
				return "", err
			}

			kept := make([]TreeEntry, 0, len(blobs))
			for _, blob := range blobs {
				if !IsStaleRunDir(blob.Path, now, maxAge) {
					kept = append(kept, blob)
				}
			}

			removed = len(blobs) - len(kept)
			if removed == 0 {
				logger.Info("No stale screenshot dirs to prune.")
				return "", nil
			}

			return BuildRewriteCommit(
				ctx,
				options.Client,
				options.Repo,
				kept,
				tip.CommitSHA,
				fmt.Sprintf("docs preview: prune %d stale screenshot(s)", removed),
			)
		},
	})
	if err != nil {
		return err
	}

	switch result.Outcome {
	case "absent":
		logger.Info(fmt.Sprintf("No %s branch: nothing to prune.", ScreenshotsBranch))
	case "updated":
		logger.Info(fmt.Sprintf("Pruned %d screenshot(s) older than 30 days.", removed))
	}

	return nil
}

type PreviewManifest map[string]map[string]PreviewEntry

type PreviewEntry struct {
	Kind string `json:"kind"`
	File string `json:"file,omitempty"`
}

type PublishOptions struct {
	Client       PublishClient
	Logger       Logger
	Repo         Repository
	Event        Event
	ManifestPath string
	AssetsDir    string
	SHA          string
	RunID        string
	RunAttempt   string
	Now          time.Time
}

func Publish(ctx context.Context, options PublishOptions) error {
	logger := options.Logger
	if logger == nil {
		logger = noopLogger{}
	}

	if !IsSameRepoPR(options.Event, options.Repo) {
		logger.Info("Fork PR: screenshots are published only for same-repo PRs; skipping publish.")
		return nil
	}

	if options.Event.PullRequest == nil || options.Event.PullRequest.Number <= 0 {
		return errors.New("pull request number is required")
	}

	manifest, err := readManifest(options.ManifestPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			logger.Info("No diff manifest: nothing to publish.")
			return nil
		}

		return fmt.Errorf("read diff manifest: %w", err)
	}

	if len(manifest) == 0 {
		logger.Info("No changed pages.")
		return nil
	}

	if options.SHA == "" {
		return errors.New("sha is required")
	}

	if options.RunID == "" {
		return errors.New("run id is required")
	}

	now := options.Now
	if now.IsZero() {
		now = time.Now()
	}

	runAttempt := options.RunAttempt
	if runAttempt == "" {
		runAttempt = "1"
	}

	files := manifestFiles(manifest)

	shortSHA := options.SHA
	if len(shortSHA) > 7 {
		shortSHA = shortSHA[:7]
	}

	// Include run id and attempt so re-runs get fresh raw.githubusercontent.com
	// URLs instead of being served stale cached images from a previous attempt.
	runDir := fmt.Sprintf(
		"pr-%d/%s-%s-%s.%s",
		options.Event.PullRequest.Number,
		now.UTC().Format("20060102"),
		shortSHA,
		options.RunID,
		runAttempt,
	)

	if len(files) > 0 {
		entries := make([]TreeEntry, 0, len(files))

		// Blobs are content-addressed and independent of the branch tip, so
		// upload once outside the compare-and-retry loop. The commit itself is
		// rebuilt inside the loop so it chains onto the current tip.
		for _, file := range files {
			data, err := os.ReadFile(filepath.Join(options.AssetsDir, file))
			if err != nil {
				return err
			}

			blobSHA, err := options.Client.CreateBlob(
				ctx,
				options.Repo.Owner,
				options.Repo.Name,
				base64.StdEncoding.EncodeToString(data),
				"base64",
			)
			if err != nil {
				return err
			}

			entries = append(entries, TreeEntry{
				Path: runDir + "/" + file,
				Mode: "100644",
				Type: "blob",
				SHA:  blobSHA,
			})
		}

		_, err = CommitToBranch(ctx, CommitToBranchOptions{
			Client:         options.Client,
			Logger:         logger,
			Repo:           options.Repo,
			CreateIfAbsent: true,
			BuildCommit: func(ctx context.Context, tip *BranchTip) (string, error) {
				baseTree := ""
				parents := []string{}

				if tip != nil {
					baseTree = tip.TreeSHA
					parents = []string{tip.CommitSHA}
				}

				// Publish appends into the existing screenshots tree when it has
				// one, but creates a root commit with parents: [] for the first
				// branch commit.
				treeSHA, err := options.Client.CreateTree(ctx, options.Repo.Owner, options.Repo.Name, entries, baseTree)
				if err != nil {
					return "", err
				}

				return options.Client.CreateCommit(
					ctx,
					options.Repo.Owner,
					options.Repo.Name,
					fmt.Sprintf("docs preview: PR #%d @ %s", options.Event.PullRequest.Number, shortSHA),
					treeSHA,
					parents,
				)
			},
		})
		if err != nil {
			return err
		}
	}

	body := buildPreviewCommentBody(options.Repo, *options.Event.PullRequest, shortSHA, runDir, manifest, len(files) > 0)

	comments, err := options.Client.ListComments(ctx, options.Repo.Owner, options.Repo.Name, options.Event.PullRequest.Number)
	if err != nil {
		return err
	}

	for _, comment := range comments {
		if strings.Contains(comment.Body, StickyMarker) {
			return options.Client.UpdateComment(ctx, options.Repo.Owner, options.Repo.Name, comment.ID, body)
		}
	}

	return options.Client.CreateComment(ctx, options.Repo.Owner, options.Repo.Name, options.Event.PullRequest.Number, body)
}

func readManifest(path string) (PreviewManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var manifest PreviewManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, err
	}

	return manifest, nil
}

func manifestFiles(manifest PreviewManifest) []string {
	seen := make(map[string]bool)

	for _, viewports := range manifest {
		for _, entry := range viewports {
			if entry.File != "" {
				seen[entry.File] = true
			}
		}
	}

	files := make([]string, 0, len(seen))
	for file := range seen {
		files = append(files, file)
	}

	sort.Strings(files)

	return files
}

func buildPreviewCommentBody(repo Repository, pr PullRequest, shortSHA, runDir string, manifest PreviewManifest, storedImages bool) string {
	names := make([]string, 0, len(manifest))
	allKinds := make(map[string]bool)

	for name, viewports := range manifest {
		names = append(names, name)

		for _, entry := range viewports {
			allKinds[entry.Kind] = true
		}
	}

	sort.Strings(names)

	raw := func(file string) string {
		return fmt.Sprintf(
			"https://raw.githubusercontent.com/%s/%s/%s/%s/%s",
			repo.Owner,
			repo.Name,
			ScreenshotsBranch,
			runDir,
			file,
		)
	}
	cell := func(entry PreviewEntry, ok bool, width int) string {
		if !ok {
			return "\u2014"
		}

		if entry.Kind == "same" {
			return "_no visual change_"
		}

		return fmt.Sprintf(`<img src="%s" width="%d">`, raw(entry.File), width)
	}

	var body strings.Builder
	body.WriteString(StickyMarker)
	body.WriteString("\n### \U0001F4F8 Docs preview\n\n")
	fmt.Fprintf(&body, "Diffed %d changed page(s) against `%s` at commit `%s`. ",
		len(names),
		pr.Base.Ref,
		shortSHA)
	body.WriteString("Each image shows only the changed regions: **base \u2502 head**, cropped with context.\n\n")

	if allKinds["new"] {
		body.WriteString("_Pages new in this PR (no baseline) are shown in full._\n\n")
	}

	if allKinds["deleted"] {
		fmt.Fprintf(&body, "_Pages removed in this PR are shown as their last render on `%s`._\n\n", pr.Base.Ref)
	}

	for _, name := range names {
		viewports := manifest[name]
		desktop, hasDesktop := viewports["desktop"]
		mobile, hasMobile := viewports["mobile"]
		removed := (hasDesktop && desktop.Kind == "deleted") || (hasMobile && mobile.Kind == "deleted")

		body.WriteString("<details><summary><b>")
		body.WriteString(name)
		body.WriteString("</b>")

		if removed {
			body.WriteString(" \u2014 removed")
		}

		body.WriteString("</summary>\n\n")
		body.WriteString("| Desktop | Mobile |\n|---|---|\n")
		body.WriteString("| ")
		body.WriteString(cell(desktop, hasDesktop, 620))
		body.WriteString(" | ")
		body.WriteString(cell(mobile, hasMobile, 300))
		body.WriteString(" |\n\n</details>\n\n")
	}

	if storedImages {
		fmt.Fprintf(&body, "_Screenshots stored on the [`%s`](https://github.com/%s/%s/tree/%s/%s) branch._",
			ScreenshotsBranch,
			repo.Owner,
			repo.Name,
			ScreenshotsBranch,
			runDir)
	} else {
		body.WriteString("_No visual changes to preview._")
	}

	return body.String()
}
