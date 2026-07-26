package cipolicy

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestWorkflowLintShellcheckPolicy(t *testing.T) {
	repoRoot := p11RepoRoot()
	makefile := readPolicyFile(t, filepath.Join(repoRoot, "Makefile"))
	workflowText := readPolicyFile(t, filepath.Join(repoRoot, ".github/workflows/workflow-lint.yml"))

	if !strings.Contains(makefile, "git ls-files -z -- '*.sh' | xargs -0 shellcheck --enable=all --severity=warning") {
		t.Fatal("Makefile shellcheck target must lint every tracked shell script with strict warnings enabled")
	}

	workflow, err := ReadP11WorkflowSummary(filepath.Join(repoRoot, ".github/workflows/workflow-lint.yml"))
	if err != nil {
		t.Fatal(err)
	}

	shellcheck := p11WorkflowJob(t, workflow, "shellcheck")
	step := p11WorkflowStep(t, shellcheck, "Lint tracked shell scripts")

	if !strings.Contains(step.Run, "shellcheck --version") || !strings.Contains(step.Run, "make shellcheck") {
		t.Fatalf("shellcheck run block = %q, want version print and make shellcheck", step.Run)
	}

	if got := strings.Count(workflowText, "- '**/*.sh'"); got != 2 {
		t.Fatalf("nested shell path-filter count = %d, want 2", got)
	}

	if got := strings.Count(workflowText, "- '*.sh'"); got != 2 {
		t.Fatalf("root shell path-filter count = %d, want 2", got)
	}
}

func TestWorkflowLintSupplyChainPolicy(t *testing.T) {
	repoRoot := p11RepoRoot()

	workflow, err := ReadP11WorkflowSummary(filepath.Join(repoRoot, ".github/workflows/workflow-lint.yml"))
	if err != nil {
		t.Fatal(err)
	}

	tests := map[string]struct {
		jobID       string
		stepName    string
		repository  string
		installExpr *regexp.Regexp
	}{
		"actionlint": {
			jobID:       "actionlint",
			stepName:    "Install actionlint",
			repository:  "rhysd/actionlint",
			installExpr: regexp.MustCompile(`tar -xzf|sudo install`),
		},
		"zizmor": {
			jobID:       "zizmor",
			stepName:    "Install zizmor",
			repository:  "zizmorcore/zizmor",
			installExpr: regexp.MustCompile(`tar -xzf`),
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			job := p11WorkflowJob(t, workflow, test.jobID)

			wantPermissions := map[string]string{"contents": "read", "attestations": "read"}
			if !reflect.DeepEqual(job.Permissions, wantPermissions) {
				t.Fatalf("%s permissions = %#v, want %#v", test.jobID, job.Permissions, wantPermissions)
			}

			step := p11WorkflowStep(t, job, test.stepName)
			if step.Env["GH_TOKEN"] != "${{ github.token }}" {
				t.Fatalf("%s GH_TOKEN env = %q, want github.token", test.stepName, step.Env["GH_TOKEN"])
			}

			code := workflowExecutableLines(step.Run)
			assertContains(t, code, "set -euo pipefail")
			assertContains(t, code, "curl -fsSL --proto '=https' --tlsv1.2")

			if !regexp.MustCompile(`gh attestation verify[^\n]*--repo ` + regexp.QuoteMeta(test.repository)).MatchString(code) {
				t.Fatalf("%s must verify provenance against %s on the attestation command line:\n%s", test.stepName, test.repository, code)
			}

			verifyAt := strings.Index(code, "gh attestation verify ")
			installAt := test.installExpr.FindStringIndex(code)

			if verifyAt == -1 || installAt == nil || verifyAt > installAt[0] {
				t.Fatalf("%s must verify provenance before extract/install:\n%s", test.stepName, code)
			}

			if regexp.MustCompile(`gh attestation verify[^\n]*\|\|`).MatchString(code) {
				t.Fatalf("%s verification must not be guarded with ||", test.stepName)
			}

			if strings.Contains(code, "set +e") {
				t.Fatalf("%s must not disable errexit", test.stepName)
			}
		})
	}
}

func TestWorkflowLintDoesNotUseUnpinnedZizmorInstallPath(t *testing.T) {
	repoRoot := p11RepoRoot()

	workflow, err := ReadP11WorkflowSummary(filepath.Join(repoRoot, ".github/workflows/workflow-lint.yml"))
	if err != nil {
		t.Fatal(err)
	}

	for jobID, job := range workflow.Jobs {
		for _, step := range job.Steps {
			code := workflowExecutableLines(step.Run)
			if regexp.MustCompile(`\buvx\b`).MatchString(code) || strings.Contains(code, "setup-uv") || strings.Contains(step.Uses, "setup-uv") {
				t.Fatalf("workflow-lint job %s step %q uses the retired uv/setup-uv zizmor install path", jobID, step.Name)
			}
		}
	}
}

func workflowExecutableLines(run string) string {
	lines := strings.Split(run, "\n")

	filtered := lines[:0]
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}

		filtered = append(filtered, line)
	}

	return strings.Join(filtered, "\n")
}

func readPolicyFile(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	return string(data)
}

func assertContains(t *testing.T, value, want string) {
	t.Helper()

	if !strings.Contains(value, want) {
		t.Fatalf("value does not contain %q:\n%s", want, value)
	}
}
