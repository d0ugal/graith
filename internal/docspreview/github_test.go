package docspreview

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGitHubHTTPClientRequests(t *testing.T) {
	t.Parallel()

	var requests []recordedRequest

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		defer func() {
			_ = request.Body.Close()
		}()

		var body map[string]any
		if request.Body != nil {
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil && err.Error() != "EOF" {
				t.Errorf("decode request body: %v", err)
				http.Error(writer, "bad request body", http.StatusInternalServerError)

				return
			}
		}

		requests = append(requests, recordedRequest{
			Method:        request.Method,
			Path:          request.URL.Path,
			RawQuery:      request.URL.RawQuery,
			Authorization: request.Header.Get("Authorization"),
			Body:          body,
		})

		writer.Header().Set("Content-Type", "application/json")

		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/repos/clachan/croft/git/ref/heads/screenshots":
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write([]byte(`{"object":{"sha":"tip-commit"}}`))
		case request.Method == http.MethodGet && request.URL.Path == "/repos/clachan/croft/git/commits/tip-commit":
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write([]byte(`{"sha":"tip-commit","message":"tip","tree":{"sha":"tree-sha"},"parents":[{"sha":"parent-sha"}]}`))
		case request.Method == http.MethodGet && request.URL.Path == "/repos/clachan/croft/git/trees/tree-sha":
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write([]byte(`{"sha":"tree-sha","truncated":false,"tree":[{"path":"pr-1/braw.png","mode":"100644","type":"blob","sha":"blob-sha"}]}`))
		case request.Method == http.MethodPost && request.URL.Path == "/repos/clachan/croft/git/trees":
			writer.WriteHeader(http.StatusCreated)
			_, _ = writer.Write([]byte(`{"sha":"new-tree"}`))
		case request.Method == http.MethodPost && request.URL.Path == "/repos/clachan/croft/git/commits":
			writer.WriteHeader(http.StatusCreated)
			_, _ = writer.Write([]byte(`{"sha":"new-commit"}`))
		case request.Method == http.MethodPost && request.URL.Path == "/repos/clachan/croft/git/blobs":
			writer.WriteHeader(http.StatusCreated)
			_, _ = writer.Write([]byte(`{"sha":"blob-sha"}`))
		case request.Method == http.MethodPost && request.URL.Path == "/repos/clachan/croft/git/refs":
			writer.WriteHeader(http.StatusCreated)
			_, _ = writer.Write([]byte(`{"ref":"refs/heads/screenshots"}`))
		case request.Method == http.MethodPatch && request.URL.Path == "/repos/clachan/croft/git/refs/heads/screenshots":
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write([]byte(`{"ref":"refs/heads/screenshots"}`))
		case request.Method == http.MethodPatch && request.URL.Path == "/repos/clachan/croft/issues/comments/99":
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write([]byte(`{"id":99}`))
		case request.Method == http.MethodPost && request.URL.Path == "/repos/clachan/croft/issues/42/comments":
			writer.WriteHeader(http.StatusCreated)
			_, _ = writer.Write([]byte(`{"id":100}`))
		default:
			t.Errorf("unexpected request %s %s", request.Method, request.URL.String())
			http.Error(writer, "unexpected request", http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	client := &GitHubHTTPClient{Client: server.Client(), BaseURL: server.URL, Token: "canny-token"}

	ref, err := client.GetRef(context.Background(), "clachan", "croft", "heads/"+ScreenshotsBranch)
	if err != nil {
		t.Fatal(err)
	}

	if ref.SHA != "tip-commit" {
		t.Fatalf("ref SHA = %q, want tip-commit", ref.SHA)
	}

	commit, err := client.GetCommit(context.Background(), "clachan", "croft", ref.SHA)
	if err != nil {
		t.Fatal(err)
	}

	if commit.TreeSHA != "tree-sha" || !equalStrings(commit.Parents, []string{"parent-sha"}) {
		t.Fatalf("commit = %+v, want tree and parent", commit)
	}

	tree, err := client.GetTree(context.Background(), "clachan", "croft", "tree-sha", true)
	if err != nil {
		t.Fatal(err)
	}

	if !equalStrings(entryPaths(tree.Entries), []string{"pr-1/braw.png"}) {
		t.Fatalf("tree entries = %+v", tree.Entries)
	}

	if _, err := client.CreateTree(context.Background(), "clachan", "croft", []TreeEntry{blobEntry("pr-2/canny.png")}, ""); err != nil {
		t.Fatal(err)
	}

	if _, err := client.CreateCommit(context.Background(), "clachan", "croft", "docs preview", "new-tree", []string{}); err != nil {
		t.Fatal(err)
	}

	if _, err := client.CreateBlob(context.Background(), "clachan", "croft", "aW1hZ2U=", "base64"); err != nil {
		t.Fatal(err)
	}

	if err := client.CreateRef(context.Background(), "clachan", "croft", "refs/heads/"+ScreenshotsBranch, "new-commit"); err != nil {
		t.Fatal(err)
	}

	if err := client.UpdateRef(context.Background(), "clachan", "croft", "heads/"+ScreenshotsBranch, "commit-sha"); err != nil {
		t.Fatal(err)
	}

	if err := client.UpdateComment(context.Background(), "clachan", "croft", 99, "updated body"); err != nil {
		t.Fatal(err)
	}

	if err := client.CreateComment(context.Background(), "clachan", "croft", 42, "new body"); err != nil {
		t.Fatal(err)
	}

	if len(requests) != 10 {
		t.Fatalf("requests = %+v, want 10", requests)
	}

	if got := requests[0].Path; got != "/repos/clachan/croft/git/ref/heads/screenshots" {
		t.Fatalf("getRef path = %q", got)
	}

	if got := requests[2].RawQuery; got != "recursive=true" {
		t.Fatalf("getTree query = %q, want recursive=true", got)
	}

	for _, request := range requests {
		if request.Authorization != "Bearer canny-token" {
			t.Fatalf("%s %s Authorization = %q", request.Method, request.Path, request.Authorization)
		}
	}

	if _, ok := requests[3].Body["base_tree"]; ok {
		t.Fatalf("createTree body included base_tree: %+v", requests[3].Body)
	}

	parents, ok := requests[4].Body["parents"].([]any)
	if !ok || len(parents) != 0 {
		t.Fatalf("createCommit parents = %#v, want empty array", requests[4].Body["parents"])
	}

	if requests[5].Body["content"] != "aW1hZ2U=" || requests[5].Body["encoding"] != "base64" {
		t.Fatalf("createBlob body = %+v, want base64 content", requests[5].Body)
	}

	if requests[6].Body["ref"] != "refs/heads/screenshots" {
		t.Fatalf("createRef body = %+v, want screenshots ref", requests[6].Body)
	}

	if _, ok := requests[7].Body["force"]; ok {
		t.Fatalf("updateRef body included force: %+v", requests[7].Body)
	}

	if requests[8].Body["body"] != "updated body" || requests[9].Body["body"] != "new body" {
		t.Fatalf("comment bodies = %+v / %+v", requests[8].Body, requests[9].Body)
	}
}

func TestGitHubHTTPClientListCommentsPaginates(t *testing.T) {
	t.Parallel()

	var pages []string

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		defer func() {
			_ = request.Body.Close()
		}()

		pages = append(pages, request.URL.RawQuery)

		writer.Header().Set("Content-Type", "application/json")

		if strings.Contains(request.URL.RawQuery, "page=2") {
			_, _ = writer.Write([]byte(`[{"id":2,"body":"canny"}]`))
			return
		}

		writer.Header().Set(
			"Link",
			`<`+serverURL(request)+`/repos/clachan/croft/issues/42/comments?page=2>; rel="next"`,
		)
		_, _ = writer.Write([]byte(`[{"id":1,"body":"braw"}]`))
	}))
	defer server.Close()

	client := &GitHubHTTPClient{Client: server.Client(), BaseURL: server.URL}

	comments, err := client.ListComments(context.Background(), "clachan", "croft", 42)
	if err != nil {
		t.Fatal(err)
	}

	if len(comments) != 2 || comments[0].ID != 1 || comments[1].ID != 2 {
		t.Fatalf("comments = %+v, want both pages", comments)
	}

	if !strings.Contains(pages[0], "per_page=100") || pages[1] != "page=2" {
		t.Fatalf("page queries = %v, want per_page then next link", pages)
	}
}

func TestGitHubHTTPClientGetBranchTipTreatsMissingRefAsAbsent(t *testing.T) {
	t.Parallel()

	var commitLookups int

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")

		if request.Method == http.MethodGet && request.URL.Path == "/repos/clachan/croft/git/ref/heads/screenshots" {
			writer.WriteHeader(http.StatusNotFound)
			_, _ = writer.Write([]byte(`{"message":"Not Found"}`))

			return
		}

		if strings.Contains(request.URL.Path, "/git/commits/") {
			commitLookups++
		}

		t.Errorf("unexpected request %s %s", request.Method, request.URL.String())
		http.Error(writer, "unexpected request", http.StatusInternalServerError)
	}))
	defer server.Close()

	client := &GitHubHTTPClient{Client: server.Client(), BaseURL: server.URL}

	tip, err := getBranchTip(context.Background(), client, testRepo)
	if err != nil {
		t.Fatal(err)
	}

	if tip != nil || commitLookups != 0 {
		t.Fatalf("tip = %+v commitLookups = %d, want absent branch without commit lookup", tip, commitLookups)
	}
}

func TestGitHubHTTPClientErrorIncludesGitHubMessage(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = writer.Write([]byte(`{"message":"Reference update failed"}`))
	}))
	defer server.Close()

	client := &GitHubHTTPClient{Client: server.Client(), BaseURL: server.URL}

	err := client.UpdateRef(context.Background(), "clachan", "croft", "heads/"+ScreenshotsBranch, "commit-sha")
	if err == nil || statusCode(err) != 422 || !strings.Contains(err.Error(), "Reference update failed") {
		t.Fatalf("UpdateRef() error = %v, want 422 with GitHub message", err)
	}
}

type recordedRequest struct {
	Method        string
	Path          string
	RawQuery      string
	Authorization string
	Body          map[string]any
}

func serverURL(request *http.Request) string {
	scheme := "http"
	if request.TLS != nil {
		scheme = "https"
	}

	return scheme + "://" + request.Host
}
