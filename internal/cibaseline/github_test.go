package cibaseline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (function roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

type cancelingReadCloser struct {
	cancel func()
	err    error
}

func (body *cancelingReadCloser) Read([]byte) (int, error) {
	body.cancel()

	return 0, body.err
}

func (*cancelingReadCloser) Close() error {
	return nil
}

func TestGitHubCollectorFetchesReadOnlyEvidence(t *testing.T) {
	requests := make([]string, 0)
	timingGone := false
	timingNegative := false

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests = append(requests, request.Method+" "+request.URL.Path)
		if request.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", request.Method)
		}

		if request.URL.Path == "/repos/d0ugal/graith/actions/runs" && request.URL.Query().Has("status") {
			t.Errorf("run query unexpectedly filters status: %q", request.URL.Query().Get("status"))
		}

		if got := request.Header.Get("Authorization"); got != "Bearer canny-token" {
			t.Errorf("authorization = %q", got)
		}

		writer.Header().Set("Content-Type", "application/json")

		switch request.URL.Path {
		case "/repos/d0ugal/graith/actions/runs":
			writeResponse(t, writer, `{"total_count":1,"workflow_runs":[{"id":81,"run_attempt":1,"path":".github/workflows/ci.yml@refs/heads/canny/braw","event":"pull_request","head_sha":"braw","head_branch":"canny","pull_requests":[{"number":1}],"created_at":"2026-07-25T10:00:00Z","run_started_at":"2026-07-25T10:00:10Z","updated_at":"2026-07-25T10:01:00Z","status":"completed","conclusion":"success"}]}`)
		case "/repos/d0ugal/graith/actions/caches":
			writeResponse(t, writer, `{"total_count":1,"actions_caches":[{"id":91,"key":"croft","ref":"refs/pull/1/merge","size_in_bytes":42,"created_at":"2026-07-25T10:00:00Z","last_accessed_at":"2026-07-25T10:00:30Z","version":"dreich"}]}`)
		case "/repos/d0ugal/graith/actions/runs/81/attempts/1/jobs":
			writeResponse(t, writer, `{"total_count":1,"jobs":[{"id":82,"name":"Test (ubuntu-latest)","status":"completed","conclusion":"success","created_at":"2026-07-25T10:00:00Z","started_at":"2026-07-25T10:00:10Z","completed_at":"2026-07-25T10:01:00Z","runner_name":"bairn","runner_group_name":"GitHub Actions","labels":["ubuntu-latest"]}]}`)
		case "/repos/d0ugal/graith/actions/runs/81/artifacts":
			writeResponse(t, writer, `{"total_count":1,"artifacts":[{"id":83,"name":"bothy","size_in_bytes":84,"digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","expired":false,"created_at":"2026-07-25T10:00:00Z","updated_at":"2026-07-25T10:00:30Z","expires_at":"2026-08-24T10:00:00Z","url":"https://api.github.test/braw","archive_download_url":"https://api.github.test/canny","node_id":"dreich","workflow_run":{"id":81}}]}`)
		case "/repos/d0ugal/graith/actions/runs/81/timing":
			if timingGone {
				http.Error(writer, "gone", http.StatusGone)

				return
			}

			if timingNegative {
				writeResponse(t, writer, `{"billable":{"UBUNTU":{"total_ms":-1}}}`)

				return
			}

			writeResponse(t, writer, `{"billable":{"UBUNTU":{"total_ms":60001}}}`)
		default:
			http.NotFound(writer, request)
		}
	}))

	defer server.Close()

	now := time.Date(2026, 7, 25, 12, 0, 0, 123, time.UTC)
	collector := GitHubCollector{
		Client: server.Client(), BaseURL: server.URL, Token: "canny-token",
		Now: func() time.Time { return now },
	}

	snapshot, err := collector.Fetch(context.Background(), "d0ugal/graith", now.Add(-24*time.Hour), loadInventory(t))
	if err != nil {
		t.Fatal(err)
	}

	if len(snapshot.Runs) != 1 || len(snapshot.Caches) != 1 {
		t.Fatalf("snapshot runs/caches = %d/%d, want 1/1", len(snapshot.Runs), len(snapshot.Caches))
	}

	run := snapshot.Runs[0]
	if run.Jobs[0].Key != "test" || run.Jobs[0].Coordinate != "ci/test" ||
		run.Cost.BillableMinutes["UBUNTU"] != 2 ||
		run.Artifacts[0].Digest != "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("run evidence = %#v", run)
	}

	if !snapshot.CollectedAt.Equal(now.Truncate(time.Second)) {
		t.Fatalf("collected_at = %s", snapshot.CollectedAt)
	}

	for _, request := range requests {
		if !strings.HasPrefix(request, "GET ") {
			t.Fatalf("mutating request observed: %s", request)
		}
	}

	timingGone = true

	unavailable, err := collector.Fetch(context.Background(), "d0ugal/graith", now.Add(-24*time.Hour), loadInventory(t))
	if err != nil {
		t.Fatal(err)
	}

	if unavailable.Runs[0].Cost.Available || !strings.Contains(unavailable.Runs[0].Cost.UnavailableReason, "410") {
		t.Fatalf("unavailable cost = %#v, want explicit HTTP 410 reason", unavailable.Runs[0].Cost)
	}

	timingGone = false
	timingNegative = true

	if _, err := collector.Fetch(context.Background(), "d0ugal/graith", now.Add(-24*time.Hour), loadInventory(t)); err == nil ||
		!strings.Contains(err.Error(), "negative run usage") {
		t.Fatalf("Fetch(negative raw usage) error = %v, want rejection", err)
	}
}

func TestGitHubArtifactWireAllowsAdditionsButRequiresNullableTimestamps(t *testing.T) {
	var artifact githubArtifact
	if err := json.Unmarshal([]byte(`{
		"id": 83,
		"name": "braw",
		"size_in_bytes": 1,
		"expired": false,
		"created_at": null,
		"updated_at": null,
		"expires_at": null,
		"url": "https://api.github.test/canny"
	}`), &artifact); err != nil {
		t.Fatalf("UnmarshalJSON(additive API field): %v", err)
	}

	if err := json.Unmarshal([]byte(`{
		"id": 83,
		"name": "braw",
		"size_in_bytes": 1,
		"expired": false,
		"created_at": null,
		"updated_at": null
	}`), &artifact); err == nil || !strings.Contains(err.Error(), "missing required field") {
		t.Fatalf("UnmarshalJSON(missing API timestamp) error = %v, want rejection", err)
	}
}

func TestGitHubCollectorFetchesAttemptScopedRunMetadata(t *testing.T) {
	mismatchedAttemptPath := false
	omittedAttemptIdentity := false

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")

		switch request.URL.Path {
		case "/repos/d0ugal/graith/actions/runs":
			writeResponse(t, writer, `{"total_count":1,"workflow_runs":[{"id":81,"run_attempt":2,"path":".github/workflows/ci.yml","event":"pull_request","head_sha":"braw","head_branch":"canny","pull_requests":[],"created_at":"2026-07-25T10:05:00Z","run_started_at":"2026-07-25T10:05:10Z","updated_at":"2026-07-25T10:06:00Z","status":"completed","conclusion":"cancelled"}]}`)
		case "/repos/d0ugal/graith/actions/runs/81/attempts/1":
			if omittedAttemptIdentity {
				writeResponse(t, writer, `{"id":81,"run_attempt":1}`)

				return
			}

			attemptPath := ".github/workflows/ci.yml"
			if mismatchedAttemptPath {
				attemptPath = ".github/workflows/ci.yml@refs/heads/dreich"
			}

			writeResponse(t, writer, fmt.Sprintf(
				`{"id":81,"run_attempt":1,"path":%q,"event":"pull_request","head_sha":"braw","head_branch":"canny","pull_requests":[],"created_at":"2026-07-25T10:00:00Z","run_started_at":"2026-07-25T10:00:10Z","updated_at":"2026-07-25T10:01:00Z","status":"completed","conclusion":"failure"}`,
				attemptPath,
			))
		case "/repos/d0ugal/graith/actions/runs/81/attempts/1/jobs":
			writeResponse(t, writer, `{"total_count":1,"jobs":[{"id":82,"name":"Lint","status":"completed","conclusion":"failure","created_at":"2026-07-25T10:00:00Z","started_at":"2026-07-25T10:00:10Z","completed_at":"2026-07-25T10:01:00Z","runner_name":null,"runner_group_name":null,"labels":[]}]}`)
		case "/repos/d0ugal/graith/actions/runs/81/attempts/2/jobs":
			writeResponse(t, writer, `{"total_count":1,"jobs":[{"id":83,"name":"Lint","status":"completed","conclusion":"cancelled","created_at":"2026-07-25T10:05:00Z","started_at":"2026-07-25T10:05:10Z","completed_at":"2026-07-25T10:06:00Z","runner_name":null,"runner_group_name":null,"labels":[]}]}`)
		case "/repos/d0ugal/graith/actions/caches":
			writeResponse(t, writer, `{"total_count":0,"actions_caches":[]}`)
		case "/repos/d0ugal/graith/actions/runs/81/artifacts":
			writeResponse(t, writer, `{"total_count":0,"artifacts":[]}`)
		case "/repos/d0ugal/graith/actions/runs/81/timing":
			http.Error(writer, "gone", http.StatusGone)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	collector := GitHubCollector{Client: server.Client(), BaseURL: server.URL, Now: func() time.Time { return now }}

	snapshot, err := collector.Fetch(context.Background(), "d0ugal/graith", now.Add(-2*time.Hour), loadInventory(t))
	if err != nil {
		t.Fatal(err)
	}

	if len(snapshot.Runs) != 2 || snapshot.Runs[0].Conclusion != "failure" ||
		!snapshot.Runs[0].CreatedAt.Equal(time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC)) ||
		snapshot.Runs[1].Conclusion != "cancelled" {
		t.Fatalf("attempt-scoped snapshot = %#v", snapshot.Runs)
	}

	mismatchedAttemptPath = true

	if _, err := collector.Fetch(context.Background(), "d0ugal/graith", now.Add(-2*time.Hour), loadInventory(t)); err == nil ||
		!strings.Contains(err.Error(), "mismatched metadata") {
		t.Fatalf("Fetch() error = %v, want mismatched attempt identity rejection", err)
	}

	mismatchedAttemptPath = false
	omittedAttemptIdentity = true

	if _, err := collector.Fetch(context.Background(), "d0ugal/graith", now.Add(-2*time.Hour), loadInventory(t)); err == nil ||
		!strings.Contains(err.Error(), "missing required field") {
		t.Fatalf("Fetch(omitted identity) error = %v, want fail-closed rejection", err)
	}
}

func TestWorkflowIDFromPathHandlesGitHubRefSuffix(t *testing.T) {
	tests := map[string]string{
		".github/workflows/ci.yml":                          "ci",
		".github/workflows/ci.yml@main":                     "ci",
		".github/workflows/ci.yml@refs/heads/canny/braw":    "ci",
		".github/workflows/docs-preview.yaml@canny/blether": "docs-preview",
	}

	for workflowPath, want := range tests {
		if got := workflowIDFromPath(workflowPath); got != want {
			t.Errorf("workflowIDFromPath(%q) = %q, want %q", workflowPath, got, want)
		}
	}

	for _, malformed := range []string{"", "/.github/workflows/ci.yml", "../../.github/workflows/ci.yml", "ci.yml@"} {
		if got := normalizeWorkflowPathReference(malformed); got != "" {
			t.Errorf("normalizeWorkflowPathReference(%q) = %q, want fail-closed empty result", malformed, got)
		}
	}
}

func TestResolveJobCoordinateCoversNamedAndUnnamedMatrixJobs(t *testing.T) {
	inventory := loadInventory(t)

	tests := []struct {
		workflow string
		name     string
		want     string
	}{
		{"dev-release", "Build and verify Linux dev archive (amd64)", "dev-release/build-linux[goarch=amd64,target=x86_64-linux-gnu]"},
		{"libghostty-native-publish", "publish (amd64, x86_64-linux-gnu)", "libghostty-native-publish/publish[goarch=amd64,target=x86_64-linux-gnu]"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := resolveJobCoordinates(inventory, test.workflow, test.name)
			if len(got) != 1 || got[0].Coordinate != test.want {
				t.Fatalf("resolveJobCoordinates() = %#v; want %q", got, test.want)
			}
		})
	}

	got := resolveJobCoordinates(inventory, "goreleaser", "Execute stable Linux artifacts (${{ matrix.goarch }})")
	if len(got) != 2 {
		t.Fatalf("unexpanded matrix coordinates = %#v, want 2 skipped legs", got)
	}

	for _, job := range []githubJob{
		{Name: "Execute stable Linux artifacts (${{ matrix.goarch }})", Status: "completed", Conclusion: "success"},
		{Name: "Execute stable Linux artifacts (${{ matrix.goarch }})", Status: "in_progress", Conclusion: "skipped"},
	} {
		if _, err := resolveGitHubJobCoordinates(inventory, "goreleaser", job); err == nil ||
			!strings.Contains(err.Error(), "not completed/skipped") {
			t.Fatalf("resolveGitHubJobCoordinates(%#v) error = %v, want fail-closed rejection", job, err)
		}
	}

	skipped := githubJob{
		Name:   "Execute stable Linux artifacts (${{ matrix.goarch }})",
		Status: "completed", Conclusion: "skipped",
	}
	if got, err := resolveGitHubJobCoordinates(inventory, "goreleaser", skipped); err != nil || len(got) != 2 {
		t.Fatalf("resolveGitHubJobCoordinates(skipped) = %#v, %v; want two coordinates", got, err)
	}
}

func TestGitHubRunIdentityIncludesWorkflowRefAndChangeCoordinate(t *testing.T) {
	base := githubRun{
		Path: ".github/workflows/ci.yml@refs/pull/7/merge", Event: "pull_request",
		HeadSHA: "braw", HeadBranch: "canny",
	}
	base.PullRequests = append(base.PullRequests, githubPullRequest{Number: 7})

	tests := []struct {
		name string
		edit func(*githubRun)
	}{
		{"workflow ref", func(run *githubRun) { run.Path = ".github/workflows/ci.yml@refs/heads/canny" }},
		{"event", func(run *githubRun) { run.Event = "push" }},
		{"head SHA", func(run *githubRun) { run.HeadSHA = "dreich" }},
		{"head branch", func(run *githubRun) { run.HeadBranch = "bothy" }},
		{"pull request", func(run *githubRun) { run.PullRequests[0].Number = 8 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			next := base
			next.PullRequests = append([]githubPullRequest(nil), base.PullRequests...)
			test.edit(&next)

			if sameGitHubRunIdentity(base, next) {
				t.Fatalf("sameGitHubRunIdentity() accepted changed %s", test.name)
			}
		})
	}

	next := base

	next.Path = "./.github/workflows/ci.yml@refs/pull/7/merge"
	if !sameGitHubRunIdentity(base, next) {
		t.Fatal("sameGitHubRunIdentity() rejected normalized equivalent path/ref")
	}
}

func TestGitHubCollectorRejectsIncompleteCount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writeResponse(t, writer, `{"total_count":2,"workflow_runs":[]}`)
	}))
	defer server.Close()

	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)

	collector := GitHubCollector{Client: server.Client(), BaseURL: server.URL, Now: func() time.Time { return now }}
	if _, err := collector.Fetch(context.Background(), "d0ugal/graith", now.Add(-2*time.Hour), loadInventory(t)); err == nil ||
		!strings.Contains(err.Error(), "count mismatch") {
		t.Fatalf("Fetch() error = %v, want count mismatch", err)
	}
}

func TestGitHubCollectorUsesStableMaturedRunCutoff(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	since := now.Add(-2 * time.Hour)
	cutoff := now.Add(-time.Hour)
	stable := matureGitHubRun(81, since.Add(10*time.Minute), cutoff.Add(-10*time.Minute))

	tests := map[string]struct {
		first  githubRun
		second githubRun
		want   string
	}{
		"stable mature terminal set": {
			first: stable, second: stable,
		},
		"completion during first observation": {
			first: func() githubRun {
				run := stable
				run.Status, run.Conclusion = "in_progress", ""
				run.UpdatedAt = cutoff.Add(-time.Minute)

				return run
			}(),
			second: stable,
			want:   "is unsettled",
		},
		"completion during final consistency pass": {
			first: stable,
			second: func() githubRun {
				run := stable
				run.Status, run.Conclusion = "in_progress", ""
				run.UpdatedAt = cutoff.Add(time.Minute)

				return run
			}(),
			want: "identities or states changed",
		},
		"rerun transition during collection": {
			first: stable,
			second: func() githubRun {
				run := stable
				run.Attempt = 2
				run.UpdatedAt = cutoff.Add(time.Minute)

				return run
			}(),
			want: "identities or states changed",
		},
		"conclusion churn during collection": {
			first: stable,
			second: func() githubRun {
				run := stable
				run.Conclusion = "cancelled"

				return run
			}(),
			want: "identities or states changed",
		},
		"identity churn during collection": {
			first: stable,
			second: func() githubRun {
				run := stable
				run.ID = 82

				return run
			}(),
			want: "identities or states changed",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			server := newRunCollectionServer(t, []githubRunsPage{
				{TotalCount: 1, Runs: []githubRun{test.first}},
				{TotalCount: 1, Runs: []githubRun{test.second}},
			})
			defer server.Close()

			collector := GitHubCollector{
				Client: server.Client(), BaseURL: server.URL, Now: func() time.Time { return now },
				MaturationDelay: time.Hour,
			}

			snapshot, err := collector.Fetch(context.Background(), "d0ugal/graith", since, loadInventory(t))
			if test.want == "" {
				if err != nil {
					t.Fatal(err)
				}

				if !snapshot.RequestedUntil.Equal(cutoff) || !snapshot.CollectedAt.Equal(now) ||
					snapshot.ExpectedWorkflowRuns != 1 {
					t.Fatalf("snapshot cutoff/count = %s/%d, collected %s", snapshot.RequestedUntil, snapshot.ExpectedWorkflowRuns, snapshot.CollectedAt)
				}

				return
			}

			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Fetch() error = %v, want containing %q", err, test.want)
			}

			if !reflect.DeepEqual(snapshot, GitHubSnapshot{}) {
				t.Fatalf("Fetch() returned partial snapshot %#v", snapshot)
			}
		})
	}
}

func TestGitHubCollectorHandlesNullConclusionForUnsettledRuns(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	since := now.Add(-2 * time.Hour)
	stable := `{"total_count":1,"workflow_runs":[{"id":81,"run_attempt":1,"path":".github/workflows/ci.yml","event":"push","head_sha":"braw","head_branch":"main","pull_requests":[],"created_at":"2026-07-25T10:10:00Z","run_started_at":"2026-07-25T10:10:01Z","updated_at":"2026-07-25T10:50:00Z","status":"completed","conclusion":"startup_failure"}]}`
	unsettled := `{"total_count":1,"workflow_runs":[{"id":81,"run_attempt":1,"path":".github/workflows/ci.yml","event":"push","head_sha":"braw","head_branch":"main","pull_requests":[],"created_at":"2026-07-25T10:10:00Z","run_started_at":null,"updated_at":"2026-07-25T10:50:00Z","status":"queued","conclusion":null}]}`

	tests := map[string]struct {
		pages []string
		want  string
	}{
		"first observation": {
			pages: []string{unsettled},
			want:  "is unsettled",
		},
		"final consistency pass": {
			pages: []string{stable, unsettled},
			want:  "identities or states changed",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			server := newRawRunCollectionServer(t, test.pages)
			defer server.Close()

			collector := GitHubCollector{
				Client: server.Client(), BaseURL: server.URL, Now: func() time.Time { return now },
				MaturationDelay: time.Hour,
			}

			snapshot, err := collector.Fetch(context.Background(), "d0ugal/graith", since, loadInventory(t))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Fetch() error = %v, want containing %q", err, test.want)
			}

			if !reflect.DeepEqual(snapshot, GitHubSnapshot{}) {
				t.Fatalf("Fetch() returned partial snapshot %#v", snapshot)
			}
		})
	}
}

func TestGitHubCollectorAcceptsStableRunReordering(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	since := now.Add(-2 * time.Hour)
	first := matureGitHubRun(81, since.Add(10*time.Minute), now.Add(-70*time.Minute))
	second := matureGitHubRun(82, since.Add(10*time.Minute), now.Add(-70*time.Minute))

	server := newRunCollectionServer(t, []githubRunsPage{
		{TotalCount: 2, Runs: []githubRun{first, second}},
		{TotalCount: 2, Runs: []githubRun{second, first}},
	})
	defer server.Close()

	collector := GitHubCollector{
		Client: server.Client(), BaseURL: server.URL, Now: func() time.Time { return now },
		MaturationDelay: time.Hour,
	}

	snapshot, err := collector.Fetch(context.Background(), "d0ugal/graith", since, loadInventory(t))
	if err != nil {
		t.Fatal(err)
	}

	if snapshot.ExpectedWorkflowRuns != 2 {
		t.Fatalf("ExpectedWorkflowRuns = %d, want 2", snapshot.ExpectedWorkflowRuns)
	}
}

func TestGitHubRunStateNormalizesEquivalentTimestamps(t *testing.T) {
	utc := time.Date(2026, 7, 25, 10, 0, 0, 123, time.UTC)
	fixedUTC := time.Date(2026, 7, 25, 10, 0, 0, 123, time.FixedZone("", 0))
	first := matureGitHubRun(81, utc, utc)
	second := matureGitHubRun(81, fixedUTC, fixedUTC)

	if githubRunStateOf(first) != githubRunStateOf(second) {
		t.Fatal("githubRunStateOf() distinguished equivalent timestamp instants")
	}
}

func TestGitHubCollectorRejectsRunCountChurnInFinalPass(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	since := now.Add(-2 * time.Hour)
	stable := matureGitHubRun(81, since.Add(10*time.Minute), now.Add(-70*time.Minute))

	server := newRunCollectionServer(t, []githubRunsPage{
		{TotalCount: 1, Runs: []githubRun{stable}},
		{TotalCount: 0, Runs: []githubRun{}},
	})
	defer server.Close()

	collector := GitHubCollector{
		Client: server.Client(), BaseURL: server.URL, Now: func() time.Time { return now },
		MaturationDelay: time.Hour,
	}

	snapshot, err := collector.Fetch(context.Background(), "d0ugal/graith", since, loadInventory(t))
	if err == nil || !strings.Contains(err.Error(), "count changed during pagination consistency pass") {
		t.Fatalf("Fetch() error = %v, want final count-churn rejection", err)
	}

	if !reflect.DeepEqual(snapshot, GitHubSnapshot{}) {
		t.Fatalf("Fetch() returned partial snapshot %#v", snapshot)
	}
}

func TestMatureGitHubRunWindowBoundariesAndFailures(t *testing.T) {
	since := time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC)
	cutoff := since.Add(time.Hour)

	tests := map[string]struct {
		run  githubRun
		want string
	}{
		"created at since": {
			run: matureGitHubRun(1, since, cutoff),
		},
		"created at cutoff": {
			run: matureGitHubRun(1, cutoff, cutoff),
		},
		"created before since": {
			run:  matureGitHubRun(1, since.Add(-time.Second), cutoff),
			want: "outside requested window",
		},
		"created after cutoff": {
			run:  matureGitHubRun(1, cutoff.Add(time.Second), cutoff.Add(time.Second)),
			want: "outside requested window",
		},
		"updated after cutoff": {
			run:  matureGitHubRun(1, since, cutoff.Add(time.Second)),
			want: "insufficiently mature",
		},
		"nonterminal before cutoff": {
			run: func() githubRun {
				run := matureGitHubRun(1, since, cutoff.Add(-time.Second))
				run.Status, run.Conclusion = "queued", ""

				return run
			}(),
			want: "is unsettled",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			err := validateMatureGitHubRun(test.run, since, cutoff)
			if test.want == "" && err != nil {
				t.Fatal(err)
			}

			if test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)) {
				t.Fatalf("validateMatureGitHubRun() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestGitHubCollectorMaturationConfigurationAndCutoffValidation(t *testing.T) {
	configured, err := (GitHubCollector{}).configured()
	if err != nil {
		t.Fatal(err)
	}

	if configured.MaturationDelay != DefaultRunMaturationDelay {
		t.Fatalf("default maturation = %s, want %s", configured.MaturationDelay, DefaultRunMaturationDelay)
	}

	if _, err := (GitHubCollector{MaturationDelay: -time.Second}).configured(); err == nil {
		t.Fatal("configured() accepted a negative maturation delay")
	}

	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	for name, since := range map[string]time.Time{
		"equal":   now.Add(-time.Hour),
		"earlier": now.Add(-30 * time.Minute),
	} {
		t.Run(name, func(t *testing.T) {
			collector := GitHubCollector{Now: func() time.Time { return now }, MaturationDelay: time.Hour}
			if _, err := collector.Fetch(context.Background(), "d0ugal/graith", since, loadInventory(t)); err == nil ||
				!strings.Contains(err.Error(), "must be after since") {
				t.Fatalf("Fetch() error = %v, want invalid cutoff rejection", err)
			}
		})
	}
}

func TestGitHubCollectorRetainsThousandResultCeiling(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writeResponse(t, writer, `{"total_count":1001,"workflow_runs":[]}`)
	}))
	defer server.Close()

	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	collector := GitHubCollector{
		Client: server.Client(), BaseURL: server.URL, Now: func() time.Time { return now },
		MaturationDelay: time.Hour,
	}

	snapshot, err := collector.Fetch(context.Background(), "d0ugal/graith", now.Add(-2*time.Hour), loadInventory(t))
	if err == nil || !strings.Contains(err.Error(), "1000-result API ceiling") {
		t.Fatalf("Fetch() error = %v, want 1,000-result ceiling rejection", err)
	}

	if !reflect.DeepEqual(snapshot, GitHubSnapshot{}) {
		t.Fatalf("Fetch() returned partial snapshot %#v", snapshot)
	}
}

func matureGitHubRun(id int64, createdAt, updatedAt time.Time) githubRun {
	return githubRun{
		ID: id, Attempt: 1, Path: ".github/workflows/ci.yml", Event: "push",
		HeadSHA: "braw", HeadBranch: "main", PullRequests: []githubPullRequest{},
		CreatedAt: createdAt, UpdatedAt: updatedAt, Status: "completed", Conclusion: "startup_failure",
	}
}

func newRunCollectionServer(t *testing.T, runPages []githubRunsPage) *httptest.Server {
	t.Helper()

	runRequest := 0

	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")

		switch request.URL.Path {
		case "/repos/d0ugal/graith/actions/runs":
			index := runRequest
			if index >= len(runPages) {
				index = len(runPages) - 1
			}

			runRequest++

			if err := json.NewEncoder(writer).Encode(runPages[index]); err != nil {
				t.Errorf("encode runs: %v", err)
			}
		case "/repos/d0ugal/graith/actions/caches":
			writeResponse(t, writer, `{"total_count":0,"actions_caches":[]}`)
		case "/repos/d0ugal/graith/actions/runs/81/artifacts",
			"/repos/d0ugal/graith/actions/runs/82/artifacts":
			writeResponse(t, writer, `{"total_count":0,"artifacts":[]}`)
		case "/repos/d0ugal/graith/actions/runs/81/timing",
			"/repos/d0ugal/graith/actions/runs/82/timing":
			http.Error(writer, "gone", http.StatusGone)
		case "/repos/d0ugal/graith/actions/runs/81/attempts/1/jobs",
			"/repos/d0ugal/graith/actions/runs/82/attempts/1/jobs":
			writeResponse(t, writer, `{"total_count":0,"jobs":[]}`)
		default:
			http.NotFound(writer, request)
		}
	}))
}

func newRawRunCollectionServer(t *testing.T, runPages []string) *httptest.Server {
	t.Helper()

	runRequest := 0

	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")

		switch request.URL.Path {
		case "/repos/d0ugal/graith/actions/runs":
			index := runRequest
			if index >= len(runPages) {
				index = len(runPages) - 1
			}

			runRequest++

			writeResponse(t, writer, runPages[index])
		case "/repos/d0ugal/graith/actions/caches":
			writeResponse(t, writer, `{"total_count":0,"actions_caches":[]}`)
		case "/repos/d0ugal/graith/actions/runs/81/artifacts":
			writeResponse(t, writer, `{"total_count":0,"artifacts":[]}`)
		case "/repos/d0ugal/graith/actions/runs/81/timing":
			http.Error(writer, "gone", http.StatusGone)
		case "/repos/d0ugal/graith/actions/runs/81/attempts/1/jobs":
			writeResponse(t, writer, `{"total_count":0,"jobs":[]}`)
		default:
			http.NotFound(writer, request)
		}
	}))
}

func TestFetchCachesPaginates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")

		page := request.URL.Query().Get("page")
		response := githubCachesPage{TotalCount: 101}

		switch page {
		case "1":
			for index := 1; index <= 100; index++ {
				response.Caches = append(response.Caches, githubCache{ID: int64(index), Key: fmt.Sprintf("croft-%d", index)})
			}
		case "2":
			response.Caches = []githubCache{{ID: 101, Key: "bothy"}}
		default:
			t.Errorf("unexpected page %q", page)
		}

		if err := json.NewEncoder(writer).Encode(response); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}))
	defer server.Close()

	collector := GitHubCollector{Client: server.Client(), BaseURL: server.URL}

	caches, count, err := collector.fetchCaches(context.Background(), "d0ugal/graith")
	if err != nil {
		t.Fatal(err)
	}

	if len(caches) != 101 || count != 101 || caches[100].Key != "bothy" {
		t.Fatalf("caches/count = %d/%d, last=%q; want 101/101/bothy", len(caches), count, caches[100].Key)
	}
}

func TestFetchJobsAndArtifactsPaginate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")

		page := request.URL.Query().Get("page")

		switch request.URL.Path {
		case "/repos/d0ugal/graith/actions/runs/81/attempts/1/jobs":
			response := githubJobsPage{TotalCount: 101}

			if page == "1" {
				for index := 1; index <= 100; index++ {
					response.Jobs = append(response.Jobs, githubJob{
						ID: int64(index), Name: fmt.Sprintf("braw-%d", index), Labels: []string{},
					})
				}
			} else {
				response.Jobs = []githubJob{{ID: 101, Name: "canny", Labels: []string{}}}
			}

			if err := json.NewEncoder(writer).Encode(response); err != nil {
				t.Errorf("encode jobs: %v", err)
			}
		case "/repos/d0ugal/graith/actions/runs/81/artifacts":
			response := githubArtifactsPage{TotalCount: 101}

			if page == "1" {
				for index := 1; index <= 100; index++ {
					response.Artifacts = append(response.Artifacts, githubArtifact{ID: int64(index), Name: fmt.Sprintf("croft-%d", index)})
				}
			} else {
				response.Artifacts = []githubArtifact{{ID: 101, Name: "bothy"}}
			}

			if err := json.NewEncoder(writer).Encode(response); err != nil {
				t.Errorf("encode artifacts: %v", err)
			}
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	collector := GitHubCollector{Client: server.Client(), BaseURL: server.URL}

	jobs, jobCount, err := collector.fetchJobs(context.Background(), "d0ugal/graith", 81, 1)
	if err != nil {
		t.Fatal(err)
	}

	artifacts, artifactCount, err := collector.fetchArtifacts(context.Background(), "d0ugal/graith", 81)
	if err != nil {
		t.Fatal(err)
	}

	if len(jobs) != 101 || jobCount != 101 || jobs[100].Name != "canny" ||
		len(artifacts) != 101 || artifactCount != 101 || artifacts[100].Name != "bothy" {
		t.Fatalf("paginated jobs/artifacts = %d/%d (counts %d/%d)", len(jobs), len(artifacts), jobCount, artifactCount)
	}
}

func TestPaginationTotalsFailClosed(t *testing.T) {
	tests := []struct {
		name string
		path string
		call func(GitHubCollector) error
	}{
		{"caches", "/repos/d0ugal/graith/actions/caches", func(collector GitHubCollector) error {
			_, _, err := collector.fetchCaches(context.Background(), "d0ugal/graith")

			return err
		}},
		{"jobs", "/repos/d0ugal/graith/actions/runs/81/attempts/1/jobs", func(collector GitHubCollector) error {
			_, _, err := collector.fetchJobs(context.Background(), "d0ugal/graith", 81, 1)

			return err
		}},
		{"artifacts", "/repos/d0ugal/graith/actions/runs/81/artifacts", func(collector GitHubCollector) error {
			_, _, err := collector.fetchArtifacts(context.Background(), "d0ugal/graith", 81)

			return err
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mode := "negative"

			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.Header().Set("Content-Type", "application/json")

				if request.URL.Path != test.path {
					http.NotFound(writer, request)

					return
				}

				total := -1
				count := 0

				if mode == "changed" {
					total = 101
					count = 100

					if request.URL.Query().Get("page") == "2" {
						total, count = 100, 1
					}
				}

				switch test.name {
				case "caches":
					page := githubCachesPage{TotalCount: total, Caches: []githubCache{}}
					for index := 0; index < count; index++ {
						page.Caches = append(page.Caches, githubCache{ID: int64(index + 1)})
					}

					if err := json.NewEncoder(writer).Encode(page); err != nil {
						t.Errorf("encode caches: %v", err)
					}
				case "jobs":
					page := githubJobsPage{TotalCount: total, Jobs: []githubJob{}}
					for index := 0; index < count; index++ {
						page.Jobs = append(page.Jobs, githubJob{ID: int64(index + 1), Labels: []string{}})
					}

					if err := json.NewEncoder(writer).Encode(page); err != nil {
						t.Errorf("encode jobs: %v", err)
					}
				case "artifacts":
					page := githubArtifactsPage{TotalCount: total, Artifacts: []githubArtifact{}}
					for index := 0; index < count; index++ {
						page.Artifacts = append(page.Artifacts, githubArtifact{ID: int64(index + 1)})
					}

					if err := json.NewEncoder(writer).Encode(page); err != nil {
						t.Errorf("encode artifacts: %v", err)
					}
				}
			}))
			defer server.Close()

			collector := GitHubCollector{Client: server.Client(), BaseURL: server.URL}
			if err := test.call(collector); err == nil || !strings.Contains(err.Error(), "negative") {
				t.Fatalf("%s negative total error = %v, want rejection", test.name, err)
			}

			mode = "changed"

			if err := test.call(collector); err == nil || !strings.Contains(err.Error(), "changed during pagination") {
				t.Fatalf("%s changing total error = %v, want rejection", test.name, err)
			}
		})
	}
}

func writeResponse(t *testing.T, writer http.ResponseWriter, body string) {
	t.Helper()

	if _, err := fmt.Fprint(writer, body); err != nil {
		t.Errorf("write response: %v", err)
	}
}

func TestParseSince(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)

	got, err := ParseSince("28", now)
	if err != nil {
		t.Fatal(err)
	}

	if want := now.Add(-28 * 24 * time.Hour); !got.Equal(want) {
		t.Fatalf("ParseSince = %s, want %s", got, want)
	}

	if _, err := ParseSince("29", now); err == nil {
		t.Fatal("ParseSince accepted more than the four-week collection window")
	}
}

func TestInferSuperseded(t *testing.T) {
	start := time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC)
	input := []GitHubRun{
		{ID: 1, Attempt: 1, WorkflowID: "ci", Event: "pull_request", PullRequest: 7, Conclusion: "failure", CreatedAt: start},
		{ID: 1, Attempt: 2, WorkflowID: "ci", Event: "pull_request", PullRequest: 7, Conclusion: "cancelled", CreatedAt: start.Add(time.Minute)},
		{ID: 3, Attempt: 1, WorkflowID: "ci", Event: "pull_request", PullRequest: 7, Conclusion: "success", CreatedAt: start.Add(2 * time.Minute)},
		{ID: 2, Attempt: 1, WorkflowID: "ci", Event: "pull_request", PullRequest: 7, Conclusion: "success", CreatedAt: start.Add(2 * time.Minute)},
		{ID: 4, Attempt: 1, WorkflowID: "ci", Event: "pull_request", PullRequest: 8, Conclusion: "success", CreatedAt: start.Add(3 * time.Minute)},
	}

	for _, runs := range [][]GitHubRun{
		append([]GitHubRun(nil), input...),
		{input[4], input[3], input[2], input[1], input[0]},
	} {
		inferSuperseded(runs)

		for _, run := range runs {
			switch {
			case run.ID == 1 && run.Attempt == 1 && run.SupersededBy != 0:
				t.Fatalf("completed attempt was marked superseded: %#v", run)
			case run.ID == 1 && run.Attempt == 2 && run.SupersededBy != 2:
				t.Fatalf("superseded_by = %d, want deterministic run 2", run.SupersededBy)
			}
		}
	}
}

func TestRawGitHubRunFieldsFailClosed(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	base := githubRun{
		ID: 81, Attempt: 1, Path: ".github/workflows/ci.yml", Event: "push", HeadSHA: "braw",
		CreatedAt: now.Add(-time.Minute), StartedAt: now.Add(-30 * time.Second), UpdatedAt: now,
		Status: "completed", Conclusion: "success",
	}

	tests := []struct {
		name string
		edit func(*githubRun)
	}{
		{"run ID", func(run *githubRun) { run.ID = 0 }},
		{"attempt", func(run *githubRun) { run.Attempt = 0 }},
		{"path", func(run *githubRun) { run.Path = "../ci.yml" }},
		{"event", func(run *githubRun) { run.Event = " " }},
		{"head SHA", func(run *githubRun) { run.HeadSHA = "" }},
		{"created timestamp", func(run *githubRun) { run.CreatedAt = time.Time{} }},
		{"updated timestamp", func(run *githubRun) { run.UpdatedAt = time.Time{} }},
		{"reversed run timestamps", func(run *githubRun) { run.UpdatedAt = run.CreatedAt.Add(-time.Second) }},
		{"started after update", func(run *githubRun) { run.StartedAt = run.UpdatedAt.Add(time.Second) }},
		{"status", func(run *githubRun) { run.Status = "queued" }},
		{"conclusion", func(run *githubRun) { run.Conclusion = "blether" }},
		{"pull request coordinate", func(run *githubRun) {
			run.Event = "pull_request"
			run.PullRequests = append(run.PullRequests, struct {
				Number int64 `json:"number"`
			}{Number: -1})
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			run := base
			test.edit(&run)

			if err := validateRawGitHubRun(run); err == nil {
				t.Fatalf("validateRawGitHubRun() accepted malformed %s", test.name)
			}
		})
	}

	emptyPullRequests := base
	emptyPullRequests.Event = "pull_request"
	emptyPullRequests.HeadBranch = "canny"

	if err := validateRawGitHubRun(emptyPullRequests); err != nil {
		t.Fatalf("validateRawGitHubRun(pull request with branch fallback): %v", err)
	}
}

func TestRawGitHubJobFieldsFailClosed(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	base := githubJob{
		ID: 82, Name: "Braw", Status: "completed", Conclusion: "success",
		CreatedAt: now.Add(-time.Minute), StartedAt: now.Add(-30 * time.Second), CompletedAt: now,
		Labels: []string{"ubuntu-latest"},
	}

	tests := []struct {
		name string
		edit func(*githubJob)
	}{
		{"job ID", func(job *githubJob) { job.ID = 0 }},
		{"name", func(job *githubJob) { job.Name = " " }},
		{"status", func(job *githubJob) { job.Status = "queued" }},
		{"conclusion", func(job *githubJob) { job.Conclusion = "blether" }},
		{"created timestamp", func(job *githubJob) { job.CreatedAt = time.Time{} }},
		{"started timestamp", func(job *githubJob) { job.StartedAt = time.Time{} }},
		{"completed timestamp", func(job *githubJob) { job.CompletedAt = time.Time{} }},
		{"reversed timestamps", func(job *githubJob) { job.CompletedAt = job.StartedAt.Add(-time.Second) }},
		{"blank label", func(job *githubJob) { job.Labels = []string{" "} }},
		{"duplicate label", func(job *githubJob) { job.Labels = []string{"ubuntu-latest", "ubuntu-latest"} }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			job := base
			job.Labels = append([]string(nil), base.Labels...)
			test.edit(&job)

			if err := validateRawGitHubJob(job); err == nil {
				t.Fatalf("validateRawGitHubJob() accepted malformed %s", test.name)
			}
		})
	}

	skipped := base
	skipped.Conclusion = "skipped"
	skipped.StartedAt = time.Time{}
	skipped.CompletedAt = time.Time{}

	if err := validateRawGitHubJob(skipped); err != nil {
		t.Fatalf("validateRawGitHubJob(skipped with nullable execution timestamps): %v", err)
	}

	skipped.StartedAt = skipped.CreatedAt
	skipped.CompletedAt = skipped.CreatedAt.Add(-time.Second)

	if err := validateRawGitHubJob(skipped); err != nil {
		t.Fatalf("validateRawGitHubJob(skipped with GitHub non-monotonic timestamps): %v", err)
	}
}

func TestCostFieldsFailClosed(t *testing.T) {
	tests := []struct {
		name string
		cost CostInput
	}{
		{"available missing source", CostInput{Available: true}},
		{"available with reason", CostInput{Available: true, Source: "braw", UnavailableReason: "canny"}},
		{"available negative billable", CostInput{
			Available: true, Source: "braw", BillableMinutes: map[string]int64{"UBUNTU": -1},
		}},
		{"available blank platform", CostInput{
			Available: true, Source: "braw", BillableMinutes: map[string]int64{" ": 1},
		}},
		{"unavailable missing reason", CostInput{}},
		{"unavailable with billable", CostInput{
			UnavailableReason: "dreich", BillableMinutes: map[string]int64{"UBUNTU": 1},
		}},
		{"blank label", CostInput{UnavailableReason: "dreich", RunnerLabels: []string{" "}}},
		{"duplicate label", CostInput{
			UnavailableReason: "dreich", RunnerLabels: []string{"ubuntu-latest", "ubuntu-latest"},
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateCost(test.cost); err == nil {
				t.Fatalf("validateCost() accepted %s", test.name)
			}
		})
	}
}

func TestGitHubCollectorRateLimitWaitMetadata(t *testing.T) {
	baseTime := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	tests := map[string]struct {
		status  int
		headers map[string]string
		body    string
		want    time.Duration
	}{
		"primary reset with boundary cushion": {
			status: http.StatusForbidden,
			headers: map[string]string{
				"X-RateLimit-Remaining": "0",
				"X-RateLimit-Reset":     strconv.FormatInt(baseTime.Add(5*time.Second).Unix(), 10),
			},
			body: `{"message":"API rate limit exceeded"}`,
			want: 6 * time.Second,
		},
		"secondary conservative fallback": {
			status: http.StatusForbidden,
			body:   `{"message":"You have exceeded a secondary rate limit"}`,
			want:   time.Minute,
		},
		"retry after seconds": {
			status:  http.StatusTooManyRequests,
			headers: map[string]string{"Retry-After": "7"},
			body:    `{"message":"slow down"}`,
			want:    7 * time.Second,
		},
		"secondary ignores primary reset window": {
			status: http.StatusForbidden,
			headers: map[string]string{
				"Retry-After":           "7",
				"X-RateLimit-Remaining": "42",
				"X-RateLimit-Reset":     strconv.FormatInt(baseTime.Add(time.Hour).Unix(), 10),
			},
			body: `{"message":"secondary rate limit"}`,
			want: 7 * time.Second,
		},
		"retry after HTTP date": {
			status: http.StatusTooManyRequests,
			headers: map[string]string{
				"Retry-After": baseTime.Add(9 * time.Second).Format(http.TimeFormat),
			},
			body: `{"message":"slow down"}`,
			want: 9 * time.Second,
		},
		"zero retry after uses conservative fallback": {
			status:  http.StatusTooManyRequests,
			headers: map[string]string{"Retry-After": "0"},
			body:    `{"message":"slow down"}`,
			want:    time.Minute,
		},
		"past retry after date uses conservative fallback": {
			status: http.StatusTooManyRequests,
			headers: map[string]string{
				"Retry-After": baseTime.Add(-time.Second).Format(http.TimeFormat),
			},
			body: `{"message":"slow down"}`,
			want: time.Minute,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			requests := 0

			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				requests++

				writer.Header().Set("Content-Type", "application/json")

				if requests == 1 {
					for key, value := range test.headers {
						writer.Header().Set(key, value)
					}

					writer.WriteHeader(test.status)
					writeResponse(t, writer, test.body)

					return
				}

				writeResponse(t, writer, `{"croft":"bothy"}`)
			}))
			defer server.Close()

			var (
				now   = baseTime
				waits []time.Duration
			)

			collector := GitHubCollector{
				Client: server.Client(), BaseURL: server.URL, Now: func() time.Time { return now },
				Wait: func(_ context.Context, delay time.Duration) error {
					waits = append(waits, delay)
					now = now.Add(delay)

					return nil
				},
				MaxElapsed: time.Hour, MaxRequests: 10, MaxRetries: 2,
			}

			var target map[string]string
			if err := collector.get(context.Background(), "/braw", &target); err != nil {
				t.Fatal(err)
			}

			if requests != 2 || len(waits) != 1 || waits[0] != test.want || target["croft"] != "bothy" {
				t.Fatalf("requests = %d, waits = %v, target = %#v; want one %s wait and complete retry", requests, waits, target, test.want)
			}
		})
	}
}

func TestGitHubRateLimitClassificationAndMalformedMetadata(t *testing.T) {
	classifications := map[string]struct {
		headers map[string]string
		want    string
	}{
		"primary": {
			headers: map[string]string{"X-RateLimit-Remaining": "0"},
			want:    "primary",
		},
		"secondary": {
			headers: map[string]string{"X-RateLimit-Remaining": "42"},
			want:    "secondary",
		},
	}

	for name, test := range classifications {
		t.Run(name, func(t *testing.T) {
			response := &http.Response{StatusCode: http.StatusTooManyRequests, Header: make(http.Header)}
			for key, value := range test.headers {
				response.Header.Set(key, value)
			}

			limit, limited, err := parseRateLimit(response, nil, time.Now())
			if err != nil || !limited || limit.kind != test.want {
				t.Fatalf("parseRateLimit() = %#v, %t, %v; want %s classification", limit, limited, err, test.want)
			}
		})
	}

	malformed := map[string]map[string]string{
		"remaining":   {"X-RateLimit-Remaining": "dreich"},
		"reset":       {"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": "thrawn"},
		"reset range": {"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": "9223372036854775807"},
		"retry after": {"Retry-After": "-1"},
		"overflow":    {"Retry-After": "9223372036854775807"},
	}

	for name, headers := range malformed {
		t.Run("malformed "+name, func(t *testing.T) {
			response := &http.Response{StatusCode: http.StatusTooManyRequests, Header: make(http.Header)}
			for key, value := range headers {
				response.Header.Set(key, value)
			}

			if _, _, err := parseRateLimit(response, nil, time.Now()); err == nil {
				t.Fatalf("parseRateLimit() accepted malformed %s metadata", name)
			}
		})
	}
}

func TestGitHubCollectorDefaultsAreFinite(t *testing.T) {
	collector, err := (GitHubCollector{}).configured()
	if err != nil {
		t.Fatal(err)
	}

	if collector.MaxElapsed != DefaultCollectionMaxElapsed ||
		collector.MaxRequests != DefaultCollectionMaxRequests ||
		collector.MaxRetries != DefaultCollectionMaxRetries ||
		collector.MaxElapsed <= 0 || collector.MaxRequests <= 0 || collector.MaxRetries <= 0 {
		t.Fatalf(
			"configured limits = %s, %d, %d; want finite defaults %s, %d, %d",
			collector.MaxElapsed, collector.MaxRequests, collector.MaxRetries,
			DefaultCollectionMaxElapsed, DefaultCollectionMaxRequests, DefaultCollectionMaxRetries,
		)
	}
}

func TestGitHubCollectorRateLimitRetryExhaustion(t *testing.T) {
	requests := 0

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests++

		writer.Header().Set("Retry-After", "1")
		http.Error(writer, "secondary rate limit", http.StatusTooManyRequests)
	}))
	defer server.Close()

	collector := GitHubCollector{
		Client: server.Client(), BaseURL: server.URL, MaxElapsed: time.Minute, MaxRequests: 10, MaxRetries: 1,
		Wait: func(context.Context, time.Duration) error {
			return nil
		},
	}

	var target map[string]string

	err := collector.get(context.Background(), "/canny", &target)
	if !errors.Is(err, ErrGitHubRateLimited) || !strings.Contains(err.Error(), "retry limit 1 exhausted") {
		t.Fatalf("get() error = %v, want rate-limit retry exhaustion", err)
	}

	if requests != 2 {
		t.Fatalf("requests = %d, want initial request plus one retry", requests)
	}
}

func TestGitHubCollectorBoundsMetadataFreeRateLimitBackoff(t *testing.T) {
	requests := 0

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests++
		if requests <= 2 {
			http.Error(writer, "secondary rate limit", http.StatusForbidden)

			return
		}

		writeResponse(t, writer, `{}`)
	}))
	defer server.Close()

	var (
		now   = time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
		waits []time.Duration
	)

	collector := GitHubCollector{
		Client: server.Client(), BaseURL: server.URL, Now: func() time.Time { return now },
		MaxElapsed: 5 * time.Minute, MaxRequests: 3, MaxRetries: 2,
		Wait: func(_ context.Context, delay time.Duration) error {
			waits = append(waits, delay)
			now = now.Add(delay)

			return nil
		},
	}

	var target map[string]any
	if err := collector.get(context.Background(), "/bairn", &target); err != nil {
		t.Fatal(err)
	}

	if requests != 3 || !reflect.DeepEqual(waits, []time.Duration{time.Minute, 2 * time.Minute}) {
		t.Fatalf("requests = %d, waits = %v; want bounded exponential waits", requests, waits)
	}
}

func TestGitHubCollectorBudgets(t *testing.T) {
	t.Run("request budget", func(t *testing.T) {
		requests := 0

		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			requests++

			writer.Header().Set("Retry-After", "1")
			http.Error(writer, "secondary rate limit", http.StatusTooManyRequests)
		}))
		defer server.Close()

		collector := GitHubCollector{
			Client: server.Client(), BaseURL: server.URL, MaxElapsed: time.Minute, MaxRequests: 1, MaxRetries: 1,
		}

		var target map[string]any
		if err := collector.get(context.Background(), "/canny", &target); !errors.Is(err, ErrCollectionBudgetExhausted) ||
			!strings.Contains(err.Error(), "request limit 1 reached") {
			t.Fatalf("get() error = %v, want request-budget exhaustion", err)
		}

		if requests != 1 {
			t.Fatalf("requests = %d, want retry blocked by request budget", requests)
		}
	})

	t.Run("elapsed budget", func(t *testing.T) {
		now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)

		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			now = now.Add(2 * time.Minute)

			writeResponse(t, writer, `{}`)
		}))
		defer server.Close()

		collector := GitHubCollector{
			Client: server.Client(), BaseURL: server.URL, Now: func() time.Time { return now },
			MaxElapsed: time.Minute, MaxRequests: 2, MaxRetries: 1,
		}

		var target map[string]any
		if err := collector.get(context.Background(), "/dreich", &target); !errors.Is(err, ErrCollectionBudgetExhausted) ||
			!strings.Contains(err.Error(), "elapsed limit") {
			t.Fatalf("get() error = %v, want elapsed-budget exhaustion", err)
		}
	})

	t.Run("rate-limit wait exceeds elapsed budget", func(t *testing.T) {
		waitCalled := false

		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Retry-After", "120")
			http.Error(writer, "secondary rate limit", http.StatusTooManyRequests)
		}))
		defer server.Close()

		collector := GitHubCollector{
			Client: server.Client(), BaseURL: server.URL, MaxElapsed: time.Minute, MaxRequests: 2, MaxRetries: 1,
			Wait: func(context.Context, time.Duration) error {
				waitCalled = true

				return nil
			},
		}

		var target map[string]any
		if err := collector.get(context.Background(), "/strath", &target); !errors.Is(err, ErrCollectionBudgetExhausted) ||
			!strings.Contains(err.Error(), "required wait 2m0s exceeds remaining time") {
			t.Fatalf("get() error = %v, want rate-limit wait budget exhaustion", err)
		}

		if waitCalled {
			t.Fatal("collector waited after proving the delay could not fit in the elapsed budget")
		}
	})
}

func TestGitHubCollectorCancellationDuringRateLimitWait(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Retry-After", "30")
		http.Error(writer, "secondary rate limit", http.StatusTooManyRequests)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	collector := GitHubCollector{
		Client: server.Client(), BaseURL: server.URL, MaxElapsed: time.Minute, MaxRequests: 2, MaxRetries: 1,
		Wait: func(waitCtx context.Context, _ time.Duration) error {
			cancel()
			<-waitCtx.Done()

			return waitCtx.Err()
		},
	}

	var target map[string]any

	err := collector.get(ctx, "/blether", &target)
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "collection cancelled") {
		t.Fatalf("get() error = %v, want cancellation during wait", err)
	}
}

func TestGitHubCollectorDoesNotMisclassifyTransportTimeoutAsCancellation(t *testing.T) {
	transportErr := fmt.Errorf("transport timeout: %w", context.DeadlineExceeded)
	collector := GitHubCollector{
		Client: &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return nil, transportErr
		})},
		BaseURL: "https://api.github.test", MaxElapsed: time.Minute, MaxRequests: 1, MaxRetries: 1,
	}

	var target map[string]any

	err := collector.get(context.Background(), "/timeout", &target)
	if !errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "collection cancelled") {
		t.Fatalf("get() error = %v, want distinct transport timeout", err)
	}
}

func TestGitHubCollectorClassifiesCancellationDuringResponseBodyRead(t *testing.T) {
	tests := map[string]struct {
		callerCancellation bool
		statusCode         int
		want               string
		wantBudget         bool
	}{
		"caller cancellation": {
			callerCancellation: true,
			statusCode:         http.StatusOK,
			want:               "GitHub collection cancelled",
		},
		"elapsed context cancellation reading error body": {
			statusCode: http.StatusInternalServerError,
			want:       "elapsed limit",
			wantBudget: true,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			parent := context.Background()
			requestContext, cancel := context.WithCancel(context.Background())

			if test.callerCancellation {
				parent = requestContext
			}

			collector := GitHubCollector{
				Client: &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode: test.statusCode,
						Status:     http.StatusText(test.statusCode),
						Header:     make(http.Header),
						Body:       &cancelingReadCloser{cancel: cancel, err: context.Canceled},
					}, nil
				})},
				BaseURL: "https://api.github.test",
				budget: &collectionBudget{
					parent: parent, now: time.Now, wait: waitForContext, startedAt: time.Now(),
					maxElapsed: time.Minute, maxRequests: 1, maxRetries: 1,
				},
			}

			var target map[string]any

			err := collector.get(requestContext, "/body-cancellation", &target)
			if err == nil || !strings.Contains(err.Error(), test.want) ||
				errors.Is(err, ErrCollectionBudgetExhausted) != test.wantBudget {
				t.Fatalf("get() error = %v, want %q with budget=%t", err, test.want, test.wantBudget)
			}
		})
	}
}

func TestFetchCachesPaginatesAcrossRateLimitRetry(t *testing.T) {
	pageOneRequests := 0

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")

		page := request.URL.Query().Get("page")
		if page == "1" {
			pageOneRequests++
			if pageOneRequests == 1 {
				writer.Header().Set("Retry-After", "1")
				http.Error(writer, "secondary rate limit", http.StatusTooManyRequests)

				return
			}
		}

		response := githubCachesPage{TotalCount: 101}

		switch page {
		case "1":
			for index := 1; index <= 100; index++ {
				response.Caches = append(response.Caches, githubCache{ID: int64(index), Key: fmt.Sprintf("croft-%d", index)})
			}
		case "2":
			response.Caches = []githubCache{{ID: 101, Key: "bothy"}}
		default:
			t.Errorf("unexpected page %q", page)
		}

		if err := json.NewEncoder(writer).Encode(response); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}))
	defer server.Close()

	collector := GitHubCollector{
		Client: server.Client(), BaseURL: server.URL, MaxElapsed: time.Minute, MaxRequests: 4, MaxRetries: 1,
		Wait: func(context.Context, time.Duration) error {
			return nil
		},
	}

	caches, expected, err := collector.fetchCaches(context.Background(), "d0ugal/graith")
	if err != nil {
		t.Fatal(err)
	}

	if pageOneRequests != 3 || expected != 101 || len(caches) != 101 || caches[100].Key != "bothy" {
		t.Fatalf("page one requests = %d, expected = %d, caches = %d; pagination lost data across retry", pageOneRequests, expected, len(caches))
	}
}

func TestGitHubCollectorReturnsNoPartialSnapshotAfterRateLimitExhaustion(t *testing.T) {
	pageTwoRequests := 0

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")

		if request.URL.Query().Get("page") == "2" {
			pageTwoRequests++

			writer.Header().Set("Retry-After", "1")
			http.Error(writer, "secondary rate limit", http.StatusTooManyRequests)

			return
		}

		page := githubRunsPage{TotalCount: 101, Runs: make([]githubRun, 100)}
		for index := range page.Runs {
			page.Runs[index].PullRequests = []githubPullRequest{}
		}

		if err := json.NewEncoder(writer).Encode(page); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}))
	defer server.Close()

	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	collector := GitHubCollector{
		Client: server.Client(), BaseURL: server.URL, Now: func() time.Time { return now },
		MaxElapsed: time.Minute, MaxRequests: 3, MaxRetries: 1,
		Wait: func(context.Context, time.Duration) error {
			return nil
		},
	}

	snapshot, err := collector.Fetch(context.Background(), "d0ugal/graith", now.Add(-2*time.Hour), loadInventory(t))
	if !errors.Is(err, ErrGitHubRateLimited) {
		t.Fatalf("Fetch() error = %v, want exhausted rate limit", err)
	}

	if !reflect.DeepEqual(snapshot, GitHubSnapshot{}) || pageTwoRequests != 2 {
		t.Fatalf("Fetch() returned partial snapshot %#v after %d page-two requests", snapshot, pageTwoRequests)
	}
}

func TestGitHubCollectorRejectsMalformedOrIncompleteResponses(t *testing.T) {
	tests := map[string]string{
		"incomplete JSON": `{"croft":`,
		"trailing JSON":   `{} {}`,
	}

	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writeResponse(t, writer, body)
			}))
			defer server.Close()

			collector := GitHubCollector{
				Client: server.Client(), BaseURL: server.URL, MaxElapsed: time.Minute, MaxRequests: 1, MaxRetries: 1,
			}

			var target map[string]any
			if err := collector.get(context.Background(), "/thrawn", &target); err == nil ||
				!strings.Contains(err.Error(), "malformed") {
				t.Fatalf("get() error = %v, want malformed/incomplete response rejection", err)
			}
		})
	}
}

func TestGitHubResponseObjectsRequireAuthoritativeFields(t *testing.T) {
	tests := map[string]struct {
		body   string
		target func() any
	}{
		"null runs page": {
			body:   `null`,
			target: func() any { return &githubRunsPage{} },
		},
		"empty runs page": {
			body:   `{}`,
			target: func() any { return &githubRunsPage{} },
		},
		"runs missing count": {
			body:   `{"workflow_runs":[]}`,
			target: func() any { return &githubRunsPage{} },
		},
		"runs missing collection": {
			body:   `{"total_count":0}`,
			target: func() any { return &githubRunsPage{} },
		},
		"null jobs collection": {
			body:   `{"total_count":0,"jobs":null}`,
			target: func() any { return &githubJobsPage{} },
		},
		"artifacts missing collection": {
			body:   `{"total_count":0}`,
			target: func() any { return &githubArtifactsPage{} },
		},
		"caches missing collection": {
			body:   `{"total_count":0}`,
			target: func() any { return &githubCachesPage{} },
		},
		"timing missing billable": {
			body:   `{}`,
			target: func() any { return &githubTiming{} },
		},
		"timing null billable": {
			body:   `{"billable":null}`,
			target: func() any { return &githubTiming{} },
		},
		"billable usage missing total": {
			body:   `{"billable":{"UBUNTU":{}}}`,
			target: func() any { return &githubTiming{} },
		},
		"artifact missing size": {
			body: `{
				"id":1,"name":"braw","expired":false,
				"created_at":null,"updated_at":null,"expires_at":null
			}`,
			target: func() any { return &githubArtifact{} },
		},
		"artifact missing expired": {
			body: `{
				"id":1,"name":"braw","size_in_bytes":0,
				"created_at":null,"updated_at":null,"expires_at":null
			}`,
			target: func() any { return &githubArtifact{} },
		},
		"cache missing size": {
			body: `{
				"id":1,"key":"braw","ref":"refs/heads/main",
				"created_at":"2026-07-25T12:00:00Z","last_accessed_at":"2026-07-25T12:00:00Z"
			}`,
			target: func() any { return &githubCache{} },
		},
		"run missing pull requests": {
			body: `{
				"id":1,"run_attempt":1,"path":".github/workflows/ci.yml","event":"push",
				"head_sha":"0123456789012345678901234567890123456789","head_branch":"main",
				"created_at":"2026-07-25T12:00:00Z","run_started_at":null,
				"updated_at":"2026-07-25T12:00:01Z","status":"completed","conclusion":"success"
			}`,
			target: func() any { return &githubRun{} },
		},
		"job missing labels": {
			body: `{
				"id":1,"name":"braw","status":"completed","conclusion":"success",
				"created_at":"2026-07-25T12:00:00Z","started_at":"2026-07-25T12:00:00Z",
				"completed_at":"2026-07-25T12:00:01Z","runner_name":"canny",
				"runner_group_name":"bothy"
			}`,
			target: func() any { return &githubJob{} },
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if err := json.Unmarshal([]byte(test.body), test.target()); err == nil {
				t.Fatalf("json.Unmarshal(%s) accepted structurally incomplete response", test.body)
			}
		})
	}
}

func TestGitHubCollectorRejectsDuplicateRunIDsAcrossStablePagination(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")

		page := githubRunsPage{TotalCount: 101}

		switch request.URL.Query().Get("page") {
		case "1":
			page.Runs = make([]githubRun, 100)
			for index := range page.Runs {
				page.Runs[index] = matureGitHubRun(
					int64(index+1),
					time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC),
					time.Date(2026, 7, 25, 10, 1, 0, 0, time.UTC),
				)
			}
		case "2":
			page.Runs = []githubRun{matureGitHubRun(
				100,
				time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC),
				time.Date(2026, 7, 25, 10, 1, 0, 0, time.UTC),
			)}
		default:
			t.Fatalf("unexpected request %s", request.URL.String())
		}

		if err := json.NewEncoder(writer).Encode(page); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}))
	defer server.Close()

	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	collector := GitHubCollector{
		Client: server.Client(), BaseURL: server.URL, Now: func() time.Time { return now },
		MaxElapsed: time.Minute, MaxRequests: 2, MaxRetries: 1,
	}

	snapshot, err := collector.Fetch(context.Background(), "d0ugal/graith", now.Add(-2*time.Hour), loadInventory(t))
	if err == nil || !strings.Contains(err.Error(), "duplicate raw workflow run ID 100") {
		t.Fatalf("Fetch() error = %v, want stable-count duplicate rejection", err)
	}

	if !reflect.DeepEqual(snapshot, GitHubSnapshot{}) {
		t.Fatalf("Fetch() returned partial snapshot %#v", snapshot)
	}
}

type githubPaginationTestCase struct {
	path string
	call func(*testing.T, GitHubCollector) error
}

func githubPaginationTestCases(now time.Time) map[string]githubPaginationTestCase {
	return map[string]githubPaginationTestCase{
		"caches": {
			path: "/repos/d0ugal/graith/actions/caches",
			call: func(t *testing.T, collector GitHubCollector) error {
				caches, _, err := collector.fetchCaches(context.Background(), "d0ugal/graith")
				if err != nil && caches != nil {
					t.Fatalf("fetchCaches() returned partial caches %#v", caches)
				}

				return err
			},
		},
		"jobs": {
			path: "/repos/d0ugal/graith/actions/runs/81/attempts/1/jobs",
			call: func(t *testing.T, collector GitHubCollector) error {
				jobs, _, err := collector.fetchJobs(context.Background(), "d0ugal/graith", 81, 1)
				if err != nil && jobs != nil {
					t.Fatalf("fetchJobs() returned partial jobs %#v", jobs)
				}

				return err
			},
		},
		"artifacts": {
			path: "/repos/d0ugal/graith/actions/runs/81/artifacts",
			call: func(t *testing.T, collector GitHubCollector) error {
				artifacts, _, err := collector.fetchArtifacts(context.Background(), "d0ugal/graith", 81)
				if err != nil && artifacts != nil {
					t.Fatalf("fetchArtifacts() returned partial artifacts %#v", artifacts)
				}

				return err
			},
		},
	}
}

func TestGitHubPaginationStopsAtAuthoritativeCount(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	tests := githubPaginationTestCases(now)

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			requests := 0

			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				requests++

				if request.URL.Path != test.path {
					t.Fatalf("request path = %q, want %q", request.URL.Path, test.path)
				}

				ids := make([]int64, 100)
				for index := range ids {
					ids[index] = int64(index + 1)
				}

				writeGitHubIdentityPage(t, writer, name, 100, ids)
			}))
			defer server.Close()

			collector := GitHubCollector{
				Client: server.Client(), BaseURL: server.URL, Now: func() time.Time { return now },
				MaxElapsed: time.Hour, MaxRequests: 10, MaxRetries: 1,
			}

			err := test.call(t, collector)
			if err == nil || !strings.Contains(err.Error(), "returned more") {
				t.Fatalf("%s error = %v, want authoritative-count over-delivery rejection", name, err)
			}

			if requests != 2 {
				t.Fatalf("%s requests = %d, want rejection on page 2", name, requests)
			}
		})
	}
}

func TestGitHubPaginationConsistencyPassRejectsReplacementChurn(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	tests := githubPaginationTestCases(now)

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			requests := 0

			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				requests++

				if request.URL.Path != test.path {
					t.Fatalf("request path = %q, want %q", request.URL.Path, test.path)
				}

				offset := int64(0)
				if requests > 2 {
					offset = 1
				}

				var ids []int64

				switch request.URL.Query().Get("page") {
				case "1":
					ids = make([]int64, 100)
					for index := range ids {
						ids[index] = int64(index+1) + offset
					}
				case "2":
					ids = []int64{101 + offset}
				default:
					t.Fatalf("unexpected request %s", request.URL.String())
				}

				writeGitHubIdentityPage(t, writer, name, 101, ids)
			}))
			defer server.Close()

			collector := GitHubCollector{
				Client: server.Client(), BaseURL: server.URL, Now: func() time.Time { return now },
				MaxElapsed: time.Hour, MaxRequests: 10, MaxRetries: 1,
			}

			err := test.call(t, collector)
			if err == nil || !strings.Contains(err.Error(), "identities or states changed during pagination consistency pass") {
				t.Fatalf("%s error = %v, want replacement-churn rejection", name, err)
			}

			if requests != 4 {
				t.Fatalf("%s requests = %d, want two complete bounded passes", name, requests)
			}
		})
	}
}

func TestGitHubPaginationConsistencyPassUsesSharedRequestBudget(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	since := now.Add(-2 * time.Hour)
	stable := matureGitHubRun(81, since.Add(10*time.Minute), now.Add(-70*time.Minute))

	server := newRunCollectionServer(t, []githubRunsPage{
		{TotalCount: 1, Runs: []githubRun{stable}},
		{TotalCount: 1, Runs: []githubRun{stable}},
	})
	defer server.Close()

	collector := GitHubCollector{
		Client: server.Client(), BaseURL: server.URL, Now: func() time.Time { return now },
		MaxElapsed: time.Hour, MaxRequests: 5, MaxRetries: 1,
		MaturationDelay: time.Hour,
	}

	snapshot, err := collector.Fetch(context.Background(), "d0ugal/graith", since, loadInventory(t))
	if !errors.Is(err, ErrCollectionBudgetExhausted) || !strings.Contains(err.Error(), "request limit 5 reached") {
		t.Fatalf("Fetch() error = %v, want final consistency-pass request-budget exhaustion", err)
	}

	if !reflect.DeepEqual(snapshot, GitHubSnapshot{}) {
		t.Fatalf("Fetch() returned partial snapshot %#v", snapshot)
	}
}

func writeGitHubIdentityPage(t *testing.T, writer http.ResponseWriter, kind string, total int, ids []int64) {
	t.Helper()

	writer.Header().Set("Content-Type", "application/json")

	switch kind {
	case "workflow runs":
		page := githubRunsPage{TotalCount: total, Runs: make([]githubRun, 0, len(ids))}
		for _, id := range ids {
			page.Runs = append(page.Runs, githubRun{ID: id, PullRequests: []githubPullRequest{}})
		}

		if err := json.NewEncoder(writer).Encode(page); err != nil {
			t.Errorf("encode workflow runs: %v", err)
		}
	case "caches":
		page := githubCachesPage{TotalCount: total, Caches: make([]githubCache, 0, len(ids))}
		for _, id := range ids {
			page.Caches = append(page.Caches, githubCache{ID: id})
		}

		if err := json.NewEncoder(writer).Encode(page); err != nil {
			t.Errorf("encode caches: %v", err)
		}
	case "jobs":
		page := githubJobsPage{TotalCount: total, Jobs: make([]githubJob, 0, len(ids))}
		for _, id := range ids {
			page.Jobs = append(page.Jobs, githubJob{ID: id, Labels: []string{}})
		}

		if err := json.NewEncoder(writer).Encode(page); err != nil {
			t.Errorf("encode jobs: %v", err)
		}
	case "artifacts":
		page := githubArtifactsPage{TotalCount: total, Artifacts: make([]githubArtifact, 0, len(ids))}
		for _, id := range ids {
			page.Artifacts = append(page.Artifacts, githubArtifact{ID: id})
		}

		if err := json.NewEncoder(writer).Encode(page); err != nil {
			t.Errorf("encode artifacts: %v", err)
		}
	default:
		t.Fatalf("unknown identity page kind %q", kind)
	}
}
