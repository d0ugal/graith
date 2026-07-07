package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUnknownKeysFromTOML(t *testing.T) {
	tests := []struct {
		name     string
		toml     string
		wantKey  string // FullKey expected present
		wantSugg string // Suggestion for that key ("" = no suggestion asserted)
	}{
		{
			name: "typo in sandbox read_dirs",
			toml: `
[sandbox]
enabled = true
read_dir = ["/home/braw"]
`,
			wantKey:  "sandbox.read_dir",
			wantSugg: "read_dirs",
		},
		{
			name: "typo under nested agent sandbox table",
			toml: `
[agents.claude.sandbox]
wrtie_files = ["/home/braw/.claude.json"]
`,
			wantKey:  "agents.claude.sandbox.wrtie_files",
			wantSugg: "write_files",
		},
		{
			name: "unknown top-level key",
			toml: `
feature = true
`,
			wantKey: "feature",
		},
		{
			name: "unknown key in array of tables",
			toml: `
[[repos]]
path = "~/Code/croft"
singletn = true
`,
			wantKey:  "repos.singletn",
			wantSugg: "singleton",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := unknownKeysFromTOML([]byte(tt.toml))
			if err != nil {
				t.Fatalf("unknownKeysFromTOML: %v", err)
			}

			var match *UnknownKey

			for i := range got {
				if got[i].FullKey() == tt.wantKey {
					match = &got[i]
					break
				}
			}

			if match == nil {
				t.Fatalf("expected unknown key %q, got %+v", tt.wantKey, got)
			}

			if tt.wantSugg != "" && match.Suggestion != tt.wantSugg {
				t.Errorf("suggestion for %q = %q, want %q", tt.wantKey, match.Suggestion, tt.wantSugg)
			}
		})
	}
}

func TestUnknownKeysAcceptsValidConfig(t *testing.T) {
	// Exercises the false-positive-prone shapes: dynamic-key maps (agents.<name>,
	// env, per-agent mcp_servers.<name>), pointer-struct tables (sandbox.network),
	// arrays of tables ([[repos]], [[mcp_servers]]), and a case-variant key
	// (READ_DIRS) that go-toml accepts and so must not be flagged.
	toml := `
default_agent = "claude"
github_username = "braw-lad"
branch_prefix = "{username}/graith"

[sandbox]
enabled = true
backend = "nono"
READ_DIRS = ["/etc"]
write_dirs = ["/tmp/bothy"]

[sandbox.network]
block = true
allow_domains = ["example.com"]

[agents.claude.sandbox]
write_files = ["/home/braw/.claude.json"]

[agents.claude.env]
FOO = "bar"
ANY_USER_KEY = "value"

[agents.claude.mcp_servers.braw]
command = "npx"

[agents.claude.mcp_servers.braw.env]
PORT = "9222"

[[mcp_servers]]
name = "chrome-devtools"
command = "npx"

[mcp_servers.env]
WHATEVER = "ok"

[[repos]]
path = "~/Code/croft"
singleton = true
includes = ["shared.env"]
`

	got, err := unknownKeysFromTOML([]byte(toml))
	if err != nil {
		t.Fatalf("unknownKeysFromTOML: %v", err)
	}

	if len(got) != 0 {
		t.Fatalf("expected no unknown keys for valid config, got %+v", got)
	}
}

func TestUnknownKeysNoSuggestionForDistantKey(t *testing.T) {
	// A genuinely new key from a newer graith (not a typo of any existing key)
	// must be reported as unknown WITHOUT a misleading "did you mean". The
	// closestKey threshold is capped so long, unrelated keys get no suggestion.
	got, err := unknownKeysFromTOML([]byte("experimental_telemetry_pipeline = true\n"))
	if err != nil {
		t.Fatalf("unknownKeysFromTOML: %v", err)
	}

	if len(got) != 1 || got[0].FullKey() != "experimental_telemetry_pipeline" {
		t.Fatalf("expected one unknown key, got %+v", got)
	}

	if got[0].Suggestion != "" {
		t.Errorf("expected no suggestion for distant key, got %q", got[0].Suggestion)
	}
}

func TestUnknownKeysTypoUnderPointerStructTable(t *testing.T) {
	// sandbox.network is a *SandboxNetworkConfig pointer field — a typo under it
	// must still be caught, with a suggestion.
	toml := `
[sandbox.network]
allow_domian = ["example.com"]
`

	got, err := unknownKeysFromTOML([]byte(toml))
	if err != nil {
		t.Fatalf("unknownKeysFromTOML: %v", err)
	}

	var match *UnknownKey

	for i := range got {
		if got[i].FullKey() == "sandbox.network.allow_domian" {
			match = &got[i]
			break
		}
	}

	if match == nil {
		t.Fatalf("expected sandbox.network.allow_domian, got %+v", got)
	}

	if match.Suggestion != "allow_domains" {
		t.Errorf("suggestion = %q, want %q", match.Suggestion, "allow_domains")
	}
}

func TestUnknownKeysDedupesArrayOfTables(t *testing.T) {
	// The same typo across two [[repos]] blocks should be reported once.
	toml := `
[[repos]]
path = "~/Code/croft"
singletn = true

[[repos]]
path = "~/Code/bothy"
singletn = false
`

	got, err := unknownKeysFromTOML([]byte(toml))
	if err != nil {
		t.Fatalf("unknownKeysFromTOML: %v", err)
	}

	count := 0

	for _, u := range got {
		if u.FullKey() == "repos.singletn" {
			count++
		}
	}

	if count != 1 {
		t.Fatalf("expected repos.singletn reported once, got %d (%+v)", count, got)
	}
}

func TestUnknownKeysReadsFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	if err := os.WriteFile(path, []byte("singal_mode = \"observe\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := UnknownKeys(path)
	if err != nil {
		t.Fatalf("UnknownKeys: %v", err)
	}

	if len(got) != 1 || got[0].FullKey() != "singal_mode" {
		t.Fatalf("expected one unknown key singal_mode, got %+v", got)
	}
}

func TestLevenshtein(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"read_dir", "read_dirs", 1},
		{"wrtie_files", "write_files", 2},
		{"", "abc", 3},
		{"same", "same", 0},
	}

	for _, c := range cases {
		if got := levenshtein(c.a, c.b); got != c.want {
			t.Errorf("levenshtein(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
