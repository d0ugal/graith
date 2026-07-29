package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/output"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

type fakeConversationSearchUseCase struct {
	req  protocol.SearchMsg
	resp protocol.SearchResponseMsg
	err  error
}

func (fake *fakeConversationSearchUseCase) SearchConversations(req protocol.SearchMsg) (protocol.SearchResponseMsg, error) {
	fake.req = req
	if fake.err != nil {
		return protocol.SearchResponseMsg{}, fake.err
	}

	return fake.resp, nil
}

type fakeSearchSessionListUseCase struct {
	live    []protocol.SessionInfo
	deleted []protocol.SessionInfo
}

func (fake fakeSearchSessionListUseCase) ListSessions(deleted bool) ([]protocol.SessionInfo, error) {
	if deleted {
		return fake.deleted, nil
	}

	return fake.live, nil
}

func resetSearchFlagsForTest(t *testing.T) {
	t.Helper()

	oldSession, oldChildren, oldRepo, oldAgent := searchSession, searchChildren, searchRepo, searchAgent
	oldKinds, oldSince, oldUntil, oldState := searchKinds, searchSince, searchUntil, searchState
	oldDeleted, oldLimit, oldCursor, oldJSON := searchDeleted, searchLimit, searchCursor, jsonOutput

	t.Cleanup(func() {
		searchSession = oldSession
		searchChildren = oldChildren
		searchRepo = oldRepo
		searchAgent = oldAgent
		searchKinds = oldKinds
		searchSince = oldSince
		searchUntil = oldUntil
		searchState = oldState
		searchDeleted = oldDeleted
		searchLimit = oldLimit
		searchCursor = oldCursor
		jsonOutput = oldJSON
	})

	searchSession = ""
	searchChildren = false
	searchRepo = ""
	searchAgent = ""
	searchKinds = nil
	searchSince = ""
	searchUntil = ""
	searchState = "all"
	searchDeleted = false
	searchLimit = 0
	searchCursor = ""
	jsonOutput = false
}

func TestRunSearchBuildsRequestAndPrintsJSON(t *testing.T) {
	resetSearchFlagsForTest(t)

	searchSession = "braw"
	searchChildren = true
	searchRepo = "croft"
	searchAgent = "claude"
	searchKinds = []string{"user", "tool"}
	searchSince = "2026-07-29"
	searchUntil = "2026-07-30T10:00:00Z"
	searchState = "active"
	searchDeleted = true
	searchLimit = 7
	searchCursor = "2"
	jsonOutput = true

	var buf bytes.Buffer

	fakeSearch := &fakeConversationSearchUseCase{resp: protocol.SearchResponseMsg{
		Limit: 7,
		Results: []protocol.SearchResult{{
			SessionID: "braw-id", SessionName: "braw", Agent: "claude", Kind: "user",
			Snippet: "fix the bothy", Matches: []protocol.SearchMatchRange{{Start: 8, End: 13}},
			Locator: "s:braw-id:a:claude:n:sess:t:0",
		}},
	}}

	cmd := &cobra.Command{}
	cmd.SetOut(&buf)
	cmd.SetContext(withCommandDependencies(context.Background(), commandDependencies{
		out: output.NewWithWriter(true, &buf),
		listSession: fakeSearchSessionListUseCase{
			live:    []protocol.SessionInfo{{ID: "live", Name: "other"}},
			deleted: []protocol.SessionInfo{{ID: "braw-id", Name: "braw"}},
		},
		search: fakeSearch,
	}))

	if err := runSearch(cmd, []string{"fix", "bothy"}); err != nil {
		t.Fatalf("runSearch: %v", err)
	}

	wantReq := protocol.SearchMsg{
		Query: "fix bothy", SessionID: "braw-id", IncludeDescendants: true,
		Repo: "croft", Agent: "claude", Kinds: []string{"user", "tool"},
		Since: "2026-07-29", Until: "2026-07-30T10:00:00Z", State: "active",
		IncludeDeleted: true, Limit: 7, Cursor: "2",
	}
	if fakeSearch.req.Query != wantReq.Query || fakeSearch.req.SessionID != wantReq.SessionID ||
		fakeSearch.req.Repo != wantReq.Repo || fakeSearch.req.Agent != wantReq.Agent ||
		fakeSearch.req.State != wantReq.State || fakeSearch.req.Limit != wantReq.Limit ||
		fakeSearch.req.Cursor != wantReq.Cursor || !fakeSearch.req.IncludeDescendants ||
		!fakeSearch.req.IncludeDeleted {
		t.Fatalf("request = %+v, want %+v", fakeSearch.req, wantReq)
	}

	var got protocol.SearchResponseMsg
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal JSON: %v\n%s", err, buf.String())
	}

	if len(got.Results) != 1 || got.Results[0].SessionID != "braw-id" {
		t.Fatalf("JSON response = %+v, want braw result", got)
	}
}

func TestPrintSearchResultsHighlightsAndCursor(t *testing.T) {
	var buf bytes.Buffer

	printSearchResults(&buf, protocol.SearchResponseMsg{
		NextCursor: "eyJ2IjoxLCJvIjoyMH0",
		Results: []protocol.SearchResult{{
			SessionID: "braw-id", SessionName: "braw", RepoName: "croft", Agent: "claude", Kind: "assistant",
			Timestamp: "2026-07-29T10:00:00Z", Snippet: "fix the bothy",
			Matches: []protocol.SearchMatchRange{{Start: 8, End: 13}}, Locator: "s:braw-id:a:claude:n:sess:t:1",
		}},
		UnsupportedAgents: []protocol.SearchUnsupportedAgent{{Agent: "cursor", Count: 2}},
	})

	got := buf.String()
	for _, want := range []string{"braw (braw-id)", "claude/assistant", "fix the [bothy]", "cursor (2 sessions)", "--cursor"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output = %q, want substring %q", got, want)
		}
	}
}

func TestValidateSearchArgs(t *testing.T) {
	resetSearchFlagsForTest(t)

	if err := validateSearchArgs(nil, nil); err == nil {
		t.Fatal("missing query should fail")
	}

	searchChildren = true

	if err := validateSearchArgs(nil, []string{"braw"}); err == nil {
		t.Fatal("--children without --session should fail")
	}
}
