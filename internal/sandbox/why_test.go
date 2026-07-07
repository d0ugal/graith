package sandbox

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestWhyQueryValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		query   WhyQuery
		wantErr string
	}{
		{
			name:  "path read is valid",
			query: WhyQuery{Path: "/glen/bothy", Op: "read"},
		},
		{
			name:  "path readwrite is valid",
			query: WhyQuery{Path: "/glen/bothy", Op: "readwrite"},
		},
		{
			name:  "host only is valid",
			query: WhyQuery{Host: "github.com"},
		},
		{
			name:    "path without op is rejected",
			query:   WhyQuery{Path: "/glen/wynd"},
			wantErr: "--op is required",
		},
		{
			name:    "invalid op is rejected",
			query:   WhyQuery{Path: "/glen/wynd", Op: "thrawn"},
			wantErr: "invalid --op",
		},
		{
			name:    "both path and host is rejected",
			query:   WhyQuery{Path: "/glen/bothy", Op: "read", Host: "github.com"},
			wantErr: "not both",
		},
		{
			name:    "neither path nor host is rejected",
			query:   WhyQuery{},
			wantErr: "provide --path",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.query.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}

				return
			}

			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestNonoWhyArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		query WhyQuery
		want  []string
	}{
		{
			name:  "filesystem query",
			query: WhyQuery{Path: "/croft/bothy", Op: "write"},
			want:  []string{"why", "--json", "-p", "/tmp/kirk.json", "--path", "/croft/bothy", "--op", "write"},
		},
		{
			name:  "network query defaults port 443",
			query: WhyQuery{Host: "github.com"},
			want:  []string{"why", "--json", "-p", "/tmp/kirk.json", "--host", "github.com", "--port", "443"},
		},
		{
			name:  "network query explicit port",
			query: WhyQuery{Host: "loch.example", Port: 8443},
			want:  []string{"why", "--json", "-p", "/tmp/kirk.json", "--host", "loch.example", "--port", "8443"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := nonoWhyArgs("/tmp/kirk.json", tt.query)
			if !equalStrings(got, tt.want) {
				t.Fatalf("nonoWhyArgs() = %v, want %v", got, tt.want)
			}
		})
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}

func TestWhyForProfileAllowed(t *testing.T) {
	t.Parallel()

	run := func(_ string, args []string) (string, string, error) {
		// Assert the profile flag threads through.
		if !containsPair(args, "-p", "/tmp/braw.json") {
			t.Errorf("args missing profile: %v", args)
		}

		return `{"status":"allowed","reason":"granted_path","granted_path":"/croft/bothy","access":"read+write","source":"profile"}`, "", nil
	}

	res, err := whyForProfile("", "/tmp/braw.json", WhyQuery{Path: "/croft/bothy", Op: "readwrite"}, run)
	if err != nil {
		t.Fatalf("whyForProfile() error = %v", err)
	}

	if !res.Allowed() {
		t.Fatalf("Allowed() = false, want true")
	}

	if !strings.Contains(res.Explanation(), "allowed") || !strings.Contains(res.Explanation(), "profile") {
		t.Fatalf("Explanation() = %q, want allowed + source", res.Explanation())
	}
}

func TestWhyForProfileDenied(t *testing.T) {
	t.Parallel()

	run := func(_ string, _ []string) (string, string, error) {
		return `{"status":"denied","reason":"sensitive_path","details":"Blocked by policy group 'deny_credentials'","policy_source":"group:deny_credentials"}`, "", nil
	}

	res, err := whyForProfile("", "/tmp/dreich.json", WhyQuery{Path: "/hame/.ssh", Op: "read"}, run)
	if err != nil {
		t.Fatalf("whyForProfile() error = %v", err)
	}

	if res.Allowed() {
		t.Fatalf("Allowed() = true, want false")
	}

	exp := res.Explanation()
	if !strings.Contains(exp, "denied") || !strings.Contains(exp, "deny_credentials") {
		t.Fatalf("Explanation() = %q, want denied + group", exp)
	}
}

func TestWhyForProfileNetworkSuggestsFlag(t *testing.T) {
	t.Parallel()

	run := func(_ string, _ []string) (string, string, error) {
		return `{"status":"denied","reason":"network_blocked","details":"Network access is fully blocked.","suggested_flag":"--allow-domain github.com"}`, "", nil
	}

	res, err := whyForProfile("", "/tmp/haar.json", WhyQuery{Host: "github.com", Port: 443}, run)
	if err != nil {
		t.Fatalf("whyForProfile() error = %v", err)
	}

	if !strings.Contains(res.Explanation(), "--allow-domain github.com") {
		t.Fatalf("Explanation() = %q, want suggested flag", res.Explanation())
	}
}

func TestWhyForProfileValidatesBeforeRunning(t *testing.T) {
	t.Parallel()

	called := false
	run := func(_ string, _ []string) (string, string, error) {
		called = true

		return "", "", nil
	}

	_, err := whyForProfile("", "/tmp/fash.json", WhyQuery{Path: "/glen/wynd", Op: "thrawn"}, run)
	if err == nil {
		t.Fatalf("whyForProfile() = nil error, want validation error")
	}

	if called {
		t.Fatalf("nono was invoked despite invalid query")
	}
}

func TestWhyForProfileSurfacesStderrOnNonJSON(t *testing.T) {
	t.Parallel()

	run := func(_ string, _ []string) (string, string, error) {
		// nono prints arg errors to stderr and emits no JSON on stdout.
		return "", "error: invalid value 'scunner' for '--op <OP>'", errors.New("exit 2")
	}

	_, err := whyForProfile("", "/tmp/scunner.json", WhyQuery{Path: "/glen/wynd", Op: "read"}, run)
	if err == nil {
		t.Fatalf("whyForProfile() = nil error, want failure")
	}

	if !strings.Contains(err.Error(), "invalid value") {
		t.Fatalf("error = %v, want stderr surfaced", err)
	}
}

func TestWhyForProfileErrorsOnEmptyStatus(t *testing.T) {
	t.Parallel()

	run := func(_ string, _ []string) (string, string, error) {
		return `{"reason":"haar"}`, "", nil
	}

	_, err := whyForProfile("", "/tmp/haar.json", WhyQuery{Path: "/glen/bothy", Op: "read"}, run)
	if err == nil || !strings.Contains(err.Error(), "no decision status") {
		t.Fatalf("whyForProfile() = %v, want no-status error", err)
	}
}

func TestBuildQueryProfile(t *testing.T) {
	t.Parallel()

	worktree := t.TempDir()

	opts := WrapOpts{
		Backend:     BackendNono,
		WorktreeDir: worktree,
		ReadDirs:    []string{"/croft/shared"},
		EnvKeys:     []string{"PATH", "HOME"},
	}

	path, warnings, err := BuildQueryProfile(opts)
	if err != nil {
		t.Fatalf("BuildQueryProfile() error = %v", err)
	}

	defer func() { _ = os.Remove(path) }()

	if path == "" {
		t.Fatalf("BuildQueryProfile() returned empty path")
	}

	// A clean policy under a temp worktree should produce no warnings.
	if len(warnings) != 0 {
		t.Fatalf("BuildQueryProfile() warnings = %v, want none", warnings)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read profile: %v", err)
	}

	data := string(raw)
	if !strings.Contains(data, worktree) {
		t.Errorf("profile missing worktree grant: %s", data)
	}

	if !strings.Contains(data, "/croft/shared") {
		t.Errorf("profile missing read dir: %s", data)
	}

	if !strings.Contains(data, `"extends": "default"`) {
		t.Errorf("profile missing extends default: %s", data)
	}
}

// TestBuildQueryProfileRejectsReadOnlyUnderTmp: a read-only grant under a nono
// default-writable prefix cannot be enforced, so `gr sandbox why` (which reuses
// the profile emitter) must fail closed with the config error rather than emit a
// profile. Regression for issue #789.
func TestBuildQueryProfileRejectsReadOnlyUnderTmp(t *testing.T) {
	t.Parallel()

	opts := WrapOpts{
		Backend:     BackendNono,
		WorktreeDir: "/hame/user/bothy",
		ReadDirs:    []string{"/tmp/dreich-readonly"},
	}

	path, _, err := BuildQueryProfile(opts)
	if err == nil {
		_ = os.Remove(path)

		t.Fatal("BuildQueryProfile should reject a read-only dir under /tmp, got nil error")
	}

	if !strings.Contains(err.Error(), "/tmp/dreich-readonly") {
		t.Errorf("error should name the offending path, got: %v", err)
	}
}

func TestBuildQueryProfileWarnsUnmappedFeature(t *testing.T) {
	t.Parallel()

	opts := WrapOpts{
		Backend:     BackendNono,
		WorktreeDir: t.TempDir(),
		Features:    []string{"clipboard"},
	}

	path, warnings, err := BuildQueryProfile(opts)
	if err != nil {
		t.Fatalf("BuildQueryProfile() error = %v", err)
	}

	defer func() { _ = os.Remove(path) }()

	if len(warnings) == 0 {
		t.Fatalf("BuildQueryProfile() = no warnings, want unmapped-feature warning")
	}

	if !strings.Contains(strings.Join(warnings, " "), "clipboard") {
		t.Fatalf("warnings = %v, want clipboard mention", warnings)
	}
}

func containsPair(args []string, flag, val string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == val {
			return true
		}
	}

	return false
}
