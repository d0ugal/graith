package daemon

import (
	"context"
	"testing"
)

func TestParseGitHubRemote(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want string
		ok   bool
	}{
		{"ssh croft", "git@github.com:croft/loch.git", "croft/loch", true},
		{"https croft", "https://github.com/croft/loch.git", "croft/loch", true},
		{"https no suffix", "https://github.com/croft/loch", "croft/loch", true},
		{"ssh no suffix", "git@github.com:croft/loch", "croft/loch", true},
		{"non-github glen", "git@gitlab.com:croft/glen.git", "", false},
		{"empty haar", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := parseGitHubRemote(c.url)
			if ok != c.ok || got != c.want {
				t.Errorf("parseGitHubRemote(%q) = (%q,%v), want (%q,%v)", c.url, got, ok, c.want, c.ok)
			}
		})
	}
}

func TestNormalizePRState(t *testing.T) {
	cases := []struct {
		state   string
		isDraft bool
		want    string
	}{
		{"OPEN", false, "open"},
		{"OPEN", true, "draft"},
		{"MERGED", false, "merged"},
		{"CLOSED", false, "closed"},
		{"open", true, "draft"},
	}
	for _, c := range cases {
		if got := normalizePRState(c.state, c.isDraft); got != c.want {
			t.Errorf("normalizePRState(%q,%v) = %q, want %q", c.state, c.isDraft, got, c.want)
		}
	}
}

func TestCIBucket(t *testing.T) {
	// NEUTRAL/SKIPPED/CANCELLED must be pass-like — they must never wake an agent.
	cases := []struct {
		name  string
		check prCheck
		want  string
	}{
		{"bucket fail", prCheck{Bucket: "fail"}, "fail"},
		{"bucket pending", prCheck{Bucket: "pending"}, "pending"},
		{"bucket skipping is pass", prCheck{Bucket: "skipping"}, "pass"},
		{"bucket cancel is pass", prCheck{Bucket: "cancel"}, "pass"},
		{"state FAILURE", prCheck{State: "FAILURE"}, "fail"},
		{"state TIMED_OUT", prCheck{State: "TIMED_OUT"}, "fail"},
		{"state NEUTRAL is pass", prCheck{State: "NEUTRAL"}, "pass"},
		{"state SKIPPED is pass", prCheck{State: "SKIPPED"}, "pass"},
		{"state IN_PROGRESS", prCheck{State: "IN_PROGRESS"}, "pending"},
		{"state SUCCESS", prCheck{State: "SUCCESS"}, "pass"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ciBucket(c.check); got != c.want {
				t.Errorf("ciBucket(%+v) = %q, want %q", c.check, got, c.want)
			}
		})
	}
}

func TestFetchChecksAggregate(t *testing.T) {
	orig := ghRunner
	defer func() { ghRunner = orig }()

	cases := []struct {
		name      string
		json      string
		wantState string
		wantFail  []string
	}{
		{
			name:      "braw all pass",
			json:      `[{"name":"build","bucket":"pass"},{"name":"lint","bucket":"pass"}]`,
			wantState: "passing",
		},
		{
			name:      "thrawn one fail",
			json:      `[{"name":"build","bucket":"pass"},{"name":"lint","bucket":"fail"}]`,
			wantState: "failing",
			wantFail:  []string{"lint"},
		},
		{
			name:      "neep skipped is not a failure",
			json:      `[{"name":"build","bucket":"pass"},{"name":"deploy","bucket":"skipping"}]`,
			wantState: "passing",
		},
		{
			name:      "haar pending",
			json:      `[{"name":"build","bucket":"pending"}]`,
			wantState: "pending",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ghRunner = func(ctx context.Context, dir string, args ...string) (string, error) {
				return c.json, nil
			}
			state, fail := fetchChecks(context.Background(), "croft/loch", 1, "")
			if state != c.wantState {
				t.Errorf("state = %q, want %q", state, c.wantState)
			}
			if len(fail) != len(c.wantFail) {
				t.Errorf("failing = %v, want %v", fail, c.wantFail)
			}
		})
	}
}

func TestResolvePRNoPR(t *testing.T) {
	orig := ghRunner
	defer func() { ghRunner = orig }()
	ghRunner = func(ctx context.Context, dir string, args ...string) (string, error) {
		return `[]`, nil
	}
	_, found, err := resolvePR(context.Background(), "croft/loch", "bide", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Error("expected found=false for empty pr list")
	}
}
