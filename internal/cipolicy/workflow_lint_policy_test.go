package cipolicy

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	yaml "go.yaml.in/yaml/v3"
)

type workflowLintEventFilter struct {
	Keys     []string
	Branches []string
	Paths    []string
}

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

func TestWorkflowLintTriggerPathsIncludeLintConfig(t *testing.T) {
	repoRoot := p11RepoRoot()
	filters := readWorkflowLintEventFilters(t, filepath.Join(repoRoot, ".github/workflows/workflow-lint.yml"))

	wantPaths := []string{
		".github/workflows/**",
		".github/actionlint.yaml",
		".github/zizmor.yml",
		"internal/libghosttydeps/testdata/renovate/**",
		"libghostty-native.lock.json",
		"renovate.json5",
		"**/*.sh",
		"*.sh",
		"Makefile",
		"scripts/verify-renovate-libghostty.sh",
	}

	tests := map[string]struct {
		wantKeys     []string
		wantBranches []string
	}{
		"pull_request": {wantKeys: []string{"paths"}},
		"push":         {wantKeys: []string{"branches", "paths"}, wantBranches: []string{"main"}},
	}

	if len(filters) != len(tests) {
		t.Fatalf("workflow-lint events = %v, want only push and pull_request", sortedWorkflowEventNames(filters))
	}

	for eventName, test := range tests {
		t.Run(eventName, func(t *testing.T) {
			filter, ok := filters[eventName]
			if !ok {
				t.Fatalf("workflow-lint is missing %s trigger", eventName)
			}

			assertStringsEqual(t, eventName+" filter keys", filter.Keys, test.wantKeys)
			assertStringsEqual(t, eventName+" path filters", filter.Paths, wantPaths)
			assertStringsEqual(t, eventName+" branch filters", filter.Branches, test.wantBranches)
		})
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

func TestGolangciLintDockerImageIsDigestPinned(t *testing.T) {
	repoRoot := p11RepoRoot()
	makefile := readPolicyFile(t, filepath.Join(repoRoot, "Makefile"))
	renovate := readPolicyFile(t, filepath.Join(repoRoot, "renovate.json5"))

	assertRegexp(t, makefile, `(?m)^GOLANGCI_LINT_VERSION := v\d+\.\d+\.\d+$`)
	assertRegexp(t, makefile, `(?m)^GOLANGCI_LINT_DIGEST := sha256:[0-9a-f]{64}$`)
	assertContains(t, makefile, "GOLANGCI_LINT_IMAGE := golangci/golangci-lint:$(GOLANGCI_LINT_VERSION)@$(GOLANGCI_LINT_DIGEST)")

	if got := strings.Count(makefile, "golangci/golangci-lint:"); got != 1 {
		t.Fatalf("Makefile golangci-lint image coordinate count = %d, want 1", got)
	}

	assertContains(t, renovate, "GOLANGCI_LINT_VERSION := (?<currentValue>v[\\\\d\\\\.]+)\\\\s+GOLANGCI_LINT_DIGEST := (?<currentDigest>sha256:[a-f0-9]{64})")
	assertContains(t, renovate, "autoReplaceStringTemplate: 'GOLANGCI_LINT_VERSION := {{{newValue}}}\\nGOLANGCI_LINT_DIGEST := {{{newDigest}}}',")
	assertNotContains(t, renovate, "pinDigests: false")
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

func readWorkflowLintEventFilters(t *testing.T, path string) map[string]workflowLintEventFilter {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var raw struct {
		On map[string]yaml.Node `yaml:"on"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}

	if len(raw.On) == 0 {
		t.Fatalf("workflow %s has no event filters", path)
	}

	filters := make(map[string]workflowLintEventFilter, len(raw.On))
	for eventName, eventNode := range raw.On {
		if eventNode.Kind != yaml.MappingNode {
			t.Fatalf("workflow %s event %s filter node kind = %v, want mapping", path, eventName, eventNode.Kind)
		}

		var filter workflowLintEventFilter

		for index := 0; index < len(eventNode.Content); index += 2 {
			key := eventNode.Content[index].Value
			value := eventNode.Content[index+1]

			filter.Keys = append(filter.Keys, key)
			switch key {
			case "branches":
				filter.Branches = p11StringList(value)
			case "paths":
				filter.Paths = p11StringList(value)
			}
		}

		filters[eventName] = filter
	}

	return filters
}

func sortedWorkflowEventNames(filters map[string]workflowLintEventFilter) []string {
	names := make([]string, 0, len(filters))
	for name := range filters {
		names = append(names, name)
	}

	return sortedStrings(names)
}

func assertContains(t *testing.T, value, want string) {
	t.Helper()

	if !strings.Contains(value, want) {
		t.Fatalf("value does not contain %q:\n%s", want, value)
	}
}
