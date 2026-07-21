package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/output"
	"github.com/d0ugal/graith/internal/protocol"
	"github.com/spf13/cobra"
)

func TestPrintPathPlainSessionTypes(t *testing.T) {
	types := []string{"worktree", "in-place", "repo-less scratch", "mirror scratch", "shared source", "orchestrator"}

	for _, sessionType := range types {
		t.Run(sessionType, func(t *testing.T) {
			cwd := filepath.Join(t.TempDir(), "bothy")
			if err := os.Mkdir(cwd, 0o700); err != nil {
				t.Fatal(err)
			}

			var buf bytes.Buffer

			session := &protocol.SessionInfo{ID: "abc123", Name: "braw", CWD: cwd}
			if err := printPath(&buf, output.New(false), session, "braw"); err != nil {
				t.Fatalf("printPath() error = %v", err)
			}

			if got := buf.String(); got != cwd {
				t.Errorf("output = %q, want %q", got, cwd)
			} else if strings.HasSuffix(got, "\n") {
				t.Error("plain output should not end with newline")
			}
		})
	}
}

func TestPrintPathJSONUsesCWD(t *testing.T) {
	cwd := t.TempDir()

	var buf bytes.Buffer

	session := &protocol.SessionInfo{ID: "abc123", Name: "braw", CWD: cwd, WorktreePath: "/source/croft"}

	if err := printPath(&bytes.Buffer{}, output.NewWithWriter(true, &buf), session, "braw"); err != nil {
		t.Fatalf("printPath() error = %v", err)
	}

	var result map[string]string
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	for _, key := range []string{"session_id", "name", "cwd"} {
		if _, ok := result[key]; !ok {
			t.Errorf("missing key %q in JSON output", key)
		}
	}

	if _, ok := result["worktree_path"]; ok {
		t.Error("JSON should expose cwd rather than the semantically different worktree_path")
	}

	if result["cwd"] != cwd {
		t.Errorf("cwd = %q, want %q", result["cwd"], cwd)
	}
}

func TestPrintPathRejectsInvalidCWD(t *testing.T) {
	file := filepath.Join(t.TempDir(), "thrawn")
	if err := os.WriteFile(file, []byte("nae directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		cwd  string
		want string
	}{
		{name: "unidentified", cwd: "", want: "no configured cwd"},
		{name: "relative", cwd: "bothy", want: "not absolute"},
		{name: "missing", cwd: filepath.Join(t.TempDir(), "missing"), want: "unavailable"},
		{name: "not directory", cwd: file, want: "not a directory"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer

			err := printPath(&buf, output.New(false), &protocol.SessionInfo{ID: "abc123", Name: "dreich", CWD: tt.cwd}, "dreich")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}

			if buf.Len() != 0 {
				t.Errorf("expected no output, got %q", buf.String())
			}
		})
	}
}

func TestResolveAndPrintPathNamedAndID(t *testing.T) {
	cwd := t.TempDir()
	sessions := protocol.SessionListMsg{Sessions: []protocol.SessionInfo{
		{ID: "orch-id", Name: "orchestrator", CWD: cwd, SystemKind: "orchestrator"},
	}}

	for _, ref := range []string{"orchestrator", "orch-id"} {
		t.Run(ref, func(t *testing.T) {
			conn := &scriptedConn{responses: []scriptedResp{okResp(payloadEnv("session_list", sessions))}}

			var buf bytes.Buffer
			if err := resolveAndPrintPath(conn, &buf, output.New(false), ref); err != nil {
				t.Fatalf("resolveAndPrintPath(%q) error = %v", ref, err)
			}

			if buf.String() != cwd {
				t.Errorf("output = %q, want orchestrator cwd %q", buf.String(), cwd)
			}
		})
	}
}

func TestPathSelfResolvesOrchestratorByIDThenName(t *testing.T) {
	cwd := t.TempDir()
	sessions := protocol.SessionListMsg{Sessions: []protocol.SessionInfo{
		{ID: "orch-id", Name: "orchestrator", CWD: cwd, SystemKind: "orchestrator"},
	}}

	tests := []struct {
		name     string
		id       string
		sessName string
		wantRef  string
	}{
		{name: "id preferred", id: "orch-id", sessName: "wrong-name", wantRef: "orch-id"},
		{name: "name fallback", sessName: "orchestrator", wantRef: "orchestrator"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GRAITH_SESSION_ID", tt.id)
			t.Setenv("GRAITH_SESSION_NAME", tt.sessName)

			args, err := selfArgs(true, nil)
			if err != nil {
				t.Fatal(err)
			}

			if len(args) != 1 || args[0] != tt.wantRef {
				t.Fatalf("self args = %v, want [%s]", args, tt.wantRef)
			}

			conn := &scriptedConn{responses: []scriptedResp{okResp(payloadEnv("session_list", sessions))}}

			var buf bytes.Buffer
			if err := resolveAndPrintPath(conn, &buf, output.New(false), args[0]); err != nil {
				t.Fatal(err)
			}

			if buf.String() != cwd {
				t.Errorf("output = %q, want %q", buf.String(), cwd)
			}
		})
	}
}

func TestPathSelfMissingEnvironment(t *testing.T) {
	t.Setenv("GRAITH_SESSION_ID", "")
	t.Setenv("GRAITH_SESSION_NAME", "")

	_, err := selfArgs(true, nil)
	if err == nil || !strings.Contains(err.Error(), "run it from inside a graith session") {
		t.Fatalf("error = %v, want clear outside-session error", err)
	}
}

func TestPathArgs(t *testing.T) {
	cmd := &cobra.Command{Use: "path"}

	pathSelf = true

	t.Cleanup(func() { pathSelf = false })

	if err := pathArgs(cmd, []string{"braw"}); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("--self positional conflict error = %v", err)
	}

	if err := pathArgs(cmd, nil); err != nil {
		t.Fatalf("--self with no args error = %v", err)
	}

	pathSelf = false

	if err := pathArgs(cmd, nil); err == nil {
		t.Fatal("path without --self should require a session argument")
	}
}

func TestPathUsesPersistedCWDFromNestedDirectory(t *testing.T) {
	cwd := t.TempDir()

	nested := filepath.Join(t.TempDir(), "glen", "bothy")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}

	t.Chdir(nested)

	var buf bytes.Buffer
	if err := printPath(&buf, output.New(false), &protocol.SessionInfo{ID: "canny", Name: "canny", CWD: cwd}, "canny"); err != nil {
		t.Fatal(err)
	}

	if buf.String() != cwd {
		t.Errorf("nested caller resolved %q, want persisted cwd %q", buf.String(), cwd)
	}
}
