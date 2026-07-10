package testutil

import (
	"os"
	"strings"
	"testing"
)

func TestGitEnvIgnoresHostGitAndSSHVariables(t *testing.T) {
	t.Setenv("GIT_DIR", "/nonexistent/dreich.git")
	t.Setenv("GIT_CONFIG_GLOBAL", "/nonexistent/thrawn.gitconfig")
	t.Setenv("SSH_AUTH_SOCK", "/nonexistent/fash.sock")

	env := GitEnv()

	values := make(map[string]string, len(env))
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		values[key] = value
	}

	if _, ok := values["GIT_DIR"]; ok {
		t.Error("GIT_DIR leaked into hermetic Git environment")
	}

	if _, ok := values["SSH_AUTH_SOCK"]; ok {
		t.Error("SSH_AUTH_SOCK leaked into hermetic Git environment")
	}

	if got := values["GIT_CONFIG_GLOBAL"]; got != os.DevNull {
		t.Errorf("GIT_CONFIG_GLOBAL = %q, want %q", got, os.DevNull)
	}

	if got := values["GIT_CONFIG_NOSYSTEM"]; got != "1" {
		t.Errorf("GIT_CONFIG_NOSYSTEM = %q, want 1", got)
	}
}

func TestGitEnvOverridesDefaults(t *testing.T) {
	env := GitEnv("GIT_CONFIG_GLOBAL=/tmp/canny.gitconfig", "HOME=/tmp/braw")
	joined := "\n" + strings.Join(env, "\n") + "\n"

	if !strings.Contains(joined, "\nGIT_CONFIG_GLOBAL=/tmp/canny.gitconfig\n") {
		t.Errorf("override missing from environment: %s", joined)
	}

	if strings.Contains(joined, "\nGIT_CONFIG_GLOBAL="+os.DevNull+"\n") {
		t.Errorf("default global config survived override: %s", joined)
	}

	if !strings.Contains(joined, "\nHOME=/tmp/braw\n") {
		t.Errorf("HOME override missing from environment: %s", joined)
	}
}

func TestGitCommandUsesHermeticEnvironment(t *testing.T) {
	t.Setenv("GIT_DIR", "/nonexistent/dreich.git")

	cmd := GitCommand("--version")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("GitCommand failed: %v\n%s", err, out)
	}

	joined := "\n" + strings.Join(cmd.Env, "\n") + "\n"
	if strings.Contains(joined, "\nGIT_DIR=") {
		t.Errorf("GIT_DIR leaked into GitCommand: %s", joined)
	}
}

func TestIsolateGitRestoresEnvironment(t *testing.T) {
	t.Setenv("GIT_DIR", "/nonexistent/dreich.git")
	t.Setenv("SSH_AUTH_SOCK", "/nonexistent/fash.sock")

	t.Run("isolated", func(t *testing.T) {
		IsolateGit(t)

		if value := os.Getenv("GIT_DIR"); value != "" {
			t.Errorf("GIT_DIR = %q, want unset", value)
		}

		if value := os.Getenv("SSH_AUTH_SOCK"); value != "" {
			t.Errorf("SSH_AUTH_SOCK = %q, want unset", value)
		}

		if value := os.Getenv("GIT_CONFIG_GLOBAL"); value != os.DevNull {
			t.Errorf("GIT_CONFIG_GLOBAL = %q, want %q", value, os.DevNull)
		}
	})

	if value := os.Getenv("GIT_DIR"); value != "/nonexistent/dreich.git" {
		t.Errorf("GIT_DIR after cleanup = %q, want original value", value)
	}

	if value := os.Getenv("SSH_AUTH_SOCK"); value != "/nonexistent/fash.sock" {
		t.Errorf("SSH_AUTH_SOCK after cleanup = %q, want original value", value)
	}
}
