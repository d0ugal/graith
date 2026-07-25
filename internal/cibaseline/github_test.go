package cibaseline

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGitHubCollectorFetchesReadOnlyEvidence(t *testing.T) {
	requests := make([]string, 0)
	timingGone := false
	timingNegative := false

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests = append(requests, request.Method+" "+request.URL.Path)
		if request.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", request.Method)
		}

		if request.URL.Path == "/repos/d0ugal/graith/actions/runs" && request.URL.Query().Get("status") != "completed" {
			t.Errorf("run status filter = %q, want completed", request.URL.Query().Get("status"))
		}

		if got := request.Header.Get("Authorization"); got != "Bearer canny-token" {
			t.Errorf("authorization = %q", got)
		}

		writer.Header().Set("Content-Type", "application/json")

		switch request.URL.Path {
		case "/repos/d0ugal/graith/actions/runs":
			writeResponse(t, writer, `{"total_count":1,"workflow_runs":[{"id":81,"run_attempt":1,"path":".github/workflows/ci.yml@refs/heads/canny/braw","event":"pull_request","head_sha":"braw","pull_requests":[{"number":1}],"created_at":"2026-07-25T10:00:00Z","run_started_at":"2026-07-25T10:00:10Z","updated_at":"2026-07-25T10:01:00Z","status":"completed","conclusion":"success"}]}`)
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
	}`), &artifact); err == nil || !strings.Contains(err.Error(), "missing required nullable field") {
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
			writeResponse(t, writer, `{"total_count":1,"jobs":[{"id":82,"name":"Lint","status":"completed","conclusion":"failure","created_at":"2026-07-25T10:00:00Z","started_at":"2026-07-25T10:00:10Z","completed_at":"2026-07-25T10:01:00Z"}]}`)
		case "/repos/d0ugal/graith/actions/runs/81/attempts/2/jobs":
			writeResponse(t, writer, `{"total_count":1,"jobs":[{"id":83,"name":"Lint","status":"completed","conclusion":"cancelled","created_at":"2026-07-25T10:05:00Z","started_at":"2026-07-25T10:05:10Z","completed_at":"2026-07-25T10:06:00Z"}]}`)
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

	snapshot, err := collector.Fetch(context.Background(), "d0ugal/graith", now.Add(-time.Hour), loadInventory(t))
	if err != nil {
		t.Fatal(err)
	}

	if len(snapshot.Runs) != 2 || snapshot.Runs[0].Conclusion != "failure" ||
		!snapshot.Runs[0].CreatedAt.Equal(time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC)) ||
		snapshot.Runs[1].Conclusion != "cancelled" {
		t.Fatalf("attempt-scoped snapshot = %#v", snapshot.Runs)
	}

	mismatchedAttemptPath = true

	if _, err := collector.Fetch(context.Background(), "d0ugal/graith", now.Add(-time.Hour), loadInventory(t)); err == nil ||
		!strings.Contains(err.Error(), "mismatched metadata") {
		t.Fatalf("Fetch() error = %v, want mismatched attempt identity rejection", err)
	}

	mismatchedAttemptPath = false
	omittedAttemptIdentity = true

	if _, err := collector.Fetch(context.Background(), "d0ugal/graith", now.Add(-time.Hour), loadInventory(t)); err == nil ||
		!strings.Contains(err.Error(), "mismatched metadata") {
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
	base.PullRequests = append(base.PullRequests, struct {
		Number int64 `json:"number"`
	}{Number: 7})

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
			next.PullRequests = append([]struct {
				Number int64 `json:"number"`
			}(nil), base.PullRequests...)
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
	if _, err := collector.Fetch(context.Background(), "d0ugal/graith", now.Add(-time.Hour), loadInventory(t)); err == nil ||
		!strings.Contains(err.Error(), "count mismatch") {
		t.Fatalf("Fetch() error = %v, want count mismatch", err)
	}
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
					response.Jobs = append(response.Jobs, githubJob{ID: int64(index), Name: fmt.Sprintf("braw-%d", index)})
				}
			} else {
				response.Jobs = []githubJob{{ID: 101, Name: "canny"}}
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
					page := githubCachesPage{TotalCount: total}
					for index := 0; index < count; index++ {
						page.Caches = append(page.Caches, githubCache{ID: int64(index + 1)})
					}

					if err := json.NewEncoder(writer).Encode(page); err != nil {
						t.Errorf("encode caches: %v", err)
					}
				case "jobs":
					page := githubJobsPage{TotalCount: total}
					for index := 0; index < count; index++ {
						page.Jobs = append(page.Jobs, githubJob{ID: int64(index + 1)})
					}

					if err := json.NewEncoder(writer).Encode(page); err != nil {
						t.Errorf("encode jobs: %v", err)
					}
				case "artifacts":
					page := githubArtifactsPage{TotalCount: total}
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
