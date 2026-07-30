package ciworkflow

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
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
		".github/ci-tool-versions.env",
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

func TestWorkflowToolInstallSupplyChainPolicy(t *testing.T) {
	repoRoot := p11RepoRoot()

	tests := map[string]struct {
		workflowPath   string
		jobID          string
		stepName       string
		repository     string
		signerWorkflow string
		installExpr    *regexp.Regexp
	}{
		"actionlint": {
			workflowPath:   ".github/workflows/workflow-lint.yml",
			jobID:          "actionlint",
			stepName:       "Install actionlint",
			repository:     "rhysd/actionlint",
			signerWorkflow: "rhysd/actionlint/.github/workflows/release.yaml",
			installExpr:    regexp.MustCompile(`tar -xzf|sudo install`),
		},
		"nono": {
			workflowPath:   ".github/workflows/sandbox.yml",
			jobID:          "linux-nono",
			stepName:       "Install nono",
			repository:     "nolabs-ai/nono",
			signerWorkflow: "nolabs-ai/nono/.github/workflows/release.yml",
			installExpr:    regexp.MustCompile(`tar -xzf`),
		},
		"zizmor": {
			workflowPath:   ".github/workflows/workflow-lint.yml",
			jobID:          "zizmor",
			stepName:       "Install zizmor",
			repository:     "zizmorcore/zizmor",
			signerWorkflow: "zizmorcore/zizmor/.github/workflows/release-binaries.yml",
			installExpr:    regexp.MustCompile(`tar -xzf`),
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			workflow, err := ReadP11WorkflowSummary(filepath.Join(repoRoot, test.workflowPath))
			if err != nil {
				t.Fatal(err)
			}

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

			if err := validateAttestationVerifyCommand(code, test.stepName, test.repository, test.signerWorkflow); err != nil {
				t.Fatal(err)
			}

			verifyAt := strings.Index(code, "gh attestation verify ")
			installAt := test.installExpr.FindStringIndex(code)

			if verifyAt == -1 || installAt == nil || verifyAt > installAt[0] {
				t.Fatalf("%s must verify provenance before extract/install:\n%s", test.stepName, code)
			}
		})
	}
}

func TestSandboxSafehouseInstallPinsReviewedBytes(t *testing.T) {
	repoRoot := p11RepoRoot()
	workflowText := readPolicyFile(t, filepath.Join(repoRoot, ".github/workflows/sandbox.yml"))
	renovate := readPolicyFile(t, filepath.Join(repoRoot, "renovate.json5"))

	workflow, err := ReadP11WorkflowSummary(filepath.Join(repoRoot, ".github/workflows/sandbox.yml"))
	if err != nil {
		t.Fatal(err)
	}

	job := p11WorkflowJob(t, workflow, "macos-safehouse")
	step := p11WorkflowStep(t, job, "Install checksum-verified safehouse")

	assertRegexp(t, step.Env["SAFEHOUSE_VERSION"], `^\d+\.\d+\.\d+$`)
	assertRegexp(t, step.Env["SAFEHOUSE_SHA256"], `^[a-f0-9]{64}$`)

	code := workflowExecutableLines(step.Run)
	assertContains(t, code, "set -euo pipefail")
	assertContains(t, code, "https://github.com/eugene1g/agent-safehouse/releases/download/v${SAFEHOUSE_VERSION}/${asset}")
	assertContains(t, code, "curl -fsSL --proto '=https' --tlsv1.2")
	assertContains(t, code, `actual_sha="$(shasum -a 256 "$tmp/$asset" | awk '{print $1}')"`)
	assertContains(t, code, `if [ "$actual_sha" != "$SAFEHOUSE_SHA256" ]; then`)
	assertContains(t, code, `install -m 0755 "$tmp/$asset" "$dest/safehouse"`)
	assertContains(t, code, `installed_sha="$(shasum -a 256 "$(command -v safehouse)" | awk '{print $1}')"`)
	assertContains(t, code, `if [ "$installed_sha" != "$SAFEHOUSE_SHA256" ]; then`)
	assertContains(t, code, `version_output="$(safehouse --version)"`)
	assertContains(t, code, `"$version_output" != "Agent Safehouse ${SAFEHOUSE_VERSION}"`)

	checksumAt := strings.Index(code, `actual_sha="$(shasum -a 256 "$tmp/$asset"`)
	checksumGateAt := strings.Index(code, `if [ "$actual_sha" != "$SAFEHOUSE_SHA256" ]; then`)

	installAt := strings.Index(code, `install -m 0755 "$tmp/$asset" "$dest/safehouse"`)
	if checksumAt == -1 || checksumGateAt == -1 || installAt == -1 ||
		checksumAt > checksumGateAt || checksumGateAt > installAt {
		t.Fatalf("safehouse install must verify the downloaded SHA-256 before installing:\n%s", code)
	}

	installedChecksumAt := strings.Index(code, `installed_sha="$(shasum -a 256 "$(command -v safehouse)"`)
	installedChecksumGateAt := strings.Index(code, `if [ "$installed_sha" != "$SAFEHOUSE_SHA256" ]; then`)

	versionAt := strings.Index(code, `version_output="$(safehouse --version)"`)
	if installedChecksumAt == -1 || installedChecksumGateAt == -1 || versionAt == -1 ||
		installedChecksumAt > installedChecksumGateAt || installedChecksumGateAt > versionAt {
		t.Fatalf("safehouse install must verify the installed SHA-256 before executing safehouse:\n%s", code)
	}

	assertNotContains(t, workflowText, "brew install eugene1g/safehouse/agent-safehouse")
	assertNotContains(t, workflowText, "brew tap eugene1g/safehouse")

	assertContains(t, renovate, `SAFEHOUSE_VERSION:\\s*"(?<currentValue>[\\d.]+)"`)
	assertContains(t, renovate, "depNameTemplate: 'eugene1g/agent-safehouse'")
	assertRegexp(t, renovate, `(?s)SAFEHOUSE_VERSION.*depNameTemplate: 'eugene1g/agent-safehouse'.*depTypeTemplate: 'ci-safehouse'.*datasourceTemplate: 'github-releases'`)
	assertRegexp(t, renovate, `(?s)matchDepTypes: \['ci-safehouse'\].*automerge: false.*dependencyDashboardApproval: true`)
}

func TestWorkflowAttestationVerifyCommandsBindSignerWorkflow(t *testing.T) {
	repoRoot := p11RepoRoot()

	var missing []string

	for _, path := range workflowPolicyFiles(t, repoRoot) {
		workflow, err := ReadP11WorkflowSummary(path)
		if err != nil {
			t.Fatal(err)
		}

		for jobID, job := range workflow.Jobs {
			for _, step := range job.Steps {
				code := workflowExecutableLines(step.Run)

				commands := workflowAttestationVerifyCommands(code)
				if len(commands) == 0 {
					continue
				}

				if err := validateAttestationVerifySafety(code, fmt.Sprintf("%s/%s/%s", filepath.ToSlash(path), jobID, step.Name)); err != nil {
					missing = append(missing, err.Error())
				}

				for _, command := range commands {
					if !attestationVerifyHasSignerWorkflow(command) {
						missing = append(missing, fmt.Sprintf("%s/%s/%s: %s", filepath.ToSlash(path), jobID, step.Name, command))
					}
				}
			}
		}
	}

	if len(missing) != 0 {
		sort.Strings(missing)
		t.Fatalf("gh attestation verify policy violations:\n%s", strings.Join(missing, "\n"))
	}
}

func TestAttestationVerifyCommandPolicyRejectsDetachedSignerAndFailOpen(t *testing.T) {
	tests := map[string]struct {
		run  string
		want string
	}{
		"braw fail open on continuation": {
			run: `gh attestation verify "$tmp/$tarball" --repo nolabs-ai/nono \
  --signer-workflow nolabs-ai/nono/.github/workflows/release.yml || true`,
			want: "must not be guarded with ||",
		},
		"canny signer only echoed later": {
			run: `set -euo pipefail
gh attestation verify "$tmp/$tarball" --repo nolabs-ai/nono
echo 'used --signer-workflow nolabs-ai/nono/.github/workflows/release.yml'`,
			want: "must bind provenance",
		},
		"couthy signer only echoed after semicolon": {
			run:  `set -euo pipefail; gh attestation verify "$tmp/$tarball" --repo nolabs-ai/nono; echo 'used --signer-workflow nolabs-ai/nono/.github/workflows/release.yml'`,
			want: "must bind provenance",
		},
		"dreich signer only in inline comment": {
			run:  `set -euo pipefail; gh attestation verify "$tmp/$tarball" --repo nolabs-ai/nono # --signer-workflow nolabs-ai/nono/.github/workflows/release.yml`,
			want: "must bind provenance",
		},
		"bairn verify in if condition": {
			run: `set -euo pipefail
if gh attestation verify "$tmp/$tarball" --repo nolabs-ai/nono \
  --signer-workflow nolabs-ai/nono/.github/workflows/release.yml; then
  echo verified
fi`,
			want: "must not be used as a shell condition",
		},
		"thrawn negated verify in if condition": {
			run: `set -euo pipefail
if ! gh attestation verify "$tmp/$tarball" --repo nolabs-ai/nono \
  --signer-workflow nolabs-ai/nono/.github/workflows/release.yml; then
  echo continuing
fi`,
			want: "must not be used as a shell condition",
		},
		"fankle verify in elif condition": {
			run: `set -euo pipefail
if false; then
  echo skipped
elif gh attestation verify "$tmp/$tarball" --repo nolabs-ai/nono \
  --signer-workflow nolabs-ai/nono/.github/workflows/release.yml; then
  echo verified
fi`,
			want: "must not be used as a shell condition",
		},
		"strath negated verify": {
			run: `set -euo pipefail
! gh attestation verify "$tmp/$tarball" --repo nolabs-ai/nono \
  --signer-workflow nolabs-ai/nono/.github/workflows/release.yml`,
			want: "must not be negated",
		},
		"bothy backgrounded verify": {
			run: `set -euo pipefail
gh attestation verify "$tmp/$tarball" --repo nolabs-ai/nono \
  --signer-workflow nolabs-ai/nono/.github/workflows/release.yml &
tar -xzf "$tmp/$tarball"`,
			want: "must not be backgrounded",
		},
		"glaikit backgrounded verify after other background job": {
			run: `set -euo pipefail
sleep 1 & gh attestation verify "$tmp/$tarball" --repo nolabs-ai/nono \
  --signer-workflow nolabs-ai/nono/.github/workflows/release.yml &
tar -xzf "$tmp/$tarball"`,
			want: "must not be backgrounded",
		},
		"muckle brace grouped verify backgrounded": {
			run: `set -euo pipefail
{ gh attestation verify "$tmp/$tarball" --repo nolabs-ai/nono \
  --signer-workflow nolabs-ai/nono/.github/workflows/release.yml; } &
tar -xzf "$tmp/$tarball"`,
			want: "must not be backgrounded",
		},
		"snell command substitution guarded": {
			run: `set -euo pipefail
out=$(gh attestation verify "$tmp/$tarball" --repo nolabs-ai/nono \
  --signer-workflow nolabs-ai/nono/.github/workflows/release.yml) || true`,
			want: "must not be guarded with ||",
		},
		"birl pipeline guarded": {
			run: `set -euo pipefail
gh attestation verify "$tmp/$tarball" --repo nolabs-ai/nono \
  --signer-workflow nolabs-ai/nono/.github/workflows/release.yml | cat || true`,
			want: "must not be guarded with ||",
		},
		"crabbit backtick substitution guarded": {
			run:  "set -euo pipefail\nout=`gh attestation verify \"$tmp/$tarball\" --repo nolabs-ai/nono \\\n  --signer-workflow nolabs-ai/nono/.github/workflows/release.yml` || true",
			want: "must not be guarded with ||",
		},
		"gleg parenthesized verify guarded": {
			run: `(gh attestation verify "$tmp/$tarball" --repo nolabs-ai/nono \
  --signer-workflow nolabs-ai/nono/.github/workflows/release.yml ) || true`,
			want: "must not be guarded with ||",
		},
		"blether disabled pipefail": {
			run: `set -euo pipefail
set +o pipefail
gh attestation verify "$tmp/$tarball" --repo nolabs-ai/nono \
  --signer-workflow nolabs-ai/nono/.github/workflows/release.yml | cat`,
			want: "must not disable pipefail",
		},
		"smeddum combined disabled pipefail": {
			run: `set -euo pipefail
set +uo pipefail
gh attestation verify "$tmp/$tarball" --repo nolabs-ai/nono \
  --signer-workflow nolabs-ai/nono/.github/workflows/release.yml | cat`,
			want: "must not disable pipefail",
		},
		"scunnered combined disabled errexit": {
			run: `set -euo pipefail
set +uo errexit
gh attestation verify "$tmp/$tarball" --repo nolabs-ai/nono \
  --signer-workflow nolabs-ai/nono/.github/workflows/release.yml`,
			want: "must not disable errexit",
		},
		"wabbit missing pipefail": {
			run: `set -eu
gh attestation verify "$tmp/$tarball" --repo nolabs-ai/nono \
  --signer-workflow nolabs-ai/nono/.github/workflows/release.yml | cat`,
			want: "must enable errexit and pipefail",
		},
		"kenspeck verify before and list": {
			run: `set -euo pipefail
gh attestation verify "$tmp/$tarball" --repo nolabs-ai/nono \
  --signer-workflow nolabs-ai/nono/.github/workflows/release.yml && true`,
			want: "must not be guarded with &&",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			err := validateAttestationVerifyCommand(workflowExecutableLines(test.run), "Install nono", "nolabs-ai/nono", "nolabs-ai/nono/.github/workflows/release.yml")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validateAttestationVerifyCommand() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestAttestationVerifyCommandPolicyAcceptsCompliantForms(t *testing.T) {
	tests := map[string]string{
		"braw redirects stderr to stdout": `set -euo pipefail
gh attestation verify "$tmp/$tarball" --repo nolabs-ai/nono \
  --signer-workflow nolabs-ai/nono/.github/workflows/release.yml 2>&1`,
		"canny redirects stdout and stderr": `set -euo pipefail
gh attestation verify "$tmp/$tarball" --repo nolabs-ai/nono \
  --signer-workflow nolabs-ai/nono/.github/workflows/release.yml &> "$tmp/verify.log"`,
		"dreich verifies artifacts in for loop": `set -euo pipefail
for artifact in "$tmp"/*.tar.gz; do gh attestation verify "$artifact" --repo nolabs-ai/nono --signer-workflow nolabs-ai/nono/.github/workflows/release.yml; done`,
		"thrawn signer workflow equals form": `set -euo pipefail
gh attestation verify "$tmp/$tarball" --repo nolabs-ai/nono --signer-workflow=nolabs-ai/nono/.github/workflows/release.yml`,
		"gleg long set options": `set -o errexit -o pipefail
gh  attestation  verify "$tmp/$tarball" --repo nolabs-ai/nono --signer-workflow=nolabs-ai/nono/.github/workflows/release.yml`,
	}

	for name, run := range tests {
		t.Run(name, func(t *testing.T) {
			err := validateAttestationVerifyCommand(workflowExecutableLines(run), "Install nono", "nolabs-ai/nono", "nolabs-ai/nono/.github/workflows/release.yml")
			if err != nil {
				t.Fatalf("validateAttestationVerifyCommand() error = %v", err)
			}
		})
	}
}

func TestAttestationVerifySignerWorkflowFlagRequiresValue(t *testing.T) {
	tests := map[string]struct {
		command string
		want    bool
	}{
		"braw literal path": {
			command: `gh attestation verify f --repo nolabs-ai/nono --signer-workflow nolabs-ai/nono/.github/workflows/release.yml`,
			want:    true,
		},
		"canny quoted expression": {
			command: `gh attestation verify f --repo "$GITHUB_REPOSITORY" --signer-workflow "$GITHUB_REPOSITORY/.github/workflows/goreleaser.yml"`,
			want:    true,
		},
		"couthy equals form": {
			command: `gh attestation verify f --repo nolabs-ai/nono --signer-workflow=nolabs-ai/nono/.github/workflows/release.yml`,
			want:    true,
		},
		"dreich empty double quoted": {
			command: `gh attestation verify f --repo nolabs-ai/nono --signer-workflow ""`,
		},
		"thrawn empty single quoted": {
			command: `gh attestation verify f --repo nolabs-ai/nono --signer-workflow=''`,
		},
		"strath missing": {
			command: `gh attestation verify f --repo nolabs-ai/nono`,
		},
		"fankle whitespace only double quoted": {
			command: `gh attestation verify f --repo nolabs-ai/nono --signer-workflow "  "`,
		},
		"glaikit next flag consumed": {
			command: `gh attestation verify f --signer-workflow --repo nolabs-ai/nono`,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if got := attestationVerifyHasSignerWorkflow(test.command); got != test.want {
				t.Fatalf("attestationVerifyHasSignerWorkflow() = %t, want %t for %q", got, test.want, test.command)
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
	assertContains(t, makefile, "GOLANGCI_LINT_REGISTRY_AUTH_FILE ?= $(CURDIR)/scripts/public-registry-auth.json")
	assertContains(t, makefile, "GOLANGCI_LINT_DOCKER_BASE := REGISTRY_AUTH_FILE=$(GOLANGCI_LINT_REGISTRY_AUTH_FILE) docker run --rm")

	if got := strings.Count(makefile, "golangci/golangci-lint:"); got != 1 {
		t.Fatalf("Makefile golangci-lint image coordinate count = %d, want 1", got)
	}

	assertContains(t, renovate, "GOLANGCI_LINT_VERSION := (?<currentValue>v[\\\\d\\\\.]+)\\\\s+GOLANGCI_LINT_DIGEST := (?<currentDigest>sha256:[a-f0-9]{64})")
	assertContains(t, renovate, "autoReplaceStringTemplate: 'GOLANGCI_LINT_VERSION := {{{newValue}}}\\nGOLANGCI_LINT_DIGEST := {{{newDigest}}}',")
	assertNotContains(t, renovate, "pinDigests: false")
}

func TestGolangciLintUsesGomodguardV2(t *testing.T) {
	repoRoot := p11RepoRoot()
	config := readPolicyFile(t, filepath.Join(repoRoot, ".golangci.yml"))

	var parsed struct {
		Linters struct {
			Enable  []string `yaml:"enable"`
			Disable []string `yaml:"disable"`
		} `yaml:"linters"`
	}
	if err := yaml.Unmarshal([]byte(config), &parsed); err != nil {
		t.Fatalf("parse .golangci.yml: %v", err)
	}

	if !containsString(parsed.Linters.Enable, "gomodguard_v2") {
		t.Fatal(".golangci.yml must enable gomodguard_v2")
	}

	if containsString(parsed.Linters.Enable, "gomodguard") {
		t.Fatal(".golangci.yml must not enable deprecated gomodguard")
	}

	if !containsString(parsed.Linters.Disable, "gomodguard") {
		t.Fatal(".golangci.yml must disable deprecated gomodguard")
	}

	if containsString(parsed.Linters.Disable, "gomodguard_v2") {
		t.Fatal(".golangci.yml must not disable gomodguard_v2")
	}
}

func TestCIToolVersionsAreRenovateManaged(t *testing.T) {
	repoRoot := p11RepoRoot()
	pins := readPolicyFile(t, filepath.Join(repoRoot, ".github/ci-tool-versions.env"))
	renovate := readPolicyFile(t, filepath.Join(repoRoot, "renovate.json5"))

	assertRegexp(t, pins, `(?m)^COMMITSAR_VERSION=v\d+\.\d+\.\d+$`)
	assertRegexp(t, pins, `(?m)^HUGO_VERSION=\d+\.\d+\.\d+$`)
	assertRegexp(t, pins, `(?m)^K6_IMAGE=grafana/k6:\d+\.\d+\.\d+-with-browser@sha256:[a-f0-9]{64}$`)
	assertRegexp(t, pins, `(?m)^GOVULNCHECK_VERSION=v\d+\.\d+\.\d+$`)
	assertRegexp(t, pins, `(?m)^GORELEASER_VERSION=v\d+\.\d+\.\d+$`)
	assertRegexp(t, pins, `(?m)^GITLEAKS_IMAGE=ghcr\.io/gitleaks/gitleaks:v\d+\.\d+\.\d+@sha256:[a-f0-9]{64}$`)
	assertRegexp(t, pins, `(?m)^SCORECARD_IMAGE=ghcr\.io/ossf/scorecard-action:v\d+\.\d+\.\d+@sha256:[a-f0-9]{64}$`)
	assertRegexp(t, pins, `(?m)^TRUFFLEHOG_IMAGE=ghcr\.io/trufflesecurity/trufflehog:\d+\.\d+\.\d+@sha256:[a-f0-9]{64}$`)

	for _, workflowPath := range []string{
		".github/workflows/commits.yml",
		".github/workflows/ci.yml",
		".github/workflows/docs.yml",
		".github/workflows/docs-preview.yml",
		".github/workflows/dev-release.yml",
		".github/workflows/goreleaser.yml",
		".github/workflows/scorecard.yml",
		".github/workflows/secret-scan.yml",
	} {
		path := filepath.Join(repoRoot, workflowPath)
		workflow := readPolicyFile(t, path)
		assertContains(t, workflow, ".github/ci-tool-versions.env")
		assertCIToolVersionsLoadOrder(t, path)
	}

	assertContains(t, renovate, "COMMITSAR_VERSION=(?<currentValue>v[\\\\d.]+)")
	assertContains(t, renovate, "depNameTemplate: 'github.com/aevea/commitsar'")
	assertContains(t, renovate, "HUGO_VERSION=(?<currentValue>[\\\\d.]+)")
	assertContains(t, renovate, "K6_IMAGE=(?<packageName>grafana/k6):(?<currentValue>[\\\\w.-]+)@(?<currentDigest>sha256:[a-f0-9]{64})")
	assertContains(t, renovate, "autoReplaceStringTemplate: 'K6_IMAGE=grafana/k6:{{{newValue}}}@{{{newDigest}}}',")
	assertContains(t, renovate, "GOVULNCHECK_VERSION=(?<currentValue>v[\\\\d.]+)")
	assertContains(t, renovate, "depNameTemplate: 'golang.org/x/vuln/cmd/govulncheck'")
	assertContains(t, renovate, "packageNameTemplate: 'golang.org/x/vuln'")
	assertContains(t, renovate, "GORELEASER_VERSION=(?<currentValue>v[\\\\d.]+)")
	assertContains(t, renovate, "depNameTemplate: 'goreleaser/goreleaser'")
	assertContains(t, renovate, "GITLEAKS_IMAGE=(?<packageName>ghcr\\\\.io/gitleaks/gitleaks):(?<currentValue>v[\\\\w.-]+)@(?<currentDigest>sha256:[a-f0-9]{64})")
	assertContains(t, renovate, "autoReplaceStringTemplate: 'GITLEAKS_IMAGE=ghcr.io/gitleaks/gitleaks:{{{newValue}}}@{{{newDigest}}}',")
	assertContains(t, renovate, "depNameTemplate: 'gitleaks/gitleaks'")
	assertContains(t, renovate, "SCORECARD_IMAGE=(?<packageName>ghcr\\\\.io/ossf/scorecard-action):(?<currentValue>v[\\\\w.-]+)@(?<currentDigest>sha256:[a-f0-9]{64})")
	assertContains(t, renovate, "autoReplaceStringTemplate: 'SCORECARD_IMAGE=ghcr.io/ossf/scorecard-action:{{{newValue}}}@{{{newDigest}}}',")
	assertContains(t, renovate, "depNameTemplate: 'ossf/scorecard-action'")
	assertContains(t, renovate, "TRUFFLEHOG_IMAGE=(?<packageName>ghcr\\\\.io/trufflesecurity/trufflehog):(?<currentValue>[\\\\w.-]+)@(?<currentDigest>sha256:[a-f0-9]{64})")
	assertContains(t, renovate, "autoReplaceStringTemplate: 'TRUFFLEHOG_IMAGE=ghcr.io/trufflesecurity/trufflehog:{{{newValue}}}@{{{newDigest}}}',")
	assertContains(t, renovate, "depNameTemplate: 'trufflesecurity/trufflehog'")

	scorecard, err := ReadP11WorkflowSummary(filepath.Join(repoRoot, ".github/workflows/scorecard.yml"))
	if err != nil {
		t.Fatal(err)
	}

	scorecardAnalysis := p11WorkflowJob(t, scorecard, "analysis")
	scorecardRun := p11WorkflowStep(t, scorecardAnalysis, "Run analysis")

	for _, want := range []string{
		`[[ ! "$SCORECARD_IMAGE" =~ ^ghcr[.]io/ossf/scorecard-action:`,
		`immutable_image="${image_repo}@${image_digest}"`,
		`docker run --rm \`,
		`-e ACTIONS_ID_TOKEN_REQUEST_TOKEN`,
		`-e ACTIONS_ID_TOKEN_REQUEST_URL`,
		`-e GITHUB_WORKSPACE=/github/workspace`,
		`-e GITHUB_EVENT_PATH=/github/event.json`,
		`-e INPUT_INTERNAL_DEFAULT_TOKEN`,
		`-e INPUT_PUBLISH_RESULTS`,
		`-e INPUT_REPO_TOKEN`,
		`-v "$PWD:/github/workspace"`,
		`-v "$GITHUB_EVENT_PATH:/github/event.json:ro"`,
		`"$immutable_image"`,
	} {
		assertContains(t, scorecardRun.Run, want)
	}

	if scorecardRun.Env["INPUT_RESULTS_FILE"] != "results.sarif" {
		t.Fatalf("Scorecard results file env = %q, want results.sarif", scorecardRun.Env["INPUT_RESULTS_FILE"])
	}

	if scorecardRun.Env["INPUT_RESULTS_FORMAT"] != "sarif" {
		t.Fatalf("Scorecard results format env = %q, want sarif", scorecardRun.Env["INPUT_RESULTS_FORMAT"])
	}

	if scorecardRun.Env["INPUT_PUBLISH_RESULTS"] != "true" {
		t.Fatalf("Scorecard publish env = %q, want true", scorecardRun.Env["INPUT_PUBLISH_RESULTS"])
	}

	commits, err := ReadP11WorkflowSummary(filepath.Join(repoRoot, ".github/workflows/commits.yml"))
	if err != nil {
		t.Fatal(err)
	}

	commitsarJob := p11WorkflowJob(t, commits, "commitsar")
	commitsarInstall := p11WorkflowStep(t, commitsarJob, "Install Commitsar")

	for _, want := range []string{
		`[[ ! "$COMMITSAR_VERSION" =~ ^v[0-9]+[.][0-9]+[.][0-9]+$ ]]`,
		`Clear GONOSUMDB so inherited private-module settings cannot bypass`,
		`GOBIN="$bin_dir" GONOSUMDB="" GOSUMDB=sum.golang.org go install "github.com/aevea/commitsar@$COMMITSAR_VERSION"`,
		`go version -m "$bin_dir/commitsar" | awk -v version="$COMMITSAR_VERSION"`,
		`$1 == "mod" && $2 == "github.com/aevea/commitsar" && $3 == version { found = 1 }`,
		`END { exit found ? 0 : 1 }`,
		`printf '%s\n' "$bin_dir" >> "$GITHUB_PATH"`,
	} {
		assertContains(t, commitsarInstall.Run, want)
	}

	commitsarRun := p11WorkflowStep(t, commitsarJob, "Conventional commits")
	if commitsarRun.Run != "commitsar --config-path=." {
		t.Fatalf("Commitsar run = %q, want commitsar --config-path=.", commitsarRun.Run)
	}

	secretScan, err := ReadP11WorkflowSummary(filepath.Join(repoRoot, ".github/workflows/secret-scan.yml"))
	if err != nil {
		t.Fatal(err)
	}

	trufflehog := p11WorkflowStep(t, p11WorkflowJob(t, secretScan, "trufflehog"), "TruffleHog (verified secrets only)")
	for _, want := range []string{
		`[[ ! "$TRUFFLEHOG_IMAGE" =~ ^ghcr[.]io/trufflesecurity/trufflehog:`,
		`immutable_image="${image_repo}@${image_digest}"`,
		`Missing TruffleHog range`,
		`docker run --rm -v "$PWD:/repo:ro" -w /repo "$immutable_image" "${args[@]}"`,
		`--fail`,
		`--no-update`,
		`--only-verified`,
		`args+=(--since-commit "$base")`,
		`args+=(--branch "$head")`,
	} {
		assertContains(t, trufflehog.Run, want)
	}

	gitleaks := p11WorkflowStep(t, p11WorkflowJob(t, secretScan, "gitleaks"), "Gitleaks")
	for _, want := range []string{
		`[[ ! "$GITLEAKS_IMAGE" =~ ^ghcr[.]io/gitleaks/gitleaks:`,
		`immutable_image="${image_repo}@${image_digest}"`,
		`docker run --rm \`,
		`-v "$PWD:/repo:ro"`,
		`--entrypoint sh`,
		`git config --global --add safe.directory /repo && exec gitleaks "$@"`,
		`detect`,
		`--exit-code=2`,
		`--report-format=sarif`,
		`args+=(--log-opts="$log_opts")`,
	} {
		assertContains(t, gitleaks.Run, want)
	}

	gitleaksUpload := p11WorkflowStep(t, p11WorkflowJob(t, secretScan, "gitleaks"), "Upload Gitleaks SARIF")
	if gitleaksUpload.Uses != "actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a" {
		t.Fatalf("Gitleaks SARIF upload action = %q, want pinned upload-artifact v7", gitleaksUpload.Uses)
	}

	if gitleaksUpload.If != "always()" {
		t.Fatalf("Gitleaks SARIF upload if = %q, want always()", gitleaksUpload.If)
	}

	if got := gitleaksUpload.With["if-no-files-found"]; got != "ignore" {
		t.Fatalf("Gitleaks SARIF upload if-no-files-found = %q, want ignore", got)
	}

	for _, workflowPath := range []string{
		".github/workflows/dev-release.yml",
		".github/workflows/goreleaser.yml",
	} {
		workflow := readPolicyFile(t, filepath.Join(repoRoot, workflowPath))
		assertNotContains(t, workflow, "~> v2")
		assertNotContains(t, workflow, "goreleaser/goreleaser-action")
		assertContains(t, workflow, "all_count=\"$(grep -Ec '^GORELEASER_VERSION=' \"$pin_file\" || :)\"")
		assertContains(t, workflow, "valid_count=\"$(grep -Ec '^GORELEASER_VERSION=v[0-9]+\\.[0-9]+\\.[0-9]+([.-][0-9A-Za-z.-]+)?$' \"$pin_file\" || :)\"")
		assertContains(t, workflow, "scripts/install-goreleaser.sh \"$GORELEASER_VERSION\"")
		assertContains(t, workflow, "goreleaser release")
	}
}

func TestCommitsarInstallScriptChecksGoModuleMetadata(t *testing.T) {
	t.Parallel()

	commits, err := ReadP11WorkflowSummary(filepath.Join(p11RepoRoot(), ".github/workflows/commits.yml"))
	if err != nil {
		t.Fatal(err)
	}

	commitsarJob := p11WorkflowJob(t, commits, "commitsar")

	commitsarInstall := p11WorkflowStep(t, commitsarJob, "Install Commitsar")
	if commitsarInstall.Run == "" {
		t.Fatal("Commitsar install step has no run script")
	}

	tests := map[string]struct {
		metadataVersion string
		wantErr         bool
	}{
		"matching module metadata passes": {
			metadataVersion: "v1.0.3",
		},
		"mismatched module metadata fails": {
			metadataVersion: "v1.0.4",
			wantErr:         true,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			binDir := filepath.Join(dir, "bin")
			runnerTemp := filepath.Join(dir, "runner")
			githubPath := filepath.Join(dir, "github_path")
			scriptPath := filepath.Join(dir, "install-commitsar.sh")

			if err := os.MkdirAll(binDir, 0o750); err != nil {
				t.Fatal(err)
			}

			writeTestExecutable(t, filepath.Join(binDir, "go"), fakeCommitsarGoScript)

			if err := os.WriteFile(scriptPath, []byte(commitsarInstall.Run), 0o600); err != nil {
				t.Fatal(err)
			}

			cmd := exec.Command("bash", scriptPath)

			cmd.Env = append(os.Environ(),
				"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
				"COMMITSAR_VERSION=v1.0.3",
				"GITHUB_PATH="+githubPath,
				"RUNNER_TEMP="+runnerTemp,
				"GRAITH_FAKE_COMMITSAR_METADATA_VERSION="+test.metadataVersion,
			)

			output, err := cmd.CombinedOutput()
			if test.wantErr {
				if err == nil {
					t.Fatalf("Commitsar install succeeded unexpectedly:\n%s", output)
				}

				if _, statErr := os.Stat(githubPath); !os.IsNotExist(statErr) {
					t.Fatalf("GITHUB_PATH was updated despite failed metadata verification: %v", statErr)
				}

				return
			}

			if err != nil {
				t.Fatalf("Commitsar install failed: %v\n%s", err, output)
			}

			wantPath := filepath.Join(runnerTemp, "commitsar-bin")

			githubPathData, err := os.ReadFile(githubPath)
			if err != nil {
				t.Fatal(err)
			}

			assertContains(t, string(githubPathData), wantPath)

			if _, err := os.Stat(filepath.Join(wantPath, "commitsar")); err != nil {
				t.Fatal(err)
			}
		})
	}
}

const fakeCommitsarGoScript = `#!/bin/sh
set -eu

case "$1" in
  install)
    if [ "$2" != "github.com/aevea/commitsar@v1.0.3" ]; then
      echo "unexpected go install target: $2" >&2
      exit 99
    fi
    if [ "${GONOSUMDB+x}" != "x" ] || [ -n "$GONOSUMDB" ]; then
      echo "GONOSUMDB must be set and empty" >&2
      exit 99
    fi
    if [ "${GOSUMDB:-}" != "sum.golang.org" ]; then
      echo "GOSUMDB must use the public checksum database" >&2
      exit 99
    fi
    mkdir -p "$GOBIN"
    printf '#!/bin/sh\nexit 0\n' >"$GOBIN/commitsar"
    chmod +x "$GOBIN/commitsar"
    ;;
  version)
    if [ "$2" != "-m" ]; then
      echo "unexpected go version flag: $2" >&2
      exit 99
    fi
    printf '%s: go1.26.5\n' "$3"
    printf 'path\tgithub.com/aevea/commitsar\n'
    printf 'mod\tgithub.com/aevea/commitsar\t%s\th1:canny\n' "$GRAITH_FAKE_COMMITSAR_METADATA_VERSION"
    ;;
  *)
    echo "unexpected go command: $*" >&2
    exit 99
    ;;
esac
`

func TestGoReleaserInstallFailsClosedOnMissingChecksum(t *testing.T) {
	repoRoot := p11RepoRoot()
	script := readPolicyFile(t, filepath.Join(repoRoot, "scripts/install-goreleaser.sh"))

	assertContains(t, script, `^v[0-9]+\.[0-9]+\.[0-9]+`)
	assertContains(t, script, `curl_args=(`)
	assertContains(t, script, `--proto '=https'`)
	assertContains(t, script, `--tlsv1.2`)
	assertContains(t, script, `--fail`)
	assertContains(t, script, `--retry 4`)
	assertContains(t, script, `--retry-max-time 120`)
	assertContains(t, script, `curl "${curl_args[@]}"`)
	assertContains(t, script, `"${base_url}/checksums.txt" --output "$checksums"`)
	assertContains(t, script, `checksums.txt does not contain exactly one checksum`)
	assertContains(t, script, `shasum -a 256 -c -`)
	assertContains(t, script, `sha256sum -c -`)
	assertContains(t, script, `tar -xzf "$archive" -C "$extract_dir" goreleaser`)
	assertContains(t, script, `"${dest}/goreleaser" --version | grep -F "${version#v}"`)
}

func TestRenovateIgnoresNativeGeneratedCommitAuthor(t *testing.T) {
	repoRoot := p11RepoRoot()
	renovate := readPolicyFile(t, filepath.Join(repoRoot, "renovate.json5"))
	workflow := readPolicyFile(t, filepath.Join(repoRoot, ".github/workflows/libghostty-native-artifacts.yml"))

	generatedAuthor := "github-actions[bot] <41898282+github-actions[bot]@users.noreply.github.com>"
	assertContains(t, renovate, "gitIgnoredAuthors: ['"+generatedAuthor+"']")
	assertContains(t, workflow, `git show -s --format=%an "$GENERATED_SHA")" != "github-actions[bot]"`)
	assertContains(t, workflow, `git show -s --format=%ae "$GENERATED_SHA")" != "41898282+github-actions[bot]@users.noreply.github.com"`)
}

func assertCIToolVersionsLoadOrder(t *testing.T, workflowPath string) {
	t.Helper()

	workflow, err := ReadP11WorkflowSummary(workflowPath)
	if err != nil {
		t.Fatal(err)
	}

	for jobID, job := range workflow.Jobs {
		checkoutIndex := -1
		loadIndex := -1
		firstConsumerIndex := -1

		for index, step := range job.Steps {
			if checkoutIndex == -1 && strings.HasPrefix(step.Uses, "actions/checkout@") {
				checkoutIndex = index
			}

			if step.Name == "Load CI tool versions" {
				loadIndex = index
			}

			if firstConsumerIndex == -1 &&
				step.Name != "Load CI tool versions" &&
				stepConsumesCIToolVersion(step) {
				firstConsumerIndex = index
			}
		}

		if loadIndex == -1 && firstConsumerIndex == -1 {
			continue
		}

		if loadIndex == -1 {
			t.Fatalf("%s job %s consumes CI tool versions without a Load CI tool versions step", workflowPath, jobID)
		}

		if checkoutIndex == -1 {
			t.Fatalf("%s job %s loads CI tool versions without actions/checkout", workflowPath, jobID)
		}

		if loadIndex <= checkoutIndex {
			t.Fatalf("%s job %s loads CI tool versions before checkout", workflowPath, jobID)
		}

		if firstConsumerIndex != -1 && loadIndex >= firstConsumerIndex {
			t.Fatalf("%s job %s loads CI tool versions after first consumer", workflowPath, jobID)
		}
	}
}

func stepConsumesCIToolVersion(step P11WorkflowStep) bool {
	for _, name := range []string{"COMMITSAR_VERSION", "HUGO_VERSION", "K6_IMAGE", "GOVULNCHECK_VERSION", "GORELEASER_VERSION", "GITLEAKS_IMAGE", "SCORECARD_IMAGE", "TRUFFLEHOG_IMAGE"} {
		if strings.Contains(step.Run, name) {
			return true
		}

		for _, value := range step.With {
			if strings.Contains(value, name) {
				return true
			}
		}
	}

	return false
}

func TestGolangciLintBuildTagCoverage(t *testing.T) {
	repoRoot := p11RepoRoot()
	makefile := readPolicyFile(t, filepath.Join(repoRoot, "Makefile"))
	config := readPolicyFile(t, filepath.Join(repoRoot, ".golangci.yml"))
	ciWorkflowText := readPolicyFile(t, filepath.Join(repoRoot, ".github/workflows/ci.yml"))
	docs := readPolicyFile(t, filepath.Join(repoRoot, "website/content/docs/contributing/_index.md"))
	nativeScript := readPolicyFile(t, filepath.Join(repoRoot, "scripts/libghostty-native.sh"))

	assertContains(t, config, "build-tags:\n    - integration")
	assertContains(t, config, "lint-libghostty target")

	assertContains(t, makefile, "lint-darwin:")
	assertContains(t, makefile, "-e GOOS=darwin -e CGO_ENABLED=0")
	assertContains(t, makefile, "lint-libghostty:")
	assertContains(t, makefile, "GOLANGCI_LINT_LIBGHOSTTY_PACKAGES := ./internal/pty ./internal/daemon ./cmd/graith")
	assertContains(t, makefile, "scripts/libghostty-native.sh prepare-linux-artifact")
	assertContains(t, makefile, "--build-tags=integration,libghostty")
	assertContains(t, makefile, "PKG_CONFIG_PATH=\"/app/.lint-libghostty-linux-$$goarch/pkgconfig\"")

	workflow, err := ReadP11WorkflowSummary(filepath.Join(repoRoot, ".github/workflows/ci.yml"))
	if err != nil {
		t.Fatal(err)
	}

	lint := p11WorkflowJob(t, workflow, "lint")
	setupGo := false

	for _, step := range lint.Steps {
		if strings.Contains(step.Uses, "actions/setup-go") {
			setupGo = true
		}
	}

	if !setupGo {
		t.Fatal("CI lint job must set up Go for the libghostty artifact helper")
	}

	assertContains(t, p11WorkflowStep(t, lint, "Lint default and integration tags").Run, "make lint-only")
	assertContains(t, p11WorkflowStep(t, lint, "Lint Darwin non-cgo files").Run, "make lint-darwin")
	zigSetup := p11WorkflowStep(t, lint, "Install checksum-verified Zig from the native dependency lock").Run
	assertContains(t, zigSetup, "set -euo pipefail")
	assertContains(t, zigSetup, "jq -er '.zig.version' libghostty-native.lock.json")
	assertContains(t, zigSetup, "jq -er '.zig.linuxX8664URL' libghostty-native.lock.json")
	assertContains(t, zigSetup, "jq -er '.zig.linuxX8664SHA256' libghostty-native.lock.json")
	assertContains(t, zigSetup, "curl --proto '=https' --tlsv1.2 --fail --location --silent --show-error")
	assertContains(t, zigSetup, "sha256sum --check --status")
	assertContains(t, zigSetup, "echo \"${RUNNER_TEMP}/zig\" >> \"$GITHUB_PATH\"")
	assertContains(t, zigSetup, "test \"$(\"${RUNNER_TEMP}/zig/zig\" version)\" = \"$zig_version\"")
	assertContains(t, p11WorkflowStep(t, lint, "Lint Linux libghostty tag").Run, "make lint-libghostty")

	assertContains(t, ciWorkflowText, "go run ./cmd/ciclassify -mode ci")

	classification, err := ClassifyWorkflowPaths([]string{"internal/daemon/hooks.go"})
	if err != nil {
		t.Fatal(err)
	}

	if !classification.CIMacOS {
		t.Fatalf("internal/daemon change classified as macOS-relevant = false, want true")
	}

	assertContains(t, nativeScript, "TestDiagnostics|TestLogTerminalBackendSelectionFields|TestFSEvents")
	assertContains(t, docs, "make lint-darwin")
	assertContains(t, docs, "make lint-libghostty")
	assertContains(t, docs, "the pinned Zig toolchain")
	assertContains(t, docs, "Darwin `cgo` code, including")
	assertContains(t, docs, "macOS build/test lanes compile that surface")
	assertContains(t, docs, "run the FSEvents and")
	assertContains(t, docs, "native terminal tests")
}

func TestLibghosttyArchivePolicyFixtureUsesPinnedZigVersion(t *testing.T) {
	repoRoot := p11RepoRoot()
	lockText := readPolicyFile(t, filepath.Join(repoRoot, "libghostty-native.lock.json"))
	nativeScript := readPolicyFile(t, filepath.Join(repoRoot, "scripts/libghostty-native.sh"))

	var lock struct {
		Zig struct {
			Version string `json:"version"`
		} `json:"zig"`
	}
	if err := json.Unmarshal([]byte(lockText), &lock); err != nil {
		t.Fatalf("parse native dependency lock: %v", err)
	}

	if lock.Zig.Version == "" {
		t.Fatal("native dependency lock is missing zig.version")
	}

	markerLine := libghosttyArchivePolicyZigMarkerLine(nativeScript)
	if markerLine == "" {
		t.Fatal("archive policy fixture does not embed a Zig marker")
	}

	if !strings.Contains(markerLine, "$REQUIRED_ZIG") {
		t.Fatalf("archive policy fixture Zig marker = %q, want lock-driven $REQUIRED_ZIG", markerLine)
	}

	if strings.Contains(markerLine, lock.Zig.Version) {
		t.Fatalf("archive policy fixture hard-codes current Zig version %q: %q", lock.Zig.Version, markerLine)
	}
}

func libghosttyArchivePolicyZigMarkerLine(nativeScript string) string {
	for _, line := range strings.Split(nativeScript, "\n") {
		if strings.Contains(line, "graith_zig_version") {
			return line
		}
	}

	return ""
}

func validateAttestationVerifyCommand(code, stepName, repository, signerWorkflow string) error {
	if err := validateAttestationVerifySafety(code, stepName); err != nil {
		return err
	}

	repoFlag := regexp.MustCompile(`(?:^|\s)--repo(?:=|\s+)` + regexp.QuoteMeta(repository) + `(?:\s|$)`)
	signerFlag := regexp.MustCompile(`(?:^|\s)--signer-workflow(?:=|\s+)` + regexp.QuoteMeta(signerWorkflow) + `(?:\s|$)`)

	commands := workflowAttestationVerifyCommands(code)
	if len(commands) == 0 {
		return fmt.Errorf("%s must verify provenance against %s and signer workflow %s on the same attestation command line:\n%s", stepName, repository, signerWorkflow, code)
	}

	for _, command := range commands {
		hasRepo := repoFlag.MatchString(command)
		hasSigner := signerFlag.MatchString(command)

		if !hasSigner {
			return fmt.Errorf("%s must bind provenance to signer workflow %s:\n%s", stepName, signerWorkflow, code)
		}

		if !hasRepo {
			return fmt.Errorf("%s must verify provenance against %s on the attestation command line:\n%s", stepName, repository, code)
		}
	}

	return nil
}

// Attestation verification safety is intentionally step-wide: once a step
// verifies provenance, later shell changes in that step must not make failures
// non-fatal before the artifact is consumed.
func validateAttestationVerifySafety(code, stepName string) error {
	if attestationVerifyDisablesShellOption(code, 'e', "errexit") {
		return fmt.Errorf("%s must not disable errexit", stepName)
	}

	if attestationVerifyDisablesShellOption(code, 0, "pipefail") {
		return fmt.Errorf("%s must not disable pipefail", stepName)
	}

	switch operator := attestationVerifyFailOpenOperator(code); operator {
	case "||":
		return fmt.Errorf("%s verification must not be guarded with ||", stepName)
	case "&&":
		return fmt.Errorf("%s verification must not be guarded with &&", stepName)
	case "&":
		return fmt.Errorf("%s verification must not be backgrounded", stepName)
	}

	if regexp.MustCompile(`(?m)(?:^|[;&|]\s*)(?:if|elif|while|until)\b[^\n;]*\bgh attestation verify\b`).MatchString(code) {
		return fmt.Errorf("%s verification must not be used as a shell condition", stepName)
	}

	if regexp.MustCompile(`(?m)(?:^|[;&|]\s*)!\s*gh attestation verify\b`).MatchString(code) {
		return fmt.Errorf("%s verification must not be negated", stepName)
	}

	if !attestationVerifyEnablesErrexitAndPipefail(code) {
		return fmt.Errorf("%s verification must enable errexit and pipefail", stepName)
	}

	return nil
}

func attestationVerifyEnablesErrexitAndPipefail(code string) bool {
	return attestationVerifyShellOptionEnabled(code, 'e', "errexit") &&
		attestationVerifyShellOptionEnabled(code, 0, "pipefail")
}

func attestationVerifyDisablesShellOption(code string, shortOption byte, longOption string) bool {
	return attestationVerifyShellOptionState(code, shortOption, longOption, '+')
}

func attestationVerifyShellOptionEnabled(code string, shortOption byte, longOption string) bool {
	return attestationVerifyShellOptionState(code, shortOption, longOption, '-')
}

func attestationVerifyShellOptionState(code string, shortOption byte, longOption string, prefix byte) bool {
	for _, line := range strings.Split(code, "\n") {
		for _, command := range splitShellCommands(line) {
			fields := strings.Fields(command)
			if len(fields) == 0 || fields[0] != "set" {
				continue
			}

			for index := 1; index < len(fields); index++ {
				field := fields[index]
				if !strings.HasPrefix(field, string(prefix)) {
					continue
				}

				options := strings.TrimPrefix(field, string(prefix))
				if shortOption != 0 && strings.ContainsRune(options, rune(shortOption)) {
					return true
				}

				if strings.ContainsRune(options, 'o') && index+1 < len(fields) && fields[index+1] == longOption {
					return true
				}
			}
		}
	}

	return false
}

func workflowAttestationVerifyCommands(code string) []string {
	var commands []string

	for _, line := range strings.Split(code, "\n") {
		for _, command := range splitShellCommands(line) {
			if isAttestationVerifyCommand(command) {
				commands = append(commands, command)
			}
		}
	}

	return commands
}

func isAttestationVerifyCommand(command string) bool {
	return regexp.MustCompile(`(?:^|[^A-Za-z0-9_./-])gh\s+attestation\s+verify\b`).MatchString(command)
}

func attestationVerifyHasSignerWorkflow(command string) bool {
	return regexp.MustCompile(`(?:^|\s)--signer-workflow(?:=|\s+)(?:"[^"\s]+"|'[^'\s]+'|[^\s'"\-][^\s'"]*)(?:\s|$)`).MatchString(command)
}

func attestationVerifyFailOpenOperator(code string) string {
	for _, line := range strings.Split(code, "\n") {
		if operator := lineAttestationVerifyFailOpenOperator(line); operator != "" {
			return operator
		}
	}

	return ""
}

func lineAttestationVerifyFailOpenOperator(line string) string {
	var (
		escaped  bool
		inSingle bool
		inDouble bool
		start    int
		bgStart  int
		piped    bool
	)

	for index := 0; index < len(line); index++ {
		char := line[index]

		if escaped {
			escaped = false
			continue
		}

		if char == '\\' && !inSingle {
			escaped = true
			continue
		}

		switch char {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case ';', '|':
			if !inSingle && !inDouble {
				segmentHasVerify := isAttestationVerifyCommand(strings.TrimSpace(line[start:index]))
				if char == '|' && index+1 < len(line) && line[index+1] == '|' {
					if segmentHasVerify || piped {
						return "||"
					}

					piped = false
				} else if char == '|' {
					piped = piped || segmentHasVerify
				} else {
					piped = false
				}

				start = nextShellSegmentStart(line, index)
				index = start - 1
			}
		case '&':
			if inSingle || inDouble {
				continue
			}

			if isShellRedirectionAmpersand(line, index) {
				continue
			}

			if index+1 < len(line) && line[index+1] == '&' {
				if isAttestationVerifyCommand(strings.TrimSpace(line[start:index])) || piped {
					return "&&"
				}

				start = index + 2
				bgStart = start
				piped = false
				index++

				continue
			}

			if isAttestationVerifyCommand(strings.TrimSpace(line[start:index])) ||
				piped ||
				backgroundGroupContainsAttestationVerify(line[bgStart:index]) {
				return "&"
			}

			start = index + 1
			bgStart = start
			piped = false
		}
	}

	return ""
}

func backgroundGroupContainsAttestationVerify(segment string) bool {
	trimmed := strings.TrimSpace(segment)
	if !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "(") {
		return false
	}

	return isAttestationVerifyCommand(trimmed)
}

func nextShellSegmentStart(line string, separatorIndex int) int {
	if separatorIndex+1 < len(line) && line[separatorIndex+1] == line[separatorIndex] && (line[separatorIndex] == '&' || line[separatorIndex] == '|') {
		return separatorIndex + 2
	}

	return separatorIndex + 1
}

func isShellRedirectionAmpersand(line string, index int) bool {
	if index+1 < len(line) && line[index+1] == '>' {
		return true
	}

	return index > 0 && (line[index-1] == '>' || line[index-1] == '<')
}

func splitShellCommands(line string) []string {
	var (
		commands []string
		escaped  bool
		inSingle bool
		inDouble bool
		start    int
	)

	for index := 0; index < len(line); index++ {
		char := line[index]

		if escaped {
			escaped = false
			continue
		}

		if char == '\\' && !inSingle {
			escaped = true
			continue
		}

		switch char {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case ';', '&', '|':
			if inSingle || inDouble {
				continue
			}

			if char == '&' && isShellRedirectionAmpersand(line, index) {
				continue
			}

			if command := strings.TrimSpace(line[start:index]); command != "" {
				commands = append(commands, command)
			}

			if index+1 < len(line) && line[index+1] == char && (char == '&' || char == '|') {
				index++
			}

			start = index + 1
		}
	}

	if command := strings.TrimSpace(line[start:]); command != "" {
		commands = append(commands, command)
	}

	return commands
}

func workflowExecutableLines(run string) string {
	lines := strings.Split(run, "\n")

	var (
		filtered []string
		current  string
	)

	for _, line := range lines {
		line = strings.TrimRight(stripShellLineComment(line), " \t")

		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		continued := strings.HasSuffix(trimmed, `\`)
		if continued {
			trimmed = strings.TrimSpace(strings.TrimSuffix(trimmed, `\`))
		}

		if current == "" {
			current = trimmed
		} else {
			current += " " + trimmed
		}

		if !continued {
			filtered = append(filtered, current)
			current = ""
		}
	}

	if current != "" {
		filtered = append(filtered, current)
	}

	return strings.Join(filtered, "\n")
}

func stripShellLineComment(line string) string {
	var (
		escaped      bool
		inSingle     bool
		inDouble     bool
		previousRune rune
	)

	for index, char := range line {
		if escaped {
			escaped = false
			previousRune = char

			continue
		}

		if char == '\\' && !inSingle {
			escaped = true
			previousRune = char

			continue
		}

		switch char {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '#':
			if !inSingle && !inDouble && (index == 0 || previousRune == ' ' || previousRune == '\t') {
				return line[:index]
			}
		}

		previousRune = char
	}

	return line
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
