package daemon

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/config"
)

func TestValidateSessionName(t *testing.T) {
	valid := []string{
		"my-session",
		"fix-bug-123",
		"feature_branch",
		"a",
		"A",
		"session.name",
		"my-session.v2",
		"123-numeric-start",
		"ALL-CAPS",
		"MixedCase",
		strings.Repeat("a", 128),
	}
	for _, name := range valid {
		if err := ValidateSessionName(name); err != nil {
			t.Errorf("ValidateSessionName(%q) = %v, want nil", name, err)
		}
	}

	invalid := []struct {
		name    string
		wantSub string
	}{
		{"", "must not be empty"},
		{"has space", "invalid"},
		{"has\nnewline", "invalid"},
		{"has\ttab", "invalid"},
		{"-leading-dash", "must start with an alphanumeric"},
		{"_leading-underscore", "must start with an alphanumeric"},
		{".leading-dot", "must start with an alphanumeric"},
		{"semi;colon", "invalid"},
		{"pipe|char", "invalid"},
		{"amp&ersand", "invalid"},
		{"dollar$sign", "invalid"},
		{"back`tick", "invalid"},
		{"single'quote", "invalid"},
		{"double\"quote", "invalid"},
		{"path/separator", "invalid"},
		{"back\\slash", "invalid"},
		{"paren(open", "invalid"},
		{"paren)close", "invalid"},
		{"curly{brace", "invalid"},
		{"curly}brace", "invalid"},
		{"angle<bracket", "invalid"},
		{"angle>bracket", "invalid"},
		{"star*glob", "invalid"},
		{"question?mark", "invalid"},
		{"hash#tag", "invalid"},
		{"exclam!", "invalid"},
		{"at@sign", "invalid"},
		{"percent%sign", "invalid"},
		{"caret^char", "invalid"},
		{"tilde~char", "invalid"},
		{"equal=sign", "invalid"},
		{"plus+sign", "invalid"},
		{"comma,sep", "invalid"},
		{"colon:sep", "invalid"},
		{"bracket[open", "invalid"},
		{"bracket]close", "invalid"},
		{"parent..traversal", "must not contain \"..\""},
		{"trailing-dot..", "must not contain \"..\""},
		{"..leading", "must not contain \"..\""},
		{"\x00null", "invalid"},
		{"\x1besc", "invalid"},
		{strings.Repeat("a", 129), "128 characters or fewer"},
	}
	for _, tc := range invalid {
		err := ValidateSessionName(tc.name)
		if err == nil {
			t.Errorf("ValidateSessionName(%q) = nil, want error containing %q", tc.name, tc.wantSub)
			continue
		}

		if !strings.Contains(err.Error(), tc.wantSub) {
			t.Errorf("ValidateSessionName(%q) = %q, want error containing %q", tc.name, err.Error(), tc.wantSub)
		}
	}
}

func TestCreateRejectsUnsafeName(t *testing.T) {
	sm := newTestSessionManager(t)

	_, err := sm.Create(CreateOpts{Name: "bad;name", AgentName: "claude", RepoPath: "/tmp", NoRepo: true, Rows: 24, Cols: 80})
	if err == nil {
		t.Fatal("Create with unsafe name should fail")
	}

	if !strings.Contains(err.Error(), "invalid") {
		t.Errorf("expected validation error, got: %v", err)
	}

	if len(sm.state.Sessions) != 0 {
		t.Error("no session should be created for an invalid name")
	}
}

func TestCreateSkipModelValidation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses shell scripts")
	}

	sm := newTestSessionManager(t)
	script := writeScript(t, "#!/bin/sh\necho 'model-a - Model A'\necho 'model-b - Model B'\n")
	sm.cfg.Agents["claude"] = config.Agent{
		Command:       "/nonexistent-graith-test-binary",
		ValidateModel: script,
	}

	t.Run("validation rejects unknown model by default", func(t *testing.T) {
		_, err := sm.Create(CreateOpts{Name: "haar-reject", AgentName: "claude", Model: "model-z", NoRepo: true, Rows: 24, Cols: 80})
		if err == nil {
			t.Fatal("expected validation error for unknown model")
		}

		if !strings.Contains(err.Error(), "invalid model") {
			t.Fatalf("expected 'invalid model' error, got: %v", err)
		}
	})

	t.Run("skip flag bypasses validation", func(t *testing.T) {
		_, err := sm.Create(CreateOpts{Name: "braw-skip", AgentName: "claude", Model: "model-z", NoRepo: true, SkipModelValidation: true, Rows: 24, Cols: 80})
		if err != nil && strings.Contains(err.Error(), "invalid model") {
			t.Fatalf("--skip-model-validation should bypass model check, got: %v", err)
		}
	})
}

func TestRenameRejectsUnsafeName(t *testing.T) {
	sm := newTestSessionManager(t)
	sm.state.Sessions["braw-id"] = &SessionState{
		ID:   "braw-id",
		Name: "bonnie-name",
	}

	err := sm.Rename("braw-id", "bad|name")
	if err == nil {
		t.Fatal("Rename with unsafe name should fail")
	}

	if sm.state.Sessions["braw-id"].Name != "bonnie-name" {
		t.Error("name should not have changed")
	}
}

func TestResumeRejectsUnsafePersistedName(t *testing.T) {
	tmpDir := t.TempDir()
	sm := newTestSessionManager(t)
	sm.paths.LogDir = tmpDir
	sm.state.Sessions["braw-id"] = &SessionState{
		ID:           "braw-id",
		Name:         "x; rm -rf /",
		Status:       StatusStopped,
		Agent:        "claude",
		WorktreePath: filepath.Join(tmpDir, "wt"),
	}

	_, err := sm.Resume("braw-id", 24, 80)
	if err == nil {
		t.Fatal("Resume with unsafe persisted name should fail")
	}

	if !strings.Contains(err.Error(), "unsafe name") {
		t.Errorf("expected unsafe name error, got: %v", err)
	}
}
