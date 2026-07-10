// Package testutil provides shared helpers for graith's Go tests.
package testutil

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func hermeticGitEnv() []string {
	return []string{
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=commit.gpgsign",
		"GIT_CONFIG_VALUE_0=false",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=canny",
		"GIT_AUTHOR_EMAIL=canny@example.com",
		"GIT_COMMITTER_NAME=canny",
		"GIT_COMMITTER_EMAIL=canny@example.com",
	}
}

// GitEnv returns a deterministic environment for Git fixture commands. Every
// inherited GIT_* variable is stripped so host settings cannot redirect the
// repository, worktree, index, object database, config, SSH command, or hooks.
// SSH_AUTH_SOCK is also omitted: local-only fixtures never need credentials and
// must not contact a developer's agent. Optional KEY=value entries override the
// defaults, which is useful for tests that intentionally exercise --global
// config with a throwaway HOME.
func GitEnv(overrides ...string) []string {
	hermetic := hermeticGitEnv()

	env := make([]string, 0, len(os.Environ())+len(hermetic)+len(overrides))
	for _, entry := range os.Environ() {
		key := envKey(entry)
		if strings.HasPrefix(key, "GIT_") || key == "SSH_AUTH_SOCK" {
			continue
		}

		env = append(env, entry)
	}

	env = append(env, hermetic...)
	for _, override := range overrides {
		env = replaceEnv(env, override)
	}

	return env
}

// GitCommand constructs a Git command with GitEnv applied.
func GitCommand(args ...string) *exec.Cmd {
	cmd := exec.Command("git", args...)
	cmd.Env = GitEnv()

	return cmd
}

// IsolateGit applies GitEnv to the current test process so production helpers
// invoked in-process (rather than through GitCommand) are hermetic too. Tests
// using this helper must not run in parallel because environment variables are
// process-global.
func IsolateGit(t testing.TB) {
	t.Helper()
	t.Cleanup(isolateGit())
}

// RunWithIsolatedGit runs a package's tests with the same hermetic Git
// environment used by GitCommand. Call it from TestMain so production Git
// helpers invoked anywhere in the package cannot observe host settings.
func RunWithIsolatedGit(m *testing.M) int {
	cleanup := isolateGit()
	defer cleanup()

	return m.Run()
}

func isolateGit() func() {
	original := make(map[string]string)

	for _, entry := range os.Environ() {
		key := envKey(entry)
		if !strings.HasPrefix(key, "GIT_") && key != "SSH_AUTH_SOCK" {
			continue
		}

		value := strings.TrimPrefix(entry, key+"=")
		original[key] = value
		_ = os.Unsetenv(key)
	}

	hermetic := hermeticGitEnv()
	for _, entry := range hermetic {
		key := envKey(entry)
		value := strings.TrimPrefix(entry, key+"=")
		_ = os.Setenv(key, value)
	}

	return func() {
		for _, entry := range hermetic {
			_ = os.Unsetenv(envKey(entry))
		}

		for key, value := range original {
			_ = os.Setenv(key, value)
		}
	}
}

func replaceEnv(env []string, replacement string) []string {
	key := envKey(replacement)

	filtered := env[:0]
	for _, entry := range env {
		if envKey(entry) != key {
			filtered = append(filtered, entry)
		}
	}

	return append(filtered, replacement)
}

func envKey(entry string) string {
	key, _, _ := strings.Cut(entry, "=")
	return key
}
