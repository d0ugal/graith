package config

import (
	"strings"
	"testing"
)

func TestNotificationInstructionsMatchAndRenderInOrder(t *testing.T) {
	rules := []NotificationInstructionRule{
		{
			Name:     "braw-review-guidance",
			Kinds:    []string{"github_pr_review"},
			Owners:   []string{"croft"},
			Authors:  []string{"alice"},
			Template: "Review guidance for {{repo}} by {{author}} on PR #{{pr_number}}.",
		},
		{
			Name:     "canny-repo-guidance",
			Repos:    []string{"portal"},
			Template: "Session {{session_name}} is working in {{session_repo}}.",
		},
		{
			Name:     "dreich-other-kind",
			Kinds:    []string{"github_ci_failure"},
			Template: "This should not render.",
		},
	}

	ctx := NotificationInstructionContext{
		Kind:        "github_pr_review",
		Repo:        "croft/portal",
		Authors:     []string{"Bob", "Alice"},
		PRNumber:    42,
		URL:         "https://github.com/croft/portal/pull/42",
		SessionName: "braw-agent",
		SessionRepo: "croft/portal",
	}

	got, err := RenderNotificationInstructions(rules, ctx)
	if err != nil {
		t.Fatalf("RenderNotificationInstructions: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("matched rules = %d, want 2: %+v", len(got), got)
	}

	if got[0].Name != "braw-review-guidance" || !strings.Contains(got[0].Text, "croft/portal by Bob, Alice on PR #42") {
		t.Fatalf("first rendered instruction = %+v", got[0])
	}

	if got[1].Name != "canny-repo-guidance" || !strings.Contains(got[1].Text, "Session braw-agent is working in croft/portal") {
		t.Fatalf("second rendered instruction = %+v", got[1])
	}
}

func TestNotificationInstructionsNoMatch(t *testing.T) {
	rules := []NotificationInstructionRule{{
		Name:     "braw-review-guidance",
		Kinds:    []string{"github_pr_review"},
		Repos:    []string{"croft/portal"},
		Authors:  []string{"alice"},
		Template: "Review guidance.",
	}}

	got, err := RenderNotificationInstructions(rules, NotificationInstructionContext{
		Kind:    "github_pr_comment",
		Repo:    "croft/portal",
		Authors: []string{"alice"},
	})
	if err != nil {
		t.Fatalf("RenderNotificationInstructions: %v", err)
	}

	if len(got) != 0 {
		t.Fatalf("matched rules = %+v, want none", got)
	}
}

func TestNotificationInstructionsTemplateDataDelimitersRemainData(t *testing.T) {
	rules := []NotificationInstructionRule{{
		Name:     "braw-session-guidance",
		Kinds:    []string{"github_pr_comment"},
		Template: "Session data: {{session_name}} in {{session_repo}}.",
	}}

	got, err := RenderNotificationInstructions(rules, NotificationInstructionContext{
		Kind:        "github_pr_comment",
		SessionName: "braw {{not_a_var}}",
		SessionRepo: "croft/{{portal}}",
	})
	if err != nil {
		t.Fatalf("RenderNotificationInstructions: %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("matched rules = %d, want 1: %+v", len(got), got)
	}

	for _, want := range []string{"braw { {not_a_var} }", "croft/{ {portal} }"} {
		if !strings.Contains(got[0].Text, want) {
			t.Fatalf("rendered text missing sanitized data %q: %q", want, got[0].Text)
		}
	}
}

func TestNotificationInstructionsOwnerMatch(t *testing.T) {
	rules := []NotificationInstructionRule{{
		Name:     "braw-owner-guidance",
		Owners:   []string{"croft"},
		Template: "Owner guidance.",
	}}

	tests := map[string]struct {
		ctx       NotificationInstructionContext
		wantMatch bool
	}{
		"repo owner matches": {
			ctx:       NotificationInstructionContext{Repo: "croft/portal"},
			wantMatch: true,
		},
		"session repo owner fallback matches": {
			ctx:       NotificationInstructionContext{SessionRepo: "croft/portal"},
			wantMatch: true,
		},
		"bare repo has no owner": {
			ctx:       NotificationInstructionContext{Repo: "portal"},
			wantMatch: false,
		},
		"different owner does not match": {
			ctx:       NotificationInstructionContext{Repo: "strath/portal"},
			wantMatch: false,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := RenderNotificationInstructions(rules, test.ctx)
			if err != nil {
				t.Fatalf("RenderNotificationInstructions: %v", err)
			}

			if matched := len(got) > 0; matched != test.wantMatch {
				t.Fatalf("matched = %v, want %v: %+v", matched, test.wantMatch, got)
			}
		})
	}
}

func TestNotificationInstructionsSessionRepoMatch(t *testing.T) {
	rules := []NotificationInstructionRule{
		{
			Name:         "braw-session-repo-guidance",
			SessionRepos: []string{"croft/portal"},
			Template:     "Full repo guidance.",
		},
		{
			Name:         "canny-session-repo-guidance",
			SessionRepos: []string{"portal"},
			Template:     "Basename guidance.",
		},
	}

	got, err := RenderNotificationInstructions(rules, NotificationInstructionContext{SessionRepo: "croft/portal"})
	if err != nil {
		t.Fatalf("RenderNotificationInstructions: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("matched rules = %d, want 2: %+v", len(got), got)
	}
}

func TestNotificationInstructionValidation(t *testing.T) {
	tests := map[string]struct {
		toml    string
		wantErr string
	}{
		"valid rule loads": {
			toml: `
[[notification_instruction]]
name = "braw-review-guidance"
kinds = ["github_pr_comment"]
owners = ["croft"]
repos = ["croft/portal"]
authors = ["alice"]
template = "Reviewer feedback guidance for {{repo}}."
`,
		},
		"unsupported field rejected": {
			toml: `
[[notification_instruction]]
name = "braw-review-guidance"
kinds = ["github_pr_comment"]
branches = ["main"]
template = "Reviewer feedback guidance."
`,
			wantErr: "unsupported condition or field",
		},
		"unknown template variable rejected": {
			toml: `
[[notification_instruction]]
name = "braw-review-guidance"
kinds = ["github_pr_comment"]
template = "Reviewer feedback guidance: {{body}}"
`,
			wantErr: "unknown template variable",
		},
		"unknown kind rejected": {
			toml: `
[[notification_instruction]]
name = "braw-review-guidance"
kinds = ["github_pr_comments"]
template = "Reviewer feedback guidance."
`,
			wantErr: "unsupported notification kind",
		},
		"malformed template rejected": {
			toml: `
[[notification_instruction]]
name = "braw-review-guidance"
kinds = ["github_pr_comment"]
template = "Reviewer feedback guidance: {{repo"
`,
			wantErr: "malformed template variable",
		},
		"empty matcher rejected": {
			toml: `
[[notification_instruction]]
name = "braw-review-guidance"
template = "Reviewer feedback guidance."
`,
			wantErr: "at least one of kinds",
		},
		"blank match value rejected": {
			toml: `
[[notification_instruction]]
name = "braw-review-guidance"
kinds = [""]
template = "Reviewer feedback guidance."
`,
			wantErr: "value must not be empty",
		},
		"owner path rejected": {
			toml: `
[[notification_instruction]]
name = "braw-review-guidance"
owners = ["croft/portal"]
template = "Reviewer feedback guidance."
`,
			wantErr: "not owner/repo",
		},
		"repo leading slash rejected": {
			toml: `
[[notification_instruction]]
name = "braw-review-guidance"
repos = ["/portal"]
template = "Reviewer feedback guidance."
`,
			wantErr: "no leading",
		},
		"repo empty path segment rejected": {
			toml: `
	[[notification_instruction]]
	name = "braw-review-guidance"
	repos = ["croft//portal"]
template = "Reviewer feedback guidance."
	`,
			wantErr: "no leading",
		},
		"repo multi segment path rejected": {
			toml: `
	[[notification_instruction]]
	name = "braw-review-guidance"
	repos = ["croft/portal/bothy"]
	template = "Reviewer feedback guidance."
	`,
			wantErr: "at most one slash",
		},
		"session repo invalid path rejected": {
			toml: `
	[[notification_instruction]]
	name = "braw-review-guidance"
	session_repos = ["/portal"]
	template = "Reviewer feedback guidance."
	`,
			wantErr: "no leading",
		},
		"excessive template rejected": {
			toml: `
	[[notification_instruction]]
name = "braw-review-guidance"
kinds = ["github_pr_comment"]
template = "` + strings.Repeat("a", NotificationInstructionTemplateMaxBytes+1) + `"
`,
			wantErr: "template must be",
		},
		"duplicate names rejected": {
			toml: `
[[notification_instruction]]
name = "braw-review-guidance"
kinds = ["github_pr_comment"]
template = "One."

[[notification_instruction]]
name = "BRAW-review-guidance"
kinds = ["github_pr_review"]
template = "Two."
`,
			wantErr: "duplicates notification_instruction",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := LoadBytes("config.toml", []byte(test.toml))
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("LoadBytes: %v", err)
				}

				return
			}

			if err == nil {
				t.Fatalf("LoadBytes: expected error containing %q", test.wantErr)
			}

			if !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("LoadBytes error = %v, want substring %q", err, test.wantErr)
			}
		})
	}
}

func TestNotificationInstructionDefaultsEmpty(t *testing.T) {
	if got := len(Default().NotificationRules); got != 0 {
		t.Fatalf("Default().NotificationRules length = %d, want 0", got)
	}
}
