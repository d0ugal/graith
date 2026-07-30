package ciworkflow

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSecretScanTruffleHogCommandBuildsExpectedArgs(t *testing.T) {
	t.Parallel()

	script := secretScanTruffleHogScript(t)
	tests := map[string]struct {
		env          map[string]string
		wantArgs     []string
		forbidArgs   []string
		wantOutput   string
		wantErr      string
		wantExitCode int
	}{
		"push range": {
			env: map[string]string{
				"COMMIT_IDS":   `["canny","braw"]`,
				"EVENT_AFTER":  "braw-head",
				"EVENT_BEFORE": "canny-base",
				"EVENT_NAME":   "push",
			},
			wantArgs: []string{
				"run", "--rm", "-v",
				"ghcr.io/trufflesecurity/trufflehog@sha256:" + strings.Repeat("a", 64),
				"git", "file:///repo/", "--fail", "--no-update", "--github-actions", "--only-verified",
				"--since-commit", "canny-base", "--branch", "braw-head",
			},
		},
		"first push scans selected head history": {
			env: map[string]string{
				"COMMIT_IDS":   `["braw"]`,
				"EVENT_AFTER":  "braw-head",
				"EVENT_BEFORE": "0000000000000000000000000000000000000000",
				"EVENT_NAME":   "push",
			},
			wantArgs: []string{
				"git", "file:///repo/", "--fail", "--no-update", "--github-actions", "--only-verified",
				"--branch", "braw-head",
			},
			forbidArgs: []string{"--since-commit"},
		},
		"push with no commits exits cleanly before docker": {
			env: map[string]string{
				"COMMIT_IDS":   `[]`,
				"EVENT_AFTER":  "braw-head",
				"EVENT_BEFORE": "canny-base",
				"EVENT_NAME":   "push",
			},
			wantOutput: "No commits to scan",
		},
		"push missing head fails closed": {
			env: map[string]string{
				"COMMIT_IDS":   `["braw"]`,
				"EVENT_AFTER":  "",
				"EVENT_BEFORE": "canny-base",
				"EVENT_NAME":   "push",
			},
			wantErr:      "Missing TruffleHog range",
			wantExitCode: 1,
		},
		"pull request range": {
			env: map[string]string{
				"COMMIT_IDS":  `[]`,
				"EVENT_NAME":  "pull_request",
				"PR_BASE_SHA": "canny-base",
				"PR_HEAD_SHA": "braw-head",
			},
			wantArgs: []string{
				"git", "file:///repo/", "--fail", "--no-update", "--github-actions", "--only-verified",
				"--since-commit", "canny-base", "--branch", "braw-head",
			},
		},
		"pull request missing range fails closed": {
			env: map[string]string{
				"COMMIT_IDS":  `[]`,
				"EVENT_NAME":  "pull_request",
				"PR_BASE_SHA": "",
				"PR_HEAD_SHA": "braw-head",
			},
			wantErr:      "Missing TruffleHog range",
			wantExitCode: 1,
		},
		"schedule full history": {
			env: map[string]string{
				"COMMIT_IDS": `[]`,
				"EVENT_NAME": "schedule",
			},
			wantArgs: []string{
				"git", "file:///repo/", "--fail", "--no-update", "--github-actions", "--only-verified",
			},
			forbidArgs: []string{"--since-commit", "--branch"},
		},
		"workflow dispatch full history": {
			env: map[string]string{
				"COMMIT_IDS": `[]`,
				"EVENT_NAME": "workflow_dispatch",
			},
			wantArgs: []string{
				"git", "file:///repo/", "--fail", "--no-update", "--github-actions", "--only-verified",
			},
			forbidArgs: []string{"--since-commit", "--branch"},
		},
		"pull request empty range fails closed": {
			env: map[string]string{
				"COMMIT_IDS":  `[]`,
				"EVENT_NAME":  "pull_request",
				"PR_BASE_SHA": "thrawn",
				"PR_HEAD_SHA": "thrawn",
			},
			wantErr:      "Empty TruffleHog range",
			wantExitCode: 1,
		},
		"unsupported event fails closed": {
			env: map[string]string{
				"COMMIT_IDS": `[]`,
				"EVENT_NAME": "release",
			},
			wantErr:      "Unsupported TruffleHog event",
			wantExitCode: 1,
		},
		"malformed image pin fails closed": {
			env: map[string]string{
				"COMMIT_IDS":       `[]`,
				"EVENT_NAME":       "schedule",
				"TRUFFLEHOG_IMAGE": "ghcr.io/trufflesecurity/trufflehog:latest",
			},
			wantErr:      "Invalid TruffleHog pin",
			wantExitCode: 1,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := runSecretScanTruffleHogScript(t, script, test.env)

			if got.exitCode != test.wantExitCode {
				t.Fatalf("exit code = %d, want %d; output:\n%s", got.exitCode, test.wantExitCode, got.output)
			}

			if test.wantOutput != "" && !strings.Contains(got.output, test.wantOutput) {
				t.Fatalf("output does not contain %q:\n%s", test.wantOutput, got.output)
			}

			if test.wantErr != "" && !strings.Contains(got.output, test.wantErr) {
				t.Fatalf("output does not contain error %q:\n%s", test.wantErr, got.output)
			}

			if test.wantExitCode != 0 || test.wantOutput != "" {
				if len(got.dockerArgs) != 0 {
					t.Fatalf("docker was unexpectedly called with %#v", got.dockerArgs)
				}

				return
			}

			assertArgsContainSubsequence(t, got.dockerArgs, test.wantArgs)
			assertArgsDoNotContain(t, got.dockerArgs, test.forbidArgs)
		})
	}
}

type secretScanRunResult struct {
	exitCode   int
	output     string
	dockerArgs []string
}

func secretScanTruffleHogScript(t *testing.T) string {
	t.Helper()

	workflow, err := ReadP11WorkflowSummary(filepath.Join(p11RepoRoot(), ".github/workflows/secret-scan.yml"))
	if err != nil {
		t.Fatal(err)
	}

	step := p11WorkflowStep(t, p11WorkflowJob(t, workflow, "trufflehog"), "TruffleHog (verified secrets only)")
	if step.Run == "" {
		t.Fatal("TruffleHog step has no run script")
	}

	return step.Run
}

func runSecretScanTruffleHogScript(t *testing.T, script string, env map[string]string) secretScanRunResult {
	t.Helper()

	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "trufflehog.sh")
	dockerPath := filepath.Join(dir, "docker")
	argsPath := filepath.Join(dir, "docker-args")

	if err := os.WriteFile(scriptPath, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}

	dockerScript := `#!/bin/sh
printf '%s\n' "$@" >"$GRAITH_FAKE_DOCKER_ARGS"
`
	if err := os.WriteFile(dockerPath, []byte(dockerScript), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(dockerPath, 0o700); err != nil { //nolint:gosec // Fake docker command must be executable for workflow policy coverage.
		t.Fatal(err)
	}

	cmd := exec.Command("bash", scriptPath)
	cmd.Dir = p11RepoRoot()

	cmd.Env = append(os.Environ(),
		"PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GRAITH_FAKE_DOCKER_ARGS="+argsPath,
		"TRUFFLEHOG_IMAGE=ghcr.io/trufflesecurity/trufflehog:3.96.0@sha256:"+strings.Repeat("a", 64),
		"COMMIT_IDS=[]",
		"EVENT_AFTER=",
		"EVENT_BEFORE=",
		"EVENT_NAME=schedule",
		"PR_BASE_SHA=",
		"PR_HEAD_SHA=",
	)
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}

	output, err := cmd.CombinedOutput()
	result := secretScanRunResult{output: string(output)}

	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("run TruffleHog script: %v\n%s", err, output)
		}

		result.exitCode = exitErr.ExitCode()
	}

	if data, err := os.ReadFile(argsPath); err == nil {
		result.dockerArgs = strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}

	return result
}

func assertArgsContainSubsequence(t *testing.T, got, want []string) {
	t.Helper()

	next := 0
	for _, arg := range got {
		if next < len(want) && arg == want[next] {
			next++
		}
	}

	if next != len(want) {
		t.Fatalf("args %#v do not contain subsequence %#v", got, want)
	}
}

func assertArgsDoNotContain(t *testing.T, got, forbidden []string) {
	t.Helper()

	for _, needle := range forbidden {
		for _, arg := range got {
			if arg == needle {
				t.Fatalf("args unexpectedly contain %q: %#v", needle, got)
			}
		}
	}
}
