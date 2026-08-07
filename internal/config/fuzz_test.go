package config

import (
	"strings"
	"testing"
)

func FuzzExpand(f *testing.F) {
	f.Add("{username}/graith")
	f.Add("--session-id {agent_session_id}")
	f.Add("{session_name}-{session_id}")
	f.Add("--model {model}")
	f.Add("{unknown}")
	f.Add("literal {user-name} braces")
	f.Add("")

	vars := TemplateVars{
		Username:                 "braw-lad",
		AgentSessionID:           "abc-123",
		SessionName:              "canny-fix",
		SessionID:                "a3f2b1c9",
		WorktreePath:             "/tmp/bothy",
		ForkSourceAgentSessionID: "def-456",
		Model:                    "codex",
		Dir:                      "/tmp/croft",
		Profile:                  "braw",
		ReasoningEffort:          "medium",
		ServiceTier:              "auto",
		WebSearch:                true,
	}

	f.Fuzz(func(t *testing.T, input string) {
		if len(input) > 8192 {
			t.Skip()
		}

		got, err := Expand(input, vars)
		gotAgain, errAgain := Expand(input, vars)

		if (err == nil) != (errAgain == nil) || got != gotAgain {
			t.Fatalf("Expand(%q) was not deterministic: (%q, %v) then (%q, %v)", input, got, err, gotAgain, errAgain)
		}

		wantErr := hasUnknownTemplateToken(input, IsTemplateVar)
		if (err != nil) != wantErr {
			t.Fatalf("Expand(%q) error = %v, want error: %v", input, err, wantErr)
		}

		if err != nil {
			return
		}

		gotSlice, err := ExpandSlice([]string{input}, vars)
		if err != nil {
			t.Fatalf("ExpandSlice(%q) failed after Expand succeeded: %v", input, err)
		}

		if len(gotSlice) != 1 || gotSlice[0] != got {
			t.Fatalf("ExpandSlice(%q) = %#v, want [%q]", input, gotSlice, got)
		}
	})
}

func FuzzExpandTrigger(f *testing.F) {
	f.Add("report {name} on {date}")
	f.Add("{change_count} files in {session_name}")
	f.Add("at {worktree_path}: {changed_files}")
	f.Add("alert {gcx_event_id}")
	f.Add("body: {issue_body}")
	f.Add("{unknown_trigger}")
	f.Add("literal {user-name} braces")
	f.Add("")

	const issueBody = "Use the canny path with {worktree_path} and {gcx_event_url}."

	vars := TriggerVars{
		Name:             "canny-lint",
		Date:             "2026-07-11",
		Datetime:         "2026-07-11T09:00:00Z",
		FireTime:         "2026-07-11T09:00:00Z",
		SessionName:      "braw",
		WorktreePath:     "/tmp/bothy",
		ChangedFiles:     "glen/a.go, glen/b.go",
		ChangeCount:      "2",
		ScenarioID:       "sc-braw",
		ScenarioName:     "strath",
		CompletionEpoch:  "7",
		ResultIndex:      "results/index.json",
		IssueNumber:      "643",
		IssueTitle:       "Inspect the brig",
		IssueBody:        issueBody,
		IssueURL:         "https://example.invalid/issues/643",
		IssueLabels:      "braw,canny",
		GCXEventID:       "AG-BRAW",
		GCXEventKind:     "oncall_alert_group",
		GCXEventState:    "firing",
		GCXEventURL:      "https://example.invalid/alerts/ag-braw",
		GCXTeamID:        "team-braw",
		GCXIntegrationID: "int-canny",
		GCXStartedAt:     "2026-07-11T08:59:00Z",
	}

	f.Fuzz(func(t *testing.T, input string) {
		if len(input) > 8192 {
			t.Skip()
		}

		got, err := ExpandTrigger(input, vars)
		gotAgain, errAgain := ExpandTrigger(input, vars)

		if (err == nil) != (errAgain == nil) || got != gotAgain {
			t.Fatalf("ExpandTrigger(%q) was not deterministic: (%q, %v) then (%q, %v)", input, got, err, gotAgain, errAgain)
		}

		wantErr := hasUnknownTemplateToken(input, IsTriggerTemplateVar)
		if (err != nil) != wantErr {
			t.Fatalf("ExpandTrigger(%q) error = %v, want error: %v", input, err, wantErr)
		}

		if err != nil {
			if got != "" {
				t.Fatalf("ExpandTrigger(%q) returned %q with error %v, want empty output", input, got, err)
			}

			return
		}

		if strings.Contains(input, "{issue_body}") && !strings.Contains(got, issueBody) {
			t.Fatalf("ExpandTrigger(%q) = %q, want issue body left unexpanded as %q", input, got, issueBody)
		}
	})
}

func hasUnknownTemplateToken(input string, known func(string) bool) bool {
	for _, match := range varPattern.FindAllStringSubmatch(input, -1) {
		if len(match) == 2 && !known(match[1]) {
			return true
		}
	}

	return false
}
