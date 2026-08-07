package docspreview

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

var testRepo = Repository{Owner: "clachan", Name: "croft"}

func TestListBlobsOrFail(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		truncated bool
		want      []string
		wantErr   string
	}{
		"braw truncated tree fails closed": {
			truncated: true,
			wantErr:   "truncated",
		},
		"canny tree returns only blobs": {
			want: []string{"pr-1/braw/x.png", "pr-2/canny/y.png"},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			github := newFakeGitHub()
			github.truncatedTrees["tree-braw"] = test.truncated
			github.trees["tree-braw"] = []TreeEntry{
				{Path: "pr-dir", Type: "tree", Mode: "040000", SHA: "dir-sha"},
				blobEntry("pr-1/braw/x.png"),
				blobEntry("pr-2/canny/y.png"),
			}

			blobs, err := ListBlobsOrFail(context.Background(), github, testRepo, "tree-braw")
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("ListBlobsOrFail() error = %v, want %q", err, test.wantErr)
				}

				if github.calls.createTree != 0 || github.calls.updateRef != 0 {
					t.Fatalf("destructive calls after truncated tree: %+v", github.calls)
				}

				return
			}

			if err != nil {
				t.Fatal(err)
			}

			if got := entryPaths(blobs); !equalStrings(got, test.want) {
				t.Fatalf("blob paths = %v, want %v", got, test.want)
			}
		})
	}
}

func TestIsStaleRunDir(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 7, 0, 0, 0, 0, time.UTC)
	tests := map[string]struct {
		path string
		want bool
	}{
		"braw old run": {
			path: "pr-1/20260528-abc1234-99.1/x.png",
			want: true,
		},
		"canny recent run": {
			path: "pr-1/20260702-abc1234-99.1/x.png",
		},
		"dreich undated run": {
			path: "pr-1/nodate/x.png",
		},
		"bothy non pr path": {
			path: "README.md",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got := IsStaleRunDir(test.path, now, DefaultMaxAge); got != test.want {
				t.Fatalf("IsStaleRunDir(%q) = %v, want %v", test.path, got, test.want)
			}
		})
	}
}

func TestIsSameRepoPR(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		event Event
		want  bool
	}{
		"braw same repository": {
			event: prEvent(42, "clachan/croft"),
			want:  true,
		},
		"canny fork": {
			event: prEvent(42, "thrawn/croft"),
		},
		"dreich deleted fork": {
			event: Event{Repository: EventRepository{FullName: "clachan/croft"}, PullRequest: &PullRequest{Number: 42}},
		},
		"bothy no pull request": {
			event: Event{Repository: EventRepository{FullName: "clachan/croft"}},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got := IsSameRepoPR(test.event, testRepo); got != test.want {
				t.Fatalf("IsSameRepoPR() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestPreviewSuiteByName(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		name    string
		want    string
		wantErr string
	}{
		"braw default docs": {
			want: "docs",
		},
		"canny docs": {
			name: "docs",
			want: "docs",
		},
		"dreich session navigator": {
			name: "session-navigator",
			want: "session-navigator",
		},
		"thrawn unknown": {
			name:    "unknown",
			wantErr: "unknown preview suite",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := PreviewSuiteByName(test.name)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("PreviewSuiteByName() error = %v, want %q", err, test.wantErr)
				}

				return
			}

			if err != nil {
				t.Fatal(err)
			}

			if got.Name != test.want {
				t.Fatalf("suite name = %q, want %q", got.Name, test.want)
			}
		})
	}
}

func TestCommitToBranchCompareAndRetry(t *testing.T) {
	t.Parallel()

	t.Run("creates branch when absent and allowed", func(t *testing.T) {
		t.Parallel()

		github := newFakeGitHub()

		var seen []*BranchTip

		result, err := CommitToBranch(context.Background(), CommitToBranchOptions{
			Client:         github,
			Repo:           testRepo,
			CreateIfAbsent: true,
			BuildCommit: func(_ context.Context, tip *BranchTip) (string, error) {
				seen = append(seen, tip)
				return github.seedDetachedCommit(nil, "commit-1"), nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}

		if result.Outcome != "created" || result.Attempts != 1 {
			t.Fatalf("result = %+v, want created in one attempt", result)
		}

		if len(seen) != 1 || seen[0] != nil {
			t.Fatalf("seen tips = %+v, want only nil", seen)
		}

		if got := github.refs["heads/"+ScreenshotsBranch]; got == "" {
			t.Fatal("branch was not created")
		}
	})

	t.Run("returns absent without building when branch is missing", func(t *testing.T) {
		t.Parallel()

		github := newFakeGitHub()
		built := false

		result, err := CommitToBranch(context.Background(), CommitToBranchOptions{
			Client: github,
			Repo:   testRepo,
			BuildCommit: func(context.Context, *BranchTip) (string, error) {
				built = true
				return "unused", nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}

		if result.Outcome != "absent" || built {
			t.Fatalf("result = %+v built = %v, want absent without build", result, built)
		}
	})

	t.Run("returns noop without writing when callback has nothing to do", func(t *testing.T) {
		t.Parallel()

		github := newFakeGitHub()
		github.seedScreenshotsBranch([]TreeEntry{blobEntry("pr-1/braw/x.png")})

		result, err := CommitToBranch(context.Background(), CommitToBranchOptions{
			Client: github,
			Repo:   testRepo,
			BuildCommit: func(context.Context, *BranchTip) (string, error) {
				return "", nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}

		if result.Outcome != "noop" || github.calls.updateRef != 0 {
			t.Fatalf("result = %+v updateRef calls = %d, want noop without update", result, github.calls.updateRef)
		}
	})

	t.Run("rebuilds on winner tip after update race", func(t *testing.T) {
		t.Parallel()

		github := newFakeGitHub()
		first := github.seedScreenshotsBranch([]TreeEntry{blobEntry("pr-1/braw/x.png")})
		winner := github.seedDetachedCommit([]TreeEntry{blobEntry("pr-2/canny/y.png")}, "winner")
		github.beforeUpdateRef = func() {
			github.refs["heads/"+ScreenshotsBranch] = winner
			github.beforeUpdateRef = nil
		}

		var seen []BranchTip

		result, err := CommitToBranch(context.Background(), CommitToBranchOptions{
			Client: github,
			Repo:   testRepo,
			BuildCommit: func(_ context.Context, tip *BranchTip) (string, error) {
				seen = append(seen, *tip)
				return github.seedDetachedCommit(nil, "commit-on-"+tip.CommitSHA, tip.CommitSHA), nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}

		if result.Outcome != "updated" || result.Attempts != 2 {
			t.Fatalf("result = %+v, want updated after retry", result)
		}

		if got := []string{seen[0].CommitSHA, seen[1].CommitSHA}; !equalStrings(got, []string{first, winner}) {
			t.Fatalf("seen commits = %v, want [%s %s]", got, first, winner)
		}
	})

	t.Run("lost create race retries as update", func(t *testing.T) {
		t.Parallel()

		github := newFakeGitHub()
		winner := github.seedDetachedCommit([]TreeEntry{blobEntry("pr-7/strath/z.png")}, "winner")
		github.beforeCreateRef = func() {
			github.refs["heads/"+ScreenshotsBranch] = winner
			github.beforeCreateRef = nil
		}

		var seen []*BranchTip

		result, err := CommitToBranch(context.Background(), CommitToBranchOptions{
			Client:         github,
			Repo:           testRepo,
			CreateIfAbsent: true,
			BuildCommit: func(_ context.Context, tip *BranchTip) (string, error) {
				seen = append(seen, tip)

				parent := ""
				if tip != nil {
					parent = tip.CommitSHA
				}

				return github.seedDetachedCommit(nil, "commit", parent), nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}

		if result.Outcome != "updated" || result.Attempts != 2 {
			t.Fatalf("result = %+v, want update after create race", result)
		}

		if len(seen) != 2 || seen[0] != nil || seen[1].CommitSHA != winner {
			t.Fatalf("seen tips = %+v, want nil then winner", seen)
		}
	})

	t.Run("gives up after repeated races", func(t *testing.T) {
		t.Parallel()

		github := newFakeGitHub()
		github.seedScreenshotsBranch([]TreeEntry{blobEntry("pr-1/braw/x.png")})
		github.alwaysRaceUpdates = true

		err := func() error {
			_, err := CommitToBranch(context.Background(), CommitToBranchOptions{
				Client:      github,
				Repo:        testRepo,
				MaxAttempts: 3,
				BuildCommit: func(context.Context, *BranchTip) (string, error) {
					return github.seedDetachedCommit(nil, "racy"), nil
				},
			})

			return err
		}()
		if err == nil || !strings.Contains(err.Error(), "after 3 attempts") {
			t.Fatalf("CommitToBranch() error = %v, want retry exhaustion", err)
		}

		if github.calls.updateRef != 3 {
			t.Fatalf("updateRef calls = %d, want 3", github.calls.updateRef)
		}
	})

	t.Run("propagates non race write errors", func(t *testing.T) {
		t.Parallel()

		github := newFakeGitHub()
		github.seedScreenshotsBranch([]TreeEntry{blobEntry("pr-1/braw/x.png")})
		github.updateRefErr = &StatusError{Status: 500, Message: "dreich"}

		_, err := CommitToBranch(context.Background(), CommitToBranchOptions{
			Client: github,
			Repo:   testRepo,
			BuildCommit: func(context.Context, *BranchTip) (string, error) {
				return github.seedDetachedCommit(nil, "commit"), nil
			},
		})
		if err == nil || !strings.Contains(err.Error(), "500") {
			t.Fatalf("CommitToBranch() error = %v, want HTTP 500", err)
		}
	})

	t.Run("propagates build errors without retrying", func(t *testing.T) {
		t.Parallel()

		github := newFakeGitHub()
		github.seedScreenshotsBranch([]TreeEntry{blobEntry("pr-1/braw/x.png")})

		builds := 0

		_, err := CommitToBranch(context.Background(), CommitToBranchOptions{
			Client:      github,
			Repo:        testRepo,
			MaxAttempts: 5,
			BuildCommit: func(context.Context, *BranchTip) (string, error) {
				builds++
				return "", errors.New("tree is empty")
			},
		})
		if err == nil || !strings.Contains(err.Error(), "tree is empty") {
			t.Fatalf("CommitToBranch() error = %v, want build error", err)
		}

		if builds != 1 || github.calls.updateRef != 0 {
			t.Fatalf("builds = %d updateRef = %d, want one build and no update", builds, github.calls.updateRef)
		}
	})
}

func TestCleanup(t *testing.T) {
	t.Parallel()

	t.Run("refuses to rewrite truncated tree", func(t *testing.T) {
		t.Parallel()

		github := newFakeGitHub()
		commit := github.seedScreenshotsBranch([]TreeEntry{blobEntry("pr-42/a/x.png"), blobEntry("pr-7/b/y.png")})
		github.truncatedTrees[github.commits[commit].TreeSHA] = true

		err := Cleanup(context.Background(), CleanupOptions{
			Client: github,
			Repo:   testRepo,
			Event:  prEvent(42, "clachan/croft"),
		})
		if err == nil || !strings.Contains(err.Error(), "truncated") {
			t.Fatalf("Cleanup() error = %v, want truncated rejection", err)
		}

		if github.calls.createTree != 0 || github.calls.updateRef != 0 {
			t.Fatalf("destructive calls after truncated tree: %+v", github.calls)
		}
	})

	t.Run("drops only requested PR prefix and updates sticky comment", func(t *testing.T) {
		t.Parallel()

		github := newFakeGitHub()
		github.seedScreenshotsBranch([]TreeEntry{
			blobEntry("pr-42/a/x.png"),
			blobEntry("pr-42/a/y.png"),
			blobEntry("pr-7/b/z.png"),
		})
		github.comments[42] = []Comment{{ID: 99, Body: StickyMarker + "\nold body"}}

		err := Cleanup(context.Background(), CleanupOptions{
			Client: github,
			Repo:   testRepo,
			Event:  prEvent(42, "clachan/croft"),
		})
		if err != nil {
			t.Fatal(err)
		}

		entries := github.treeAtRef(t, "heads/"+ScreenshotsBranch)
		if got := entryPaths(entries); !equalStrings(got, []string{"pr-7/b/z.png"}) {
			t.Fatalf("tree paths = %v, want only pr-7", got)
		}

		if github.calls.createTreeBaseTrees[0] != "" {
			t.Fatalf("cleanup createTree base_tree = %q, want omitted", github.calls.createTreeBaseTrees[0])
		}

		if got := github.comments[42][0].Body; !strings.Contains(got, "cleaned up after it was closed") {
			t.Fatalf("sticky comment body = %q, want cleanup note", got)
		}
	})

	t.Run("updates session navigator sticky comment", func(t *testing.T) {
		t.Parallel()

		github := newFakeGitHub()
		github.seedScreenshotsBranch([]TreeEntry{
			blobEntry("pr-42/session/nav.png"),
			blobEntry("pr-7/docs/keep.png"),
		})
		github.comments[42] = []Comment{
			{ID: 99, Body: SessionNavigatorStickyMarker + "\nold navigator body"},
			{ID: 100, Body: StickyMarker + "\nold docs body"},
		}

		err := Cleanup(context.Background(), CleanupOptions{
			Client: github,
			Repo:   testRepo,
			Event:  prEvent(42, "clachan/croft"),
			Suite:  SessionNavigatorPreviewSuite(),
		})
		if err != nil {
			t.Fatal(err)
		}

		if got := entryPaths(github.treeAtRef(t, "heads/"+ScreenshotsBranch)); !equalStrings(got, []string{"pr-7/docs/keep.png"}) {
			t.Fatalf("tree paths = %v, want only pr-7", got)
		}

		if got := github.comments[42][0].Body; !strings.Contains(got, "TUI preview") || !strings.Contains(got, "cleaned up after it was closed") {
			t.Fatalf("navigator sticky comment body = %q, want cleanup note", got)
		}

		if got := github.comments[42][1].Body; got != StickyMarker+"\nold docs body" {
			t.Fatalf("docs sticky comment was changed: %q", got)
		}
	})

	t.Run("cleanup ignores quoted sticky marker", func(t *testing.T) {
		t.Parallel()

		github := newFakeGitHub()
		humanBody := "quoted previous bot output\n" + SessionNavigatorStickyMarker + "\nold navigator body"
		github.comments[42] = []Comment{{ID: 99, Body: humanBody}}

		err := Cleanup(context.Background(), CleanupOptions{
			Client: github,
			Repo:   testRepo,
			Event:  prEvent(42, "clachan/croft"),
			Suite:  SessionNavigatorPreviewSuite(),
		})
		if err != nil {
			t.Fatal(err)
		}

		if github.calls.updateComment != 0 || github.calls.deleteComment != 0 {
			t.Fatalf("comment writes = %+v, want none for quoted marker", github.calls)
		}

		if len(github.comments[42]) != 1 || github.comments[42][0].Body != humanBody {
			t.Fatalf("comments = %+v, want quoted marker comment left intact", github.comments[42])
		}
	})

	t.Run("cleanup removes duplicate sticky comments", func(t *testing.T) {
		t.Parallel()

		github := newFakeGitHub()
		github.comments[42] = []Comment{
			{ID: 99, Body: SessionNavigatorStickyMarker + "\nold navigator body"},
			{ID: 100, Body: SessionNavigatorStickyMarker + "\nduplicate navigator body"},
			{ID: 101, Body: StickyMarker + "\nold docs body"},
		}

		err := Cleanup(context.Background(), CleanupOptions{
			Client: github,
			Repo:   testRepo,
			Event:  prEvent(42, "clachan/croft"),
			Suite:  SessionNavigatorPreviewSuite(),
		})
		if err != nil {
			t.Fatal(err)
		}

		if github.calls.updateComment != 1 || github.calls.deleteComment != 1 || github.calls.createComment != 0 {
			t.Fatalf("comment writes = %+v, want one update and one delete", github.calls)
		}

		if len(github.comments[42]) != 2 {
			t.Fatalf("comments = %+v, want one cleanup comment plus docs sticky", github.comments[42])
		}

		if got := github.comments[42][0].Body; !strings.Contains(got, "TUI preview") || !strings.Contains(got, "cleaned up after it was closed") {
			t.Fatalf("navigator sticky comment body = %q, want cleanup note", got)
		}

		if got := github.comments[42][1].Body; got != StickyMarker+"\nold docs body" {
			t.Fatalf("docs sticky comment was changed: %q", got)
		}
	})

	t.Run("updates sticky comment when screenshots were already removed", func(t *testing.T) {
		t.Parallel()

		github := newFakeGitHub()
		github.seedScreenshotsBranch([]TreeEntry{blobEntry("pr-7/docs/keep.png")})
		github.comments[42] = []Comment{{ID: 99, Body: SessionNavigatorStickyMarker + "\nold navigator body"}}

		err := Cleanup(context.Background(), CleanupOptions{
			Client: github,
			Repo:   testRepo,
			Event:  prEvent(42, "clachan/croft"),
			Suite:  SessionNavigatorPreviewSuite(),
		})
		if err != nil {
			t.Fatal(err)
		}

		if github.calls.updateRef != 0 || github.calls.createTree != 0 {
			t.Fatalf("branch writes = %+v, want none", github.calls)
		}

		if got := github.comments[42][0].Body; !strings.Contains(got, "TUI preview") || !strings.Contains(got, "cleaned up after it was closed") {
			t.Fatalf("navigator sticky comment body = %q, want cleanup note", got)
		}
	})

	t.Run("noops when branch is absent or PR has no screenshots", func(t *testing.T) {
		t.Parallel()

		tests := map[string]func(*fakeGitHub){
			"braw absent branch": func(*fakeGitHub) {},
			"canny no matching prefix": func(github *fakeGitHub) {
				github.seedScreenshotsBranch([]TreeEntry{blobEntry("pr-7/b/z.png")})
			},
		}

		for name, setup := range tests {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				github := newFakeGitHub()
				setup(github)

				err := Cleanup(context.Background(), CleanupOptions{
					Client: github,
					Repo:   testRepo,
					Event:  prEvent(42, "clachan/croft"),
				})
				if err != nil {
					t.Fatal(err)
				}

				if github.calls.createTree != 0 || github.calls.updateRef != 0 {
					t.Fatalf("destructive calls = %+v, want none", github.calls)
				}
			})
		}
	})

	t.Run("skips fork before reading or writing the branch", func(t *testing.T) {
		t.Parallel()

		github := newFakeGitHub()
		github.seedScreenshotsBranch([]TreeEntry{blobEntry("pr-42/a/x.png")})

		err := Cleanup(context.Background(), CleanupOptions{
			Client: github,
			Repo:   testRepo,
			Event:  prEvent(42, "thrawn/croft"),
		})
		if err != nil {
			t.Fatal(err)
		}

		if github.calls.getRef != 0 || github.calls.getTree != 0 || github.calls.updateRef != 0 {
			t.Fatalf("calls = %+v, want no GitHub reads/writes for fork", github.calls)
		}
	})

	t.Run("uses empty tree when closing PR was the last one", func(t *testing.T) {
		t.Parallel()

		github := newFakeGitHub()
		github.seedScreenshotsBranch([]TreeEntry{blobEntry("pr-42/a/x.png"), blobEntry("pr-42/a/y.png")})

		err := Cleanup(context.Background(), CleanupOptions{
			Client: github,
			Repo:   testRepo,
			Event:  prEvent(42, "clachan/croft"),
		})
		if err != nil {
			t.Fatal(err)
		}

		commit := github.commits[github.refs["heads/"+ScreenshotsBranch]]
		if commit.TreeSHA != EmptyTreeSHA {
			t.Fatalf("commit tree = %s, want empty tree", commit.TreeSHA)
		}

		if github.calls.createTree != 0 {
			t.Fatalf("createTree calls = %d, want none for empty tree", github.calls.createTree)
		}
	})

	t.Run("retries cleanup on raced ref and preserves concurrent additions", func(t *testing.T) {
		t.Parallel()

		github := newFakeGitHub()
		github.seedScreenshotsBranch([]TreeEntry{
			blobEntry("pr-42/a/x.png"),
			blobEntry("pr-7/b/z.png"),
		})
		winner := github.seedDetachedCommit([]TreeEntry{
			blobEntry("pr-42/a/x.png"),
			blobEntry("pr-7/b/z.png"),
			blobEntry("pr-99/c/keep.png"),
		}, "winner")
		github.beforeUpdateRef = func() {
			github.refs["heads/"+ScreenshotsBranch] = winner
			github.beforeUpdateRef = nil
		}

		err := Cleanup(context.Background(), CleanupOptions{
			Client: github,
			Repo:   testRepo,
			Event:  prEvent(42, "clachan/croft"),
		})
		if err != nil {
			t.Fatal(err)
		}

		if got := entryPaths(github.treeAtRef(t, "heads/"+ScreenshotsBranch)); !equalStrings(got, []string{"pr-7/b/z.png", "pr-99/c/keep.png"}) {
			t.Fatalf("tree paths after retry = %v, want concurrent pr-99 preserved", got)
		}
	})
}

func TestPrune(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 7, 0, 0, 0, 0, time.UTC)

	t.Run("refuses truncated tree", func(t *testing.T) {
		t.Parallel()

		github := newFakeGitHub()
		commit := github.seedScreenshotsBranch([]TreeEntry{
			blobEntry("pr-1/20200101-abc-1.1/x.png"),
			blobEntry("pr-2/29990101-def-1.1/y.png"),
		})
		github.truncatedTrees[github.commits[commit].TreeSHA] = true

		err := Prune(context.Background(), PruneOptions{Client: github, Repo: testRepo, Now: now})
		if err == nil || !strings.Contains(err.Error(), "truncated") {
			t.Fatalf("Prune() error = %v, want truncated rejection", err)
		}

		if github.calls.createTree != 0 || github.calls.updateRef != 0 {
			t.Fatalf("destructive calls after truncated tree: %+v", github.calls)
		}
	})

	t.Run("drops only stale dirs", func(t *testing.T) {
		t.Parallel()

		github := newFakeGitHub()
		github.seedScreenshotsBranch([]TreeEntry{
			blobEntry("pr-1/20200101-abc-1.1/x.png"),
			blobEntry("pr-2/29990101-def-1.1/y.png"),
		})

		if err := Prune(context.Background(), PruneOptions{Client: github, Repo: testRepo, Now: now}); err != nil {
			t.Fatal(err)
		}

		if got := entryPaths(github.treeAtRef(t, "heads/"+ScreenshotsBranch)); !equalStrings(got, []string{"pr-2/29990101-def-1.1/y.png"}) {
			t.Fatalf("tree paths = %v, want only future run", got)
		}

		if github.calls.createTreeBaseTrees[0] != "" {
			t.Fatalf("prune createTree base_tree = %q, want omitted", github.calls.createTreeBaseTrees[0])
		}
	})

	t.Run("noops when nothing is stale", func(t *testing.T) {
		t.Parallel()

		github := newFakeGitHub()
		github.seedScreenshotsBranch([]TreeEntry{blobEntry("pr-2/29990101-def-1.1/y.png")})

		if err := Prune(context.Background(), PruneOptions{Client: github, Repo: testRepo, Now: now}); err != nil {
			t.Fatal(err)
		}

		if github.calls.createTree != 0 || github.calls.updateRef != 0 {
			t.Fatalf("destructive calls = %+v, want none", github.calls)
		}
	})

	t.Run("uses empty tree when every run is stale", func(t *testing.T) {
		t.Parallel()

		github := newFakeGitHub()
		github.seedScreenshotsBranch([]TreeEntry{
			blobEntry("pr-1/20200101-abc-1.1/x.png"),
			blobEntry("pr-2/20200102-def-1.1/y.png"),
		})

		if err := Prune(context.Background(), PruneOptions{Client: github, Repo: testRepo, Now: now}); err != nil {
			t.Fatal(err)
		}

		commit := github.commits[github.refs["heads/"+ScreenshotsBranch]]
		if commit.TreeSHA != EmptyTreeSHA {
			t.Fatalf("commit tree = %s, want empty tree", commit.TreeSHA)
		}

		if github.calls.createTree != 0 {
			t.Fatalf("createTree calls = %d, want none for empty tree", github.calls.createTree)
		}
	})
}

func TestPublish(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)

	t.Run("skips fork before reading manifest or writing GitHub", func(t *testing.T) {
		t.Parallel()

		github := newFakeGitHub()

		err := Publish(context.Background(), PublishOptions{
			Client:       github,
			Repo:         testRepo,
			Event:        prEvent(42, "thrawn/croft"),
			ManifestPath: filepath.Join(t.TempDir(), "missing.json"),
			SHA:          "abcdef123456",
			RunID:        "81",
			Now:          now,
		})
		if err != nil {
			t.Fatal(err)
		}

		if github.calls.totalWrites() != 0 || github.calls.getRef != 0 {
			t.Fatalf("calls = %+v, want complete fork no-op", github.calls)
		}
	})

	t.Run("creates screenshots branch and sticky comment", func(t *testing.T) {
		t.Parallel()

		github := newFakeGitHub()
		assetsDir, manifestPath := writePreviewAssets(t, PreviewManifest{
			"braw-page": {
				"desktop": {Kind: "diff", File: "braw-desktop.png"},
				"mobile":  {Kind: "same"},
			},
			"deleted-page": {
				"desktop": {Kind: "deleted", File: "deleted-desktop.png"},
			},
		}, map[string]string{
			"braw-desktop.png":    "image-bytes",
			"deleted-desktop.png": "deleted-image",
		})

		err := Publish(context.Background(), PublishOptions{
			Client:       github,
			Repo:         testRepo,
			Event:        prEvent(42, "clachan/croft"),
			ManifestPath: manifestPath,
			AssetsDir:    assetsDir,
			SHA:          "abcdef123456",
			RunID:        "81",
			RunAttempt:   "2",
			Now:          now,
		})
		if err != nil {
			t.Fatal(err)
		}

		wantPaths := []string{
			"pr-42/20260726-abcdef1-81.2/braw-desktop.png",
			"pr-42/20260726-abcdef1-81.2/deleted-desktop.png",
		}
		if got := entryPaths(github.treeAtRef(t, "heads/"+ScreenshotsBranch)); !equalStrings(got, wantPaths) {
			t.Fatalf("tree paths = %v, want %v", got, wantPaths)
		}

		if len(github.comments[42]) != 1 {
			t.Fatalf("comments = %+v, want one sticky comment", github.comments[42])
		}

		body := github.comments[42][0].Body
		for _, want := range []string{
			StickyMarker,
			"braw-page",
			"deleted-page</b> \u2014 removed",
			"Pages removed in this PR are shown as their last render on `main`",
			"https://raw.githubusercontent.com/clachan/croft/screenshots/" + wantPaths[0],
			`<a href="https://raw.githubusercontent.com/clachan/croft/screenshots/` + wantPaths[0],
			"_Screenshots stored on the [`screenshots`]",
		} {
			if !strings.Contains(body, want) {
				t.Fatalf("comment body missing %q:\n%s", want, body)
			}
		}

		commit := github.commits[github.refs["heads/"+ScreenshotsBranch]]
		if len(commit.Parents) != 0 {
			t.Fatalf("root publish parents = %v, want none", commit.Parents)
		}
	})

	t.Run("creates session navigator sticky comment", func(t *testing.T) {
		t.Parallel()

		github := newFakeGitHub()
		assetsDir, manifestPath := writePreviewAssets(t, PreviewManifest{
			"session-navigator-all": {
				"small":  {Kind: "diff", File: "session-navigator-all-small.png"},
				"normal": {Kind: "same"},
				"wide":   {Kind: "new", File: "session-navigator-all-wide.png"},
			},
		}, map[string]string{
			"session-navigator-all-small.png": "small-image",
			"session-navigator-all-wide.png":  "wide-image",
		})

		err := Publish(context.Background(), PublishOptions{
			Client:       github,
			Repo:         testRepo,
			Event:        prEvent(42, "clachan/croft"),
			ManifestPath: manifestPath,
			AssetsDir:    assetsDir,
			SHA:          "abcdef123456",
			RunID:        "81",
			RunAttempt:   "2",
			Now:          now,
			Suite:        SessionNavigatorPreviewSuite(),
		})
		if err != nil {
			t.Fatal(err)
		}

		body := github.comments[42][0].Body
		for _, want := range []string{
			SessionNavigatorStickyMarker,
			"TUI preview",
			"Rendered 1 TUI scene(s)",
			"session-navigator-all",
			"| Small | Normal | Wide |",
			"_Snapshots new in this PR",
			"https://raw.githubusercontent.com/clachan/croft/screenshots/pr-42/20260726-abcdef1-81.2/session-navigator-all-small.png",
			`<a href="https://raw.githubusercontent.com/clachan/croft/screenshots/pr-42/20260726-abcdef1-81.2/session-navigator-all-small.png"`,
			"https://raw.githubusercontent.com/clachan/croft/screenshots/pr-42/20260726-abcdef1-81.2/session-navigator-all-wide.png",
		} {
			if !strings.Contains(body, want) {
				t.Fatalf("comment body missing %q:\n%s", want, body)
			}
		}

		if strings.Contains(body, StickyMarker) {
			t.Fatalf("session navigator comment should not use docs marker:\n%s", body)
		}

		commit := github.commits[github.refs["heads/"+ScreenshotsBranch]]
		if !strings.Contains(commit.Message, "tui preview: PR #42 @ abcdef1") {
			t.Fatalf("commit message = %q, want TUI prefix", commit.Message)
		}
	})

	t.Run("appends to existing branch and updates sticky comment", func(t *testing.T) {
		t.Parallel()

		github := newFakeGitHub()
		github.seedScreenshotsBranch([]TreeEntry{blobEntry("pr-7/old/z.png")})
		github.comments[42] = []Comment{{ID: 44, Body: StickyMarker + "\nold"}}
		assetsDir, manifestPath := writePreviewAssets(t, PreviewManifest{
			"canny-page": {
				"desktop": {Kind: "new", File: "canny-desktop.png"},
			},
		}, map[string]string{"canny-desktop.png": "image-bytes"})

		err := Publish(context.Background(), PublishOptions{
			Client:       github,
			Repo:         testRepo,
			Event:        prEvent(42, "clachan/croft"),
			ManifestPath: manifestPath,
			AssetsDir:    assetsDir,
			SHA:          "1234567890",
			RunID:        "82",
			Now:          now,
		})
		if err != nil {
			t.Fatal(err)
		}

		wantPath := "pr-42/20260726-1234567-82.1/canny-desktop.png"
		if got := entryPaths(github.treeAtRef(t, "heads/"+ScreenshotsBranch)); !equalStrings(got, []string{wantPath, "pr-7/old/z.png"}) {
			t.Fatalf("tree paths = %v, want old screenshot plus new", got)
		}

		if got := github.comments[42][0].ID; got != 44 {
			t.Fatalf("comment ID = %d, want update of existing sticky comment", got)
		}

		if !strings.Contains(github.comments[42][0].Body, "canny-page") {
			t.Fatalf("updated comment body = %q, want new page", github.comments[42][0].Body)
		}

		if got := github.calls.createTreeBaseTrees[len(github.calls.createTreeBaseTrees)-1]; got == "" {
			t.Fatal("publish append omitted base_tree")
		}
	})

	t.Run("diff manifest updates first sticky comment and deletes duplicates", func(t *testing.T) {
		t.Parallel()

		github := newFakeGitHub()
		github.seedScreenshotsBranch([]TreeEntry{blobEntry("pr-7/old/z.png")})
		github.comments[42] = []Comment{
			{ID: 44, Body: StickyMarker + "\nold"},
			{ID: 45, Body: StickyMarker + "\nduplicate"},
		}
		assetsDir, manifestPath := writePreviewAssets(t, PreviewManifest{
			"canny-page": {
				"desktop": {Kind: "new", File: "canny-desktop.png"},
			},
		}, map[string]string{"canny-desktop.png": "image-bytes"})

		err := Publish(context.Background(), PublishOptions{
			Client:       github,
			Repo:         testRepo,
			Event:        prEvent(42, "clachan/croft"),
			ManifestPath: manifestPath,
			AssetsDir:    assetsDir,
			SHA:          "1234567890",
			RunID:        "82",
			Now:          now,
		})
		if err != nil {
			t.Fatal(err)
		}

		if github.calls.updateComment != 1 || github.calls.deleteComment != 1 || github.calls.createComment != 0 {
			t.Fatalf("comment writes = %+v, want one update and one delete", github.calls)
		}

		if len(github.comments[42]) != 1 {
			t.Fatalf("comments = %+v, want duplicate sticky comment deleted", github.comments[42])
		}

		if got := github.comments[42][0]; got.ID != 44 || !strings.Contains(got.Body, "canny-page") {
			t.Fatalf("updated comment = %+v, want first sticky updated with new page", got)
		}
	})

	t.Run("same-only manifest noops without existing sticky comment", func(t *testing.T) {
		t.Parallel()

		github := newFakeGitHub()
		assetsDir, manifestPath := writePreviewAssets(t, PreviewManifest{
			"dreich-page": {
				"desktop": {Kind: "same"},
				"mobile":  {Kind: "same"},
			},
		}, nil)

		err := Publish(context.Background(), PublishOptions{
			Client:       github,
			Repo:         testRepo,
			Event:        prEvent(42, "clachan/croft"),
			ManifestPath: manifestPath,
			AssetsDir:    assetsDir,
			SHA:          "abcdef123456",
			RunID:        "83",
			Now:          now,
		})
		if err != nil {
			t.Fatal(err)
		}

		if github.calls.createBlob != 0 || github.calls.createTree != 0 || github.calls.createRef != 0 {
			t.Fatalf("branch writes = %+v, want none for same-only manifest", github.calls)
		}

		if len(github.comments[42]) != 0 {
			t.Fatalf("comments = %+v, want no new sticky comment for same-only manifest", github.comments[42])
		}
	})

	t.Run("same-only session navigator manifest noops without existing sticky comment", func(t *testing.T) {
		t.Parallel()

		github := newFakeGitHub()
		assetsDir, manifestPath := writePreviewAssets(t, PreviewManifest{
			"session-navigator-all": {
				"small":  {Kind: "same"},
				"normal": {Kind: "same"},
				"wide":   {Kind: "same"},
			},
		}, nil)

		err := Publish(context.Background(), PublishOptions{
			Client:       github,
			Repo:         testRepo,
			Event:        prEvent(42, "clachan/croft"),
			ManifestPath: manifestPath,
			AssetsDir:    assetsDir,
			SHA:          "abcdef123456",
			RunID:        "83",
			Now:          now,
			Suite:        SessionNavigatorPreviewSuite(),
		})
		if err != nil {
			t.Fatal(err)
		}

		if github.calls.createBlob != 0 || github.calls.createTree != 0 || github.calls.createRef != 0 {
			t.Fatalf("branch writes = %+v, want none for same-only session navigator manifest", github.calls)
		}

		if len(github.comments[42]) != 0 {
			t.Fatalf("comments = %+v, want no new sticky comment for same-only session navigator manifest", github.comments[42])
		}
	})

	t.Run("empty session navigator manifest noops without existing sticky comment", func(t *testing.T) {
		t.Parallel()

		github := newFakeGitHub()
		assetsDir, manifestPath := writePreviewAssets(t, PreviewManifest{}, nil)

		err := Publish(context.Background(), PublishOptions{
			Client:       github,
			Repo:         testRepo,
			Event:        prEvent(42, "clachan/croft"),
			ManifestPath: manifestPath,
			AssetsDir:    assetsDir,
			SHA:          "abcdef123456",
			RunID:        "83",
			Now:          now,
			Suite:        SessionNavigatorPreviewSuite(),
		})
		if err != nil {
			t.Fatal(err)
		}

		if github.calls.createBlob != 0 || github.calls.createTree != 0 || github.calls.createRef != 0 || github.calls.createComment != 0 {
			t.Fatalf("writes = %+v, want none for empty session navigator manifest", github.calls)
		}

		if len(github.comments[42]) != 0 {
			t.Fatalf("comments = %+v, want no new sticky comment for empty session navigator manifest", github.comments[42])
		}
	})

	t.Run("empty manifest deletes existing sticky comment", func(t *testing.T) {
		t.Parallel()

		github := newFakeGitHub()
		github.comments[42] = []Comment{{ID: 44, Body: StickyMarker + "\nold"}}
		assetsDir, manifestPath := writePreviewAssets(t, PreviewManifest{}, nil)

		err := Publish(context.Background(), PublishOptions{
			Client:       github,
			Repo:         testRepo,
			Event:        prEvent(42, "clachan/croft"),
			ManifestPath: manifestPath,
			AssetsDir:    assetsDir,
			SHA:          "abcdef123456",
			RunID:        "83",
			Now:          now,
		})
		if err != nil {
			t.Fatal(err)
		}

		if github.calls.createBlob != 0 || github.calls.createTree != 0 || github.calls.createRef != 0 {
			t.Fatalf("branch writes = %+v, want none for empty manifest", github.calls)
		}

		if github.calls.deleteComment != 1 || github.calls.updateComment != 0 || github.calls.createComment != 0 {
			t.Fatalf("comment writes = %+v, want one delete only", github.calls)
		}

		if len(github.comments[42]) != 0 {
			t.Fatalf("comments = %+v, want existing sticky comment deleted", github.comments[42])
		}
	})

	t.Run("same-only manifest deletes existing sticky comment without branch write", func(t *testing.T) {
		t.Parallel()

		github := newFakeGitHub()
		github.comments[42] = []Comment{{ID: 44, Body: StickyMarker + "\nold"}}
		assetsDir, manifestPath := writePreviewAssets(t, PreviewManifest{
			"dreich-page": {
				"desktop": {Kind: "same"},
				"mobile":  {Kind: "same"},
			},
		}, nil)

		err := Publish(context.Background(), PublishOptions{
			Client:       github,
			Repo:         testRepo,
			Event:        prEvent(42, "clachan/croft"),
			ManifestPath: manifestPath,
			AssetsDir:    assetsDir,
			SHA:          "abcdef123456",
			RunID:        "83",
			Now:          now,
		})
		if err != nil {
			t.Fatal(err)
		}

		if github.calls.createBlob != 0 || github.calls.createTree != 0 || github.calls.createRef != 0 {
			t.Fatalf("branch writes = %+v, want none for same-only manifest", github.calls)
		}

		if github.calls.deleteComment != 1 || github.calls.updateComment != 0 || github.calls.createComment != 0 {
			t.Fatalf("comment writes = %+v, want one delete only", github.calls)
		}

		if len(github.comments[42]) != 0 {
			t.Fatalf("comments = %+v, want existing sticky comment deleted", github.comments[42])
		}
	})

	t.Run("same-only manifest deletes duplicate sticky comments", func(t *testing.T) {
		t.Parallel()

		github := newFakeGitHub()
		github.comments[42] = []Comment{
			{ID: 44, Body: StickyMarker + "\nold"},
			{ID: 45, Body: StickyMarker + "\nolder"},
		}
		assetsDir, manifestPath := writePreviewAssets(t, PreviewManifest{
			"dreich-page": {
				"desktop": {Kind: "same"},
				"mobile":  {Kind: "same"},
			},
		}, nil)

		err := Publish(context.Background(), PublishOptions{
			Client:       github,
			Repo:         testRepo,
			Event:        prEvent(42, "clachan/croft"),
			ManifestPath: manifestPath,
			AssetsDir:    assetsDir,
			SHA:          "abcdef123456",
			RunID:        "83",
			Now:          now,
		})
		if err != nil {
			t.Fatal(err)
		}

		if github.calls.deleteComment != 2 || github.calls.updateComment != 0 || github.calls.createComment != 0 {
			t.Fatalf("comment writes = %+v, want two deletes only", github.calls)
		}

		if len(github.comments[42]) != 0 {
			t.Fatalf("comments = %+v, want duplicate sticky comments deleted", github.comments[42])
		}
	})

	t.Run("same-only manifest does not delete quoted sticky marker", func(t *testing.T) {
		t.Parallel()

		github := newFakeGitHub()
		humanBody := "quoted previous bot output\n" + StickyMarker + "\nold"
		github.comments[42] = []Comment{{ID: 44, Body: humanBody}}
		assetsDir, manifestPath := writePreviewAssets(t, PreviewManifest{
			"dreich-page": {
				"desktop": {Kind: "same"},
				"mobile":  {Kind: "same"},
			},
		}, nil)

		err := Publish(context.Background(), PublishOptions{
			Client:       github,
			Repo:         testRepo,
			Event:        prEvent(42, "clachan/croft"),
			ManifestPath: manifestPath,
			AssetsDir:    assetsDir,
			SHA:          "abcdef123456",
			RunID:        "83",
			Now:          now,
		})
		if err != nil {
			t.Fatal(err)
		}

		if github.calls.deleteComment != 0 || github.calls.updateComment != 0 || github.calls.createComment != 0 {
			t.Fatalf("comment writes = %+v, want none for quoted marker", github.calls)
		}

		if len(github.comments[42]) != 1 || github.comments[42][0].Body != humanBody {
			t.Fatalf("comments = %+v, want quoted marker comment left intact", github.comments[42])
		}
	})

	t.Run("same-only manifest tolerates already deleted sticky comment", func(t *testing.T) {
		t.Parallel()

		github := newFakeGitHub()
		github.comments[42] = []Comment{{ID: 44, Body: StickyMarker + "\nold"}}
		github.deleteCommentErr = &StatusError{Status: 404, Message: "Not Found"}
		assetsDir, manifestPath := writePreviewAssets(t, PreviewManifest{
			"dreich-page": {
				"desktop": {Kind: "same"},
				"mobile":  {Kind: "same"},
			},
		}, nil)

		err := Publish(context.Background(), PublishOptions{
			Client:       github,
			Repo:         testRepo,
			Event:        prEvent(42, "clachan/croft"),
			ManifestPath: manifestPath,
			AssetsDir:    assetsDir,
			SHA:          "abcdef123456",
			RunID:        "83",
			Now:          now,
		})
		if err != nil {
			t.Fatal(err)
		}

		if github.calls.deleteComment != 1 || github.calls.updateComment != 0 || github.calls.createComment != 0 {
			t.Fatalf("comment writes = %+v, want one delete only", github.calls)
		}
	})

	t.Run("same-only manifest propagates sticky comment delete failure", func(t *testing.T) {
		t.Parallel()

		github := newFakeGitHub()
		github.comments[42] = []Comment{{ID: 44, Body: StickyMarker + "\nold"}}
		github.deleteCommentErr = &StatusError{Status: 500, Message: "dreich"}
		assetsDir, manifestPath := writePreviewAssets(t, PreviewManifest{
			"dreich-page": {
				"desktop": {Kind: "same"},
				"mobile":  {Kind: "same"},
			},
		}, nil)

		err := Publish(context.Background(), PublishOptions{
			Client:       github,
			Repo:         testRepo,
			Event:        prEvent(42, "clachan/croft"),
			ManifestPath: manifestPath,
			AssetsDir:    assetsDir,
			SHA:          "abcdef123456",
			RunID:        "83",
			Now:          now,
		})
		if err == nil || !strings.Contains(err.Error(), "dreich") {
			t.Fatalf("Publish() error = %v, want delete failure", err)
		}

		if github.calls.deleteComment != 1 || github.calls.updateComment != 0 || github.calls.createComment != 0 {
			t.Fatalf("comment writes = %+v, want one delete only", github.calls)
		}
	})

	t.Run("same-only session navigator manifest deletes existing sticky comment without branch write", func(t *testing.T) {
		t.Parallel()

		github := newFakeGitHub()
		github.comments[42] = []Comment{{ID: 44, Body: SessionNavigatorStickyMarker + "\nold navigator body"}}
		assetsDir, manifestPath := writePreviewAssets(t, PreviewManifest{
			"session-navigator-repo": {
				"small":  {Kind: "same"},
				"normal": {Kind: "same"},
				"wide":   {Kind: "same"},
			},
		}, nil)

		err := Publish(context.Background(), PublishOptions{
			Client:       github,
			Repo:         testRepo,
			Event:        prEvent(42, "clachan/croft"),
			ManifestPath: manifestPath,
			AssetsDir:    assetsDir,
			SHA:          "abcdef123456",
			RunID:        "83",
			Now:          now,
			Suite:        SessionNavigatorPreviewSuite(),
		})
		if err != nil {
			t.Fatal(err)
		}

		if github.calls.createBlob != 0 || github.calls.createTree != 0 || github.calls.createRef != 0 || github.calls.createComment != 0 {
			t.Fatalf("branch/create writes = %+v, want only existing comment delete", github.calls)
		}

		if github.calls.deleteComment != 1 || github.calls.updateComment != 0 {
			t.Fatalf("comment writes = %+v, want one delete only", github.calls)
		}

		if len(github.comments[42]) != 0 {
			t.Fatalf("comments = %+v, want existing session navigator sticky comment deleted", github.comments[42])
		}
	})

	t.Run("missing manifest noops", func(t *testing.T) {
		t.Parallel()

		github := newFakeGitHub()

		err := Publish(context.Background(), PublishOptions{
			Client:       github,
			Repo:         testRepo,
			Event:        prEvent(42, "clachan/croft"),
			ManifestPath: filepath.Join(t.TempDir(), "missing.json"),
			SHA:          "abcdef123456",
			RunID:        "84",
			Now:          now,
		})
		if err != nil {
			t.Fatal(err)
		}

		if github.calls.totalWrites() != 0 {
			t.Fatalf("writes = %+v, want none for missing manifest", github.calls)
		}
	})

	t.Run("malformed manifest fails without writing", func(t *testing.T) {
		t.Parallel()

		github := newFakeGitHub()

		manifestPath := filepath.Join(t.TempDir(), "manifest.json")
		if err := os.WriteFile(manifestPath, []byte(`{"braw":`), 0o600); err != nil {
			t.Fatal(err)
		}

		err := Publish(context.Background(), PublishOptions{
			Client:       github,
			Repo:         testRepo,
			Event:        prEvent(42, "clachan/croft"),
			ManifestPath: manifestPath,
			SHA:          "abcdef123456",
			RunID:        "84",
			Now:          now,
		})
		if err == nil || !strings.Contains(err.Error(), "read diff manifest") {
			t.Fatalf("Publish() error = %v, want malformed manifest failure", err)
		}

		if github.calls.totalWrites() != 0 || github.calls.getRef != 0 {
			t.Fatalf("calls = %+v, want no GitHub traffic for malformed manifest", github.calls)
		}
	})

	t.Run("unsafe manifest asset fails without writing", func(t *testing.T) {
		t.Parallel()

		github := newFakeGitHub()
		assetsDir, manifestPath := writePreviewAssets(t, PreviewManifest{
			"dreich-page": {
				"desktop": {Kind: "diff", File: "../dreich.png"},
			},
		}, map[string]string{"dreich.png": "image-bytes"})

		err := Publish(context.Background(), PublishOptions{
			Client:       github,
			Repo:         testRepo,
			Event:        prEvent(42, "clachan/croft"),
			ManifestPath: manifestPath,
			AssetsDir:    assetsDir,
			SHA:          "abcdef123456",
			RunID:        "84",
			Now:          now,
		})
		if err == nil || !strings.Contains(err.Error(), "unsafe asset filename") {
			t.Fatalf("Publish() error = %v, want unsafe asset failure", err)
		}

		if github.calls.totalWrites() != 0 || github.calls.getRef != 0 {
			t.Fatalf("calls = %+v, want no GitHub traffic for unsafe manifest", github.calls)
		}
	})

	t.Run("unsafe manifest metadata fails without writing", func(t *testing.T) {
		t.Parallel()

		tests := map[string]struct {
			manifest PreviewManifest
			files    map[string]string
			wantErr  string
		}{
			"missing changed asset": {
				manifest: PreviewManifest{"dreich-page": {"desktop": {Kind: "diff"}}},
				wantErr:  "missing asset filename",
			},
			"same entry with asset": {
				manifest: PreviewManifest{"dreich-page": {"desktop": {Kind: "same", File: "dreich.png"}}},
				files:    map[string]string{"dreich.png": "image-bytes"},
				wantErr:  "unexpected asset filename",
			},
			"unsafe page": {
				manifest: PreviewManifest{"../dreich": {"desktop": {Kind: "same"}}},
				wantErr:  "unsafe page name",
			},
			"unsafe viewport": {
				manifest: PreviewManifest{"dreich-page": {"../desktop": {Kind: "same"}}},
				wantErr:  "unsafe viewport label",
			},
			"unknown kind": {
				manifest: PreviewManifest{"dreich-page": {"desktop": {Kind: "changed", File: "dreich.png"}}},
				files:    map[string]string{"dreich.png": "image-bytes"},
				wantErr:  "unknown screenshot kind",
			},
		}

		for name, test := range tests {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				github := newFakeGitHub()
				assetsDir, manifestPath := writePreviewAssets(t, test.manifest, test.files)

				err := Publish(context.Background(), PublishOptions{
					Client:       github,
					Repo:         testRepo,
					Event:        prEvent(42, "clachan/croft"),
					ManifestPath: manifestPath,
					AssetsDir:    assetsDir,
					SHA:          "abcdef123456",
					RunID:        "84",
					Now:          now,
				})
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("Publish() error = %v, want %q", err, test.wantErr)
				}

				if github.calls.totalWrites() != 0 || github.calls.getRef != 0 {
					t.Fatalf("calls = %+v, want no GitHub traffic for unsafe manifest", github.calls)
				}
			})
		}
	})

	t.Run("invalid preview asset fails without writing", func(t *testing.T) {
		t.Parallel()

		github := newFakeGitHub()

		assetsDir, manifestPath := writePreviewAssets(t, PreviewManifest{
			"dreich-page": {
				"desktop": {Kind: "diff", File: "dreich.png"},
			},
		}, map[string]string{"dreich.png": "image-bytes"})
		if err := os.WriteFile(filepath.Join(assetsDir, "dreich.png"), []byte("not a png"), 0o600); err != nil {
			t.Fatal(err)
		}

		err := Publish(context.Background(), PublishOptions{
			Client:       github,
			Repo:         testRepo,
			Event:        prEvent(42, "clachan/croft"),
			ManifestPath: manifestPath,
			AssetsDir:    assetsDir,
			SHA:          "abcdef123456",
			RunID:        "84",
			Now:          now,
		})
		if err == nil || !strings.Contains(err.Error(), "not a valid PNG") {
			t.Fatalf("Publish() error = %v, want invalid PNG failure", err)
		}

		if github.calls.totalWrites() != 0 || github.calls.getRef != 0 {
			t.Fatalf("calls = %+v, want no GitHub traffic for invalid preview asset", github.calls)
		}
	})

	t.Run("duplicate manifest asset fails without writing", func(t *testing.T) {
		t.Parallel()

		github := newFakeGitHub()
		assetsDir, manifestPath := writePreviewAssets(t, PreviewManifest{
			"dreich-page": {
				"desktop": {Kind: "diff", File: "dreich.png"},
				"mobile":  {Kind: "diff", File: "dreich.png"},
			},
		}, map[string]string{"dreich.png": "image-bytes"})

		err := Publish(context.Background(), PublishOptions{
			Client:       github,
			Repo:         testRepo,
			Event:        prEvent(42, "clachan/croft"),
			ManifestPath: manifestPath,
			AssetsDir:    assetsDir,
			SHA:          "abcdef123456",
			RunID:        "84",
			Now:          now,
		})
		if err == nil || !strings.Contains(err.Error(), "duplicated") {
			t.Fatalf("Publish() error = %v, want duplicate asset failure", err)
		}

		if github.calls.totalWrites() != 0 || github.calls.getRef != 0 {
			t.Fatalf("calls = %+v, want no GitHub traffic for duplicate manifest", github.calls)
		}
	})

	t.Run("lost create race appends to winner branch", func(t *testing.T) {
		t.Parallel()

		github := newFakeGitHub()
		winner := github.seedDetachedCommit([]TreeEntry{blobEntry("pr-7/winner/z.png")}, "winner")
		github.beforeCreateRef = func() {
			github.refs["heads/"+ScreenshotsBranch] = winner
			github.beforeCreateRef = nil
		}
		assetsDir, manifestPath := writePreviewAssets(t, PreviewManifest{
			"bothy-page": {
				"desktop": {Kind: "diff", File: "bothy.png"},
			},
		}, map[string]string{"bothy.png": "image-bytes"})

		err := Publish(context.Background(), PublishOptions{
			Client:       github,
			Repo:         testRepo,
			Event:        prEvent(42, "clachan/croft"),
			ManifestPath: manifestPath,
			AssetsDir:    assetsDir,
			SHA:          "abcdef123456",
			RunID:        "85",
			Now:          now,
		})
		if err != nil {
			t.Fatal(err)
		}

		if got := entryPaths(github.treeAtRef(t, "heads/"+ScreenshotsBranch)); !equalStrings(got, []string{"pr-42/20260726-abcdef1-85.1/bothy.png", "pr-7/winner/z.png"}) {
			t.Fatalf("tree paths after create race = %v, want winner plus new publish", got)
		}
	})
}

func prEvent(number int, headFullName string) Event {
	return Event{
		Repository: EventRepository{FullName: "clachan/croft"},
		PullRequest: &PullRequest{
			Number: number,
			Head:   PullRef{Repo: &PullRepository{FullName: headFullName}},
			Base:   PullBaseRef{Ref: "main", SHA: "base-sha"},
		},
	}
}

func blobEntry(path string) TreeEntry {
	return TreeEntry{Path: path, Mode: "100644", Type: "blob", SHA: "sha-" + path}
}

func entryPaths(entries []TreeEntry) []string {
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		paths = append(paths, entry.Path)
	}

	sort.Strings(paths)

	return paths
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}

	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}

	return true
}

func writePreviewAssets(t *testing.T, manifest PreviewManifest, files map[string]string) (string, string) {
	t.Helper()

	dir := t.TempDir()

	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}

	manifestPath := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(manifestPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	for name := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(path, previewPNGBytes(t), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	return dir, manifestPath
}

func previewPNGBytes(t *testing.T) []byte {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 0x30, G: 0x36, B: 0x40, A: 0xff})
	img.Set(1, 0, color.RGBA{R: 0xd8, G: 0xde, B: 0xe9, A: 0xff})
	img.Set(0, 1, color.RGBA{R: 0x61, G: 0xaf, B: 0xef, A: 0xff})
	img.Set(1, 1, color.RGBA{R: 0xc6, G: 0x78, B: 0xdd, A: 0xff})

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

type fakeGitHub struct {
	refs           map[string]string
	commits        map[string]Commit
	trees          map[string][]TreeEntry
	truncatedTrees map[string]bool
	blobs          map[string]string
	comments       map[int][]Comment
	nextID         int

	beforeUpdateRef   func()
	beforeCreateRef   func()
	alwaysRaceUpdates bool
	updateRefErr      error
	deleteCommentErr  error
	calls             fakeCalls
}

type fakeCalls struct {
	getRef              int
	getTree             int
	createBlob          int
	createTree          int
	createTreeBaseTrees []string
	createCommit        int
	createRef           int
	updateRef           int
	updateComment       int
	createComment       int
	deleteComment       int
}

func (calls fakeCalls) totalWrites() int {
	return calls.createBlob + calls.createTree + calls.createCommit + calls.createRef +
		calls.updateRef + calls.updateComment + calls.createComment + calls.deleteComment
}

func newFakeGitHub() *fakeGitHub {
	return &fakeGitHub{
		refs:           make(map[string]string),
		commits:        make(map[string]Commit),
		trees:          map[string][]TreeEntry{EmptyTreeSHA: {}},
		truncatedTrees: make(map[string]bool),
		blobs:          make(map[string]string),
		comments:       make(map[int][]Comment),
		nextID:         1,
	}
}

func (github *fakeGitHub) GetRef(_ context.Context, _, _, ref string) (Ref, error) {
	github.calls.getRef++

	sha := github.refs[normalizeRef(ref)]
	if sha == "" {
		return Ref{}, &StatusError{Status: 404, Message: "Not Found"}
	}

	return Ref{SHA: sha}, nil
}

func (github *fakeGitHub) CreateRef(_ context.Context, _, _, ref, sha string) error {
	github.calls.createRef++
	if github.beforeCreateRef != nil {
		github.beforeCreateRef()
	}

	normalized := normalizeRef(ref)
	if github.refs[normalized] != "" {
		return &StatusError{Status: 422, Message: "Reference already exists"}
	}

	if _, ok := github.commits[sha]; !ok {
		return &StatusError{Status: 422, Message: "Commit does not exist"}
	}

	github.refs[normalized] = sha

	return nil
}

func (github *fakeGitHub) UpdateRef(_ context.Context, _, _, ref, sha string) error {
	github.calls.updateRef++
	if github.beforeUpdateRef != nil {
		github.beforeUpdateRef()
	}

	if github.updateRefErr != nil {
		return github.updateRefErr
	}

	normalized := normalizeRef(ref)

	current := github.refs[normalized]
	if current == "" {
		return &StatusError{Status: 404, Message: "Not Found"}
	}

	if github.alwaysRaceUpdates {
		github.refs[normalized] = github.seedDetachedCommit(nil, "raced-"+current, current)
		return &StatusError{Status: 422, Message: "Reference update failed"}
	}

	commit, ok := github.commits[sha]
	if !ok {
		return &StatusError{Status: 422, Message: "Commit does not exist"}
	}

	if !containsString(commit.Parents, current) {
		return &StatusError{Status: 422, Message: "Reference update failed"}
	}

	github.refs[normalized] = sha

	return nil
}

func (github *fakeGitHub) GetCommit(_ context.Context, _, _, sha string) (Commit, error) {
	commit, ok := github.commits[sha]
	if !ok {
		return Commit{}, &StatusError{Status: 404, Message: "Not Found"}
	}

	return commit, nil
}

func (github *fakeGitHub) GetTree(_ context.Context, _, _, sha string, _ bool) (Tree, error) {
	github.calls.getTree++

	entries, ok := github.trees[sha]
	if !ok {
		return Tree{}, &StatusError{Status: 404, Message: "Not Found"}
	}

	return Tree{SHA: sha, Entries: append([]TreeEntry(nil), entries...), Truncated: github.truncatedTrees[sha]}, nil
}

func (github *fakeGitHub) CreateTree(_ context.Context, _, _ string, entries []TreeEntry, baseTree string) (string, error) {
	github.calls.createTree++
	github.calls.createTreeBaseTrees = append(github.calls.createTreeBaseTrees, baseTree)

	if len(entries) == 0 {
		return "", &StatusError{Status: 422, Message: "tree may not be empty"}
	}

	merged := make(map[string]TreeEntry)

	if baseTree != "" {
		baseEntries, ok := github.trees[baseTree]
		if !ok {
			return "", &StatusError{Status: 422, Message: "base tree does not exist"}
		}

		for _, entry := range baseEntries {
			merged[entry.Path] = entry
		}
	}

	for _, entry := range entries {
		merged[entry.Path] = entry
	}

	paths := make([]string, 0, len(merged))
	for path := range merged {
		paths = append(paths, path)
	}

	sort.Strings(paths)

	treeEntries := make([]TreeEntry, 0, len(paths))
	for _, path := range paths {
		treeEntries = append(treeEntries, merged[path])
	}

	sha := github.nextSHA("tree")
	github.trees[sha] = treeEntries

	return sha, nil
}

func (github *fakeGitHub) CreateCommit(_ context.Context, _, _ string, message, tree string, parents []string) (string, error) {
	github.calls.createCommit++
	if _, ok := github.trees[tree]; !ok {
		return "", &StatusError{Status: 422, Message: "tree does not exist"}
	}

	for _, parent := range parents {
		if parent != "" {
			if _, ok := github.commits[parent]; !ok {
				return "", &StatusError{Status: 422, Message: "parent does not exist"}
			}
		}
	}

	sha := github.nextSHA("commit")
	github.commits[sha] = Commit{
		SHA:     sha,
		TreeSHA: tree,
		Parents: append([]string(nil), parents...),
		Message: message,
	}

	return sha, nil
}

func (github *fakeGitHub) CreateBlob(_ context.Context, _, _ string, content, _ string) (string, error) {
	github.calls.createBlob++
	sha := github.nextSHA("blob")
	github.blobs[sha] = content

	return sha, nil
}

func (github *fakeGitHub) ListComments(_ context.Context, _, _ string, issueNumber int) ([]Comment, error) {
	return append([]Comment(nil), github.comments[issueNumber]...), nil
}

func (github *fakeGitHub) UpdateComment(_ context.Context, _, _ string, commentID int64, body string) error {
	github.calls.updateComment++
	for issueNumber, comments := range github.comments {
		for i, comment := range comments {
			if comment.ID == commentID {
				github.comments[issueNumber][i].Body = body
				return nil
			}
		}
	}

	return &StatusError{Status: 404, Message: "Not Found"}
}

func (github *fakeGitHub) CreateComment(_ context.Context, _, _ string, issueNumber int, body string) error {
	github.calls.createComment++
	comment := Comment{ID: int64(github.nextID), Body: body}
	github.nextID++
	github.comments[issueNumber] = append(github.comments[issueNumber], comment)

	return nil
}

func (github *fakeGitHub) DeleteComment(_ context.Context, _, _ string, commentID int64) error {
	github.calls.deleteComment++
	if github.deleteCommentErr != nil {
		return github.deleteCommentErr
	}

	for issueNumber, comments := range github.comments {
		for i, comment := range comments {
			if comment.ID == commentID {
				github.comments[issueNumber] = append(comments[:i], comments[i+1:]...)
				return nil
			}
		}
	}

	return &StatusError{Status: 404, Message: "Not Found"}
}

func (github *fakeGitHub) seedScreenshotsBranch(entries []TreeEntry) string {
	commit := github.seedDetachedCommit(entries, "seed")
	github.refs["heads/"+ScreenshotsBranch] = commit

	return commit
}

func (github *fakeGitHub) seedDetachedCommit(entries []TreeEntry, message string, parents ...string) string {
	treeSHA := EmptyTreeSHA
	if len(entries) > 0 {
		treeSHA = github.nextSHA("tree")

		github.trees[treeSHA] = append([]TreeEntry(nil), entries...)
	}

	commitSHA := github.nextSHA("commit")
	github.commits[commitSHA] = Commit{
		SHA:     commitSHA,
		TreeSHA: treeSHA,
		Parents: compactStrings(parents),
		Message: message,
	}

	return commitSHA
}

func (github *fakeGitHub) treeAtRef(t *testing.T, ref string) []TreeEntry {
	t.Helper()

	commitSHA := github.refs[normalizeRef(ref)]
	if commitSHA == "" {
		t.Fatalf("ref %s does not exist", ref)
	}

	commit := github.commits[commitSHA]

	entries, ok := github.trees[commit.TreeSHA]
	if !ok {
		t.Fatalf("tree %s does not exist", commit.TreeSHA)
	}

	return append([]TreeEntry(nil), entries...)
}

func (github *fakeGitHub) nextSHA(prefix string) string {
	sha := prefix + "-" + string(rune('a'+github.nextID%26)) + "-" + strings.Repeat("0", github.nextID%5)
	github.nextID++

	return sha
}

func normalizeRef(ref string) string {
	return strings.TrimPrefix(ref, "refs/")
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}

	return false
}

func compactStrings(values []string) []string {
	var compacted []string

	for _, value := range values {
		if value != "" {
			compacted = append(compacted, value)
		}
	}

	return compacted
}
