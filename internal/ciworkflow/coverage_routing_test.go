package ciworkflow

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCoverageGUIDetectorFailsSafe(t *testing.T) {
	t.Parallel()

	script := coverageGUIDetectorScript(t)
	tests := map[string]struct {
		files  string
		ghFail bool
		goFail bool
		want   string
		output string
	}{
		"detector success with gui change": {
			files:  "gui/shared/Sources/Braw.swift\ninternal/client/passthrough.go",
			want:   "true",
			output: "gui=true",
		},
		"detector success with no gui change": {
			files:  "internal/client/passthrough.go\nwebsite/content/docs/canny.md",
			want:   "false",
			output: "gui=false",
		},
		"file list failure runs swift coverage": {
			ghFail: true,
			want:   "true",
			output: "gui=true",
		},
		"classifier failure runs swift coverage": {
			files:  "internal/client/passthrough.go",
			goFail: true,
			want:   "true",
			output: "gui=true",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, combined := runCoverageGUIDetector(t, script, test.files, test.ghFail, test.goFail)

			if got != test.want {
				t.Fatalf("gui output = %q, want %q; command output:\n%s", got, test.want, combined)
			}

			if !strings.Contains(combined, test.output) {
				t.Fatalf("command output does not contain %q:\n%s", test.output, combined)
			}
		})
	}
}

func TestCoverageSwiftRoutingFailsSafe(t *testing.T) {
	t.Parallel()

	workflow := coverageWorkflow(t)
	swiftCoverage := p11WorkflowJob(t, workflow, "swift-coverage")

	const wantCondition = "!cancelled() && (needs.changes.result != 'success' || needs.changes.outputs.gui != 'false')"

	if swiftCoverage.If != wantCondition {
		t.Fatalf("swift-coverage condition = %q, want %q", swiftCoverage.If, wantCondition)
	}

	commentScript := p11WorkflowStep(t, p11WorkflowJob(t, workflow, "comment"), "Build and post sticky comment").Run
	assertContains(t, commentScript, `if [ "$GUI_CHANGED" != "false" ]; then`)

	tests := map[string]struct {
		changesResult string
		guiOutput     string
		cancelled     bool
		want          bool
	}{
		"detector success with gui change":    {changesResult: "success", guiOutput: "true", want: true},
		"detector success with no gui change": {changesResult: "success", guiOutput: "false", want: false},
		"malformed detector output":           {changesResult: "success", guiOutput: "dreich", want: true},
		"missing detector output":             {changesResult: "success", guiOutput: "", want: true},
		"detector failure with false output":  {changesResult: "failure", guiOutput: "false", want: true},
		"cancelled workflow":                  {changesResult: "success", guiOutput: "true", cancelled: true, want: false},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got := coverageSwiftCondition(test.changesResult, test.guiOutput, test.cancelled); got != test.want {
				t.Fatalf("coverageSwiftCondition(result=%q, gui=%q, cancelled=%t) = %t, want %t",
					test.changesResult, test.guiOutput, test.cancelled, got, test.want)
			}
		})
	}
}

func coverageWorkflow(t *testing.T) P11WorkflowSummary {
	t.Helper()

	workflow, err := ReadP11WorkflowSummary(filepath.Join(p11RepoRoot(), ".github/workflows/coverage.yml"))
	if err != nil {
		t.Fatal(err)
	}

	return workflow
}

func coverageGUIDetectorScript(t *testing.T) string {
	t.Helper()

	changes := p11WorkflowJob(t, coverageWorkflow(t), "changes")

	var script string

	for _, step := range changes.Steps {
		if strings.Contains(step.Run, "go run ./cmd/ciclassify -mode coverage") {
			script = step.Run
			break
		}
	}

	if script == "" {
		t.Fatalf("coverage changes job does not call ciclassify: %#v", changes.Steps)
	}

	assertContains(t, script, `echo "gui=true" >> "$GITHUB_OUTPUT"`)
	assertContains(t, script, "Could not list PR files; running Swift coverage to be safe.")
	assertContains(t, script, "Shared classifier failed; running Swift coverage to be safe.")

	return script
}

func runCoverageGUIDetector(t *testing.T, script, files string, ghFail, goFail bool) (string, string) {
	t.Helper()

	dir := t.TempDir()
	outputPath := filepath.Join(dir, "github-output")
	scriptPath := filepath.Join(dir, "detector.sh")
	ghPath := filepath.Join(dir, "gh")
	goPath := filepath.Join(dir, "go")

	if err := os.WriteFile(scriptPath, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}

	ghScript := `#!/bin/sh
if [ "${GRAITH_FAKE_GH_FAIL:-}" = "1" ]; then
  exit 23
fi
printf '%s\n' "$GRAITH_FAKE_GH_FILES"
`
	if err := os.WriteFile(ghPath, []byte(ghScript), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(ghPath, 0o700); err != nil { //nolint:gosec // Fake gh command must be executable for workflow policy coverage.
		t.Fatal(err)
	}

	goScript := `#!/bin/sh
if [ "${GRAITH_FAKE_GO_FAIL:-}" = "1" ]; then
  exit 24
fi
files="$(cat)"
case "$files" in
  *gui/*) echo "gui=true" ;;
  *) echo "gui=false" ;;
esac
`
	if err := os.WriteFile(goPath, []byte(goScript), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(goPath, 0o700); err != nil { //nolint:gosec // Fake go command must be executable for workflow policy coverage.
		t.Fatal(err)
	}

	cmd := exec.Command("bash", scriptPath)

	cmd.Env = append(os.Environ(),
		"GITHUB_OUTPUT="+outputPath,
		"GH_TOKEN=braw-token",
		"REPO=d0ugal/graith",
		"PR=1760",
		"GRAITH_FAKE_GH_FILES="+files,
		"PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	if ghFail {
		cmd.Env = append(cmd.Env, "GRAITH_FAKE_GH_FAIL=1")
	}

	if goFail {
		cmd.Env = append(cmd.Env, "GRAITH_FAKE_GO_FAIL=1")
	}

	combinedBytes, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("detector command failed: %v\n%s", err, combinedBytes)
	}

	outputBytes, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read GITHUB_OUTPUT: %v\ncommand output:\n%s", err, combinedBytes)
	}

	return coverageGUIOutput(t, string(outputBytes)), string(combinedBytes) + string(outputBytes)
}

func coverageGUIOutput(t *testing.T, output string) string {
	t.Helper()

	var got []string

	for _, line := range strings.Split(output, "\n") {
		value, ok := strings.CutPrefix(line, "gui=")
		if ok {
			got = append(got, value)
		}
	}

	if len(got) != 1 {
		t.Fatalf("gui output count = %d, want 1 in:\n%s", len(got), output)
	}

	return got[0]
}

func coverageSwiftCondition(changesResult, guiOutput string, cancelled bool) bool {
	return !cancelled && (changesResult != "success" || guiOutput != "false")
}
