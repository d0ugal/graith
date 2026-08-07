package docspreview

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultGitHubAPI = "https://api.github.com"

type GitHubHTTPClient struct {
	Client  *http.Client
	BaseURL string
	Token   string
}

func (client *GitHubHTTPClient) GetRef(ctx context.Context, owner, repo, ref string) (Ref, error) {
	var response struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}

	err := client.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/git/ref/%s", escape(owner), escape(repo), pathRef(ref)), nil, nil, &response)
	if err != nil {
		return Ref{}, err
	}

	return Ref{SHA: response.Object.SHA}, nil
}

func (client *GitHubHTTPClient) CreateRef(ctx context.Context, owner, repo, ref, sha string) error {
	return client.do(
		ctx,
		http.MethodPost,
		fmt.Sprintf("/repos/%s/%s/git/refs", escape(owner), escape(repo)),
		nil,
		struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		}{Ref: ref, SHA: sha},
		nil,
	)
}

func (client *GitHubHTTPClient) UpdateRef(ctx context.Context, owner, repo, ref, sha string) error {
	return client.do(
		ctx,
		http.MethodPatch,
		fmt.Sprintf("/repos/%s/%s/git/refs/%s", escape(owner), escape(repo), pathRef(ref)),
		nil,
		struct {
			SHA string `json:"sha"`
		}{SHA: sha},
		nil,
	)
}

func (client *GitHubHTTPClient) GetCommit(ctx context.Context, owner, repo, sha string) (Commit, error) {
	var response struct {
		SHA  string `json:"sha"`
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
		Parents []struct {
			SHA string `json:"sha"`
		} `json:"parents"`
		Message string `json:"message"`
	}

	err := client.do(
		ctx,
		http.MethodGet,
		fmt.Sprintf("/repos/%s/%s/git/commits/%s", escape(owner), escape(repo), escape(sha)),
		nil,
		nil,
		&response,
	)
	if err != nil {
		return Commit{}, err
	}

	parents := make([]string, 0, len(response.Parents))
	for _, parent := range response.Parents {
		parents = append(parents, parent.SHA)
	}

	return Commit{SHA: response.SHA, TreeSHA: response.Tree.SHA, Parents: parents, Message: response.Message}, nil
}

func (client *GitHubHTTPClient) GetTree(ctx context.Context, owner, repo, sha string, recursive bool) (Tree, error) {
	query := url.Values{}
	if recursive {
		query.Set("recursive", "true")
	}

	var response struct {
		SHA       string      `json:"sha"`
		Truncated bool        `json:"truncated"`
		Tree      []TreeEntry `json:"tree"`
	}

	err := client.do(
		ctx,
		http.MethodGet,
		fmt.Sprintf("/repos/%s/%s/git/trees/%s", escape(owner), escape(repo), escape(sha)),
		query,
		nil,
		&response,
	)
	if err != nil {
		return Tree{}, err
	}

	return Tree{SHA: response.SHA, Entries: response.Tree, Truncated: response.Truncated}, nil
}

func (client *GitHubHTTPClient) CreateTree(ctx context.Context, owner, repo string, entries []TreeEntry, baseTree string) (string, error) {
	payload := struct {
		Tree     []TreeEntry `json:"tree"`
		BaseTree string      `json:"base_tree,omitempty"`
	}{Tree: entries, BaseTree: baseTree}

	var response struct {
		SHA string `json:"sha"`
	}

	err := client.do(
		ctx,
		http.MethodPost,
		fmt.Sprintf("/repos/%s/%s/git/trees", escape(owner), escape(repo)),
		nil,
		payload,
		&response,
	)
	if err != nil {
		return "", err
	}

	return response.SHA, nil
}

func (client *GitHubHTTPClient) CreateCommit(ctx context.Context, owner, repo string, message, tree string, parents []string) (string, error) {
	payload := struct {
		Message string   `json:"message"`
		Tree    string   `json:"tree"`
		Parents []string `json:"parents"`
	}{Message: message, Tree: tree, Parents: parents}

	var response struct {
		SHA string `json:"sha"`
	}

	err := client.do(
		ctx,
		http.MethodPost,
		fmt.Sprintf("/repos/%s/%s/git/commits", escape(owner), escape(repo)),
		nil,
		payload,
		&response,
	)
	if err != nil {
		return "", err
	}

	return response.SHA, nil
}

func (client *GitHubHTTPClient) CreateBlob(ctx context.Context, owner, repo string, content, encoding string) (string, error) {
	payload := struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}{Content: content, Encoding: encoding}

	var response struct {
		SHA string `json:"sha"`
	}

	err := client.do(
		ctx,
		http.MethodPost,
		fmt.Sprintf("/repos/%s/%s/git/blobs", escape(owner), escape(repo)),
		nil,
		payload,
		&response,
	)
	if err != nil {
		return "", err
	}

	return response.SHA, nil
}

func (client *GitHubHTTPClient) ListComments(ctx context.Context, owner, repo string, issueNumber int) ([]Comment, error) {
	query := url.Values{"per_page": []string{"100"}}
	next := ""
	endpoint := fmt.Sprintf("/repos/%s/%s/issues/%d/comments", escape(owner), escape(repo), issueNumber)

	var comments []Comment

	for {
		var page []struct {
			ID   int64  `json:"id"`
			Body string `json:"body"`
		}

		headers, err := client.doWithURL(ctx, http.MethodGet, endpoint, next, query, nil, &page)
		if err != nil {
			return nil, err
		}

		for _, comment := range page {
			comments = append(comments, Comment{ID: comment.ID, Body: comment.Body})
		}

		next = nextLink(headers.Get("Link"))
		if next == "" {
			return comments, nil
		}

		query = nil
	}
}

func (client *GitHubHTTPClient) UpdateComment(ctx context.Context, owner, repo string, commentID int64, body string) error {
	return client.do(
		ctx,
		http.MethodPatch,
		fmt.Sprintf("/repos/%s/%s/issues/comments/%d", escape(owner), escape(repo), commentID),
		nil,
		struct {
			Body string `json:"body"`
		}{Body: body},
		nil,
	)
}

func (client *GitHubHTTPClient) CreateComment(ctx context.Context, owner, repo string, issueNumber int, body string) error {
	return client.do(
		ctx,
		http.MethodPost,
		fmt.Sprintf("/repos/%s/%s/issues/%d/comments", escape(owner), escape(repo), issueNumber),
		nil,
		struct {
			Body string `json:"body"`
		}{Body: body},
		nil,
	)
}

func (client *GitHubHTTPClient) DeleteComment(ctx context.Context, owner, repo string, commentID int64) error {
	return client.do(
		ctx,
		http.MethodDelete,
		fmt.Sprintf("/repos/%s/%s/issues/comments/%d", escape(owner), escape(repo), commentID),
		nil,
		nil,
		nil,
	)
}

func (client *GitHubHTTPClient) do(ctx context.Context, method, endpoint string, query url.Values, payload, output any) error {
	_, err := client.doWithURL(ctx, method, endpoint, "", query, payload, output)
	return err
}

func (client *GitHubHTTPClient) doWithURL(ctx context.Context, method, endpoint, absoluteURL string, query url.Values, payload, output any) (http.Header, error) {
	requestURL, err := client.requestURL(endpoint, absoluteURL, query)
	if err != nil {
		return nil, err
	}

	var body io.Reader

	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}

		body = bytes.NewReader(data)
	}

	request, err := http.NewRequestWithContext(ctx, method, requestURL, body)
	if err != nil {
		return nil, err
	}

	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	if client.Token != "" {
		request.Header.Set("Authorization", "Bearer "+client.Token)
	}

	httpClient := client.Client
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}

	response, err := httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode < 200 || response.StatusCode > 299 {
		return response.Header, decodeStatusError(response.StatusCode, response.Body)
	}

	if output == nil {
		_, err = io.Copy(io.Discard, response.Body)
		return response.Header, err
	}

	decoder := json.NewDecoder(response.Body)
	if err := decoder.Decode(output); err != nil && !errors.Is(err, io.EOF) {
		return response.Header, err
	}

	return response.Header, nil
}

func (client *GitHubHTTPClient) requestURL(endpoint, absoluteURL string, query url.Values) (string, error) {
	if absoluteURL != "" {
		parsed, err := url.Parse(absoluteURL)
		if err != nil {
			return "", err
		}

		if query != nil {
			parsed.RawQuery = query.Encode()
		}

		return parsed.String(), nil
	}

	base := client.BaseURL
	if base == "" {
		base = defaultGitHubAPI
	}

	parsed, err := url.Parse(base)
	if err != nil {
		return "", err
	}

	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/" + strings.TrimLeft(endpoint, "/")
	if query != nil {
		parsed.RawQuery = query.Encode()
	}

	return parsed.String(), nil
}

func decodeStatusError(status int, body io.Reader) error {
	data, err := io.ReadAll(io.LimitReader(body, 1<<20))
	if err != nil {
		return err
	}

	var decoded struct {
		Message string `json:"message"`
	}

	message := strings.TrimSpace(string(data))
	if err := json.Unmarshal(data, &decoded); err == nil && decoded.Message != "" {
		message = decoded.Message
	}

	return &StatusError{Status: status, Message: message}
}

func escape(value string) string {
	return url.PathEscape(value)
}

func pathRef(ref string) string {
	parts := strings.Split(ref, "/")
	for i, part := range parts {
		parts[i] = escape(part)
	}

	return strings.Join(parts, "/")
}

func nextLink(header string) string {
	for _, raw := range strings.Split(header, ",") {
		part := strings.TrimSpace(raw)

		linkEnd := strings.Index(part, ">")
		if !strings.HasPrefix(part, "<") || linkEnd == -1 {
			continue
		}

		target := part[1:linkEnd]
		for _, parameter := range strings.Split(part[linkEnd+1:], ";") {
			if strings.TrimSpace(parameter) == `rel="next"` {
				return target
			}
		}
	}

	return ""
}
