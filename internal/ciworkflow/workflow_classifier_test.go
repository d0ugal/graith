package ciworkflow

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

type workflowClassifierFixtureFile struct {
	SchemaVersion int                         `json:"schema_version"`
	Cases         []workflowClassifierFixture `json:"cases"`
}

type workflowClassifierFixture struct {
	Name  string                  `json:"name"`
	Event string                  `json:"event"`
	Files []string                `json:"files"`
	Want  WorkflowClassifications `json:"want"`
}

func TestWorkflowClassifierParityFixtures(t *testing.T) {
	t.Parallel()

	fixtures := loadWorkflowClassifierFixtures(t)
	legacyMatchers := currentWorkflowLegacyMatchers(t)

	if fixtures.SchemaVersion != 1 {
		t.Fatalf("fixture schema version = %d, want 1", fixtures.SchemaVersion)
	}

	for _, test := range fixtures.Cases {
		t.Run(test.Name, func(t *testing.T) {
			t.Parallel()

			if test.Event != "pull_request" {
				t.Fatalf("fixture event = %q, want pull_request", test.Event)
			}

			got, err := ClassifyWorkflowPaths(test.Files)
			if err != nil {
				t.Fatal(err)
			}

			if got != test.Want {
				t.Fatalf("shared classifier = %#v, want fixture %#v", got, test.Want)
			}

			legacy := legacyWorkflowClassification(test.Files, legacyMatchers)
			if got != legacy {
				t.Fatalf("shared classifier = %#v, want legacy parity %#v", got, legacy)
			}
		})
	}
}

func TestWorkflowClassifierTreatsPolicyChangesAsMigratedGatingConsumers(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"ci policy workflow":      ".github/workflows/coverage.yml",
		"shared classifier":       "cmd/ciclassify/main.go",
		"shared workflow package": "internal/ciworkflow/workflow_classifier.go",
	}

	for name, path := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := ClassifyWorkflowPaths([]string{path})
			if err != nil {
				t.Fatal(err)
			}

			if !got.CIMacOS ||
				!got.CoverageGUI ||
				!got.SandboxMacOS ||
				!got.LibghosttyNative ||
				!got.LibghosttyDependencyUnit {
				t.Fatalf("policy classifier change = %#v, want every migrated gating output true", got)
			}

			if got.DocsPreviewTrigger || got.DocsPreviewBuild || got.DocsPreviewGlobal {
				t.Fatalf("policy classifier change = %#v, want docs-preview outputs false", got)
			}
		})
	}
}

func TestWorkflowClassifierReleaseModesMatchCurrentClassifiersForPolicyPaths(t *testing.T) {
	t.Parallel()

	legacyMatchers := currentWorkflowLegacyMatchers(t)
	tests := map[string]string{
		"ci policy workflow":        ".github/workflows/coverage.yml",
		"dev release workflow":      ".github/workflows/dev-release.yml",
		"stable release workflow":   ".github/workflows/goreleaser.yml",
		"shared classifier command": "cmd/ciclassify/main.go",
		"shared workflow package":   "internal/ciworkflow/workflow_classifier.go",
	}

	for name, path := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := ClassifyWorkflowPaths([]string{path})
			if err != nil {
				t.Fatal(err)
			}

			if want := legacyMatchers.devRelease.MatchString(path); got.DevRelease != want {
				t.Fatalf("DevRelease for %s = %t, want current dev-release classifier %t", path, got.DevRelease, want)
			}

			if want := legacyMatchers.stableRelease.MatchString(path); got.StableRelease != want {
				t.Fatalf("StableRelease for %s = %t, want current stable-release classifier %t", path, got.StableRelease, want)
			}
		})
	}
}

func TestDevReleaseWorkflowRoutingPreservesEventSemantics(t *testing.T) {
	t.Parallel()

	workflowPath := filepath.Join(p11RepoRoot(), ".github/workflows/dev-release.yml")
	workflow := devReleaseWorkflow(t)
	workflowYAML := readWorkflowYAML(t, workflowPath)
	push := p11MappingValue(p11MappingValue(workflowYAML, "on"), "push")

	assertStringsEqual(t, "dev-release workflow events", workflow.Events, []string{"pull_request", "push"})
	assertStringsEqual(t, "dev-release push branches", p11StringList(p11MappingValue(push, "branches")), []string{"main"})

	if p11MappingValue(push, "tags") != nil {
		t.Fatal("dev-release workflow must not run from push tags")
	}

	filter := workflowDetectorScript(t, ".github/workflows/dev-release.yml", "changes", "dev-release")
	assertContains(t, filter, `if [ "$EVENT" != "pull_request" ]; then
  echo "release=true" >> "$GITHUB_OUTPUT"`)
	assertContains(t, filter, `-changed-files "$changed_files"`)
	assertContains(t, filter, `-github-output "$GITHUB_OUTPUT"`)
	assertNotContains(t, filter, "cmd/cipolicy")
	assertNotContains(t, filter, "dev-release-plan.json")
}

func TestDevReleaseRoutingFailsSafeWhenDetectorDoesNotSucceed(t *testing.T) {
	t.Parallel()

	releaseContext := p11WorkflowJob(t, devReleaseWorkflow(t), "release-context")

	p11AssertJobIf(t, "release-context", releaseContext, "!cancelled() && (needs.changes.result != 'success' || needs.changes.outputs.release == 'true')")
	assertContains(t, releaseContext.If, "needs.changes.result != 'success'")
	assertStringsEqual(t, "release-context needs", releaseContext.Needs, []string{"changes"})

	tests := map[string]struct {
		changesResult string
		releaseOutput string
		cancelled     bool
		want          bool
	}{
		"detector selected dev release":      {changesResult: "success", releaseOutput: "true", want: true},
		"detector skipped dev release":       {changesResult: "success", releaseOutput: "false", want: false},
		"detector failed after false output": {changesResult: "failure", releaseOutput: "false", want: true},
		"detector timed out before output":   {changesResult: "timed_out", want: true},
		"detector skipped before output":     {changesResult: "skipped", want: true},
		"cancelled workflow":                 {changesResult: "success", releaseOutput: "true", cancelled: true, want: false},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := devReleaseContextSelected(t, releaseContext.If, test.changesResult, test.releaseOutput, test.cancelled)
			if got != test.want {
				t.Fatalf("devReleaseContextSelected(result=%q, release=%q, cancelled=%t) = %t, want %t",
					test.changesResult, test.releaseOutput, test.cancelled, got, test.want)
			}
		})
	}
}

func TestDevReleasePublicationCredentialsStayPushOnly(t *testing.T) {
	t.Parallel()

	workflow := devReleaseWorkflow(t)
	buildDarwin := p11WorkflowJob(t, workflow, "build-darwin")
	attestLinux := p11WorkflowJob(t, workflow, "attest-linux")
	publishDev := p11WorkflowJob(t, workflow, "publish-dev")

	p11AssertJobIf(t, "attest-linux", attestLinux, "github.event_name == 'push'")
	p11AssertJobIf(t, "publish-dev", publishDev, "github.event_name == 'push'")
	assertStringsEqual(t, "publish-dev needs", publishDev.Needs, []string{"release-context", "assemble-dev", "attest-linux"})

	if publishDev.Permissions["contents"] != "write" || publishDev.Permissions["attestations"] != "read" || len(publishDev.Permissions) != 2 {
		t.Fatalf("publish-dev permissions = %#v, want contents:write and attestations:read only", publishDev.Permissions)
	}

	signing := p11WorkflowStep(t, buildDarwin, "Configure optional macOS service signing")
	unsigned := p11WorkflowStep(t, buildDarwin, "Configure unsigned pull-request packaging")

	if signing.If != "github.event_name == 'push'" {
		t.Fatalf("macOS signing step if = %q, want push-only", signing.If)
	}

	if unsigned.If != "github.event_name == 'pull_request'" {
		t.Fatalf("unsigned pull-request packaging step if = %q, want pull_request-only", unsigned.If)
	}

	if err := ValidateCredentialOperation(devReleasePublishOperation("trusted-publication")); err != nil {
		t.Fatalf("trusted dev-release publication credential rejected: %v", err)
	}

	tests := map[string]struct {
		trustTier string
		wantErr   string
	}{
		"fork pull request": {
			trustTier: "fork-untrusted",
			wantErr:   "fork pull requests may use only synthetic read tokens",
		},
		"same-repository pull request": {
			trustTier: "same-repository-agent",
			wantErr:   "same-repository agent branches cannot obtain maintainer credentials",
		},
		"trusted base without publish": {
			trustTier: "trusted-base",
			wantErr:   "dev-release-publish is not allowed for trust tier trusted-base",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := ValidateCredentialOperation(devReleasePublishOperation(test.trustTier))
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("dev-release publish credential error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestMigratedDetectorScriptsFailSafe(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		workflow string
		jobID    string
		mode     string
		output   string
	}{
		"ci macos detector": {
			workflow: ".github/workflows/ci.yml",
			jobID:    "changes",
			mode:     "ci",
			output:   "macos",
		},
		"sandbox macos detector": {
			workflow: ".github/workflows/sandbox.yml",
			jobID:    "changes",
			mode:     "sandbox",
			output:   "macos",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			script := workflowDetectorScript(t, test.workflow, test.jobID, test.mode)
			failSafeOutput := `echo "` + test.output + `=true" >> "$GITHUB_OUTPUT"`

			assertContains(t, script, `if ! files="$(gh api "repos/$REPO/pulls/$PR/files" --paginate --jq '.[].filename')"; then
  `+failSafeOutput)
			assertContains(t, script, `if ! classification="$(go run ./cmd/ciclassify -mode `+test.mode+` <<<"$files")"; then
  `+failSafeOutput)
		})
	}
}

func TestWorkflowClassifierRejectsUnsafeChangedFileLists(t *testing.T) {
	t.Parallel()

	tests := map[string][]string{
		"blank":              {""},
		"empty":              {},
		"leading whitespace": {" internal/pty/session.go"},
		"traversal":          {"internal/../go.mod"},
		"trailing traversal": {"internal/.."},
		"absolute":           {"/go.mod"},
	}

	for name, files := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if _, err := ClassifyWorkflowPaths(files); err == nil {
				t.Fatalf("ClassifyWorkflowPaths(%q) succeeded, want fail-safe error", files)
			}
		})
	}
}

func TestWorkflowModeOutputs(t *testing.T) {
	t.Parallel()

	classification := WorkflowClassifications{
		CIMacOS:                  true,
		CoverageGUI:              true,
		SandboxMacOS:             true,
		LibghosttyNative:         true,
		LibghosttyDependencyUnit: true,
		DevRelease:               true,
		StableRelease:            true,
		DocsPreviewTrigger:       true,
		DocsPreviewGlobal:        true,
		DocsPreviewBuild:         true,
	}

	tests := map[WorkflowClassifierMode][]string{
		WorkflowClassifierModeCI:          {"macos"},
		WorkflowClassifierModeCoverage:    {"gui"},
		WorkflowClassifierModeSandbox:     {"macos"},
		WorkflowClassifierModeLibghostty:  {"dependency-unit", "native"},
		WorkflowClassifierModeDevRelease:  {"release"},
		WorkflowClassifierModeStable:      {"release"},
		WorkflowClassifierModeDocsPreview: {"build", "global", "trigger"},
	}

	for mode, wantKeys := range tests {
		t.Run(string(mode), func(t *testing.T) {
			t.Parallel()

			outputs, err := WorkflowModeOutputs(mode, classification)
			if err != nil {
				t.Fatal(err)
			}

			var gotKeys []string
			for key := range outputs {
				gotKeys = append(gotKeys, key)
				if !outputs[key] {
					t.Fatalf("output %s for %s = false, want true", key, mode)
				}
			}

			slices.Sort(gotKeys)

			if !slices.Equal(gotKeys, wantKeys) {
				t.Fatalf("output keys for %s = %v, want %v", mode, gotKeys, wantKeys)
			}
		})
	}
}

func devReleaseWorkflow(t *testing.T) P11WorkflowSummary {
	t.Helper()

	workflow, err := ReadP11WorkflowSummary(filepath.Join(p11RepoRoot(), ".github/workflows/dev-release.yml"))
	if err != nil {
		t.Fatal(err)
	}

	return workflow
}

func devReleaseContextSelected(t *testing.T, condition, changesResult, releaseOutput string, cancelled bool) bool {
	t.Helper()

	switch p11NormalizeExpression(condition) {
	case "!cancelled() && (needs.changes.result != 'success' || needs.changes.outputs.release == 'true')":
		return !cancelled && (changesResult != "success" || releaseOutput == "true")
	default:
		t.Fatalf("unsupported dev-release condition %q", condition)
		return false
	}
}

func loadWorkflowClassifierFixtures(t *testing.T) workflowClassifierFixtureFile {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("testdata", "workflow_classifiers.json"))
	if err != nil {
		t.Fatal(err)
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	var fixtures workflowClassifierFixtureFile
	if err := decoder.Decode(&fixtures); err != nil {
		t.Fatal(err)
	}

	return fixtures
}

func workflowDetectorScript(t *testing.T, workflowPath, jobID, mode string) string {
	t.Helper()

	workflow, err := ReadP11WorkflowSummary(filepath.Join(p11RepoRoot(), workflowPath))
	if err != nil {
		t.Fatal(err)
	}

	job := p11WorkflowJob(t, workflow, jobID)

	modePattern := regexp.MustCompile(`(?:^|[[:space:]])-mode[[:space:]]+` + regexp.QuoteMeta(mode) + `(?:[[:space:]]|$)`)
	for _, step := range job.Steps {
		if strings.Contains(step.Run, "go run ./cmd/ciclassify") &&
			modePattern.MatchString(step.Run) {
			return step.Run
		}
	}

	t.Fatalf("%s job %s does not call ciclassify -mode %s", workflowPath, jobID, mode)

	return ""
}

type workflowLegacyMatchers struct {
	devRelease    *regexp.Regexp
	stableRelease *regexp.Regexp
}

func currentWorkflowLegacyMatchers(t *testing.T) workflowLegacyMatchers {
	t.Helper()

	repoRoot := p11RepoRoot()

	return workflowLegacyMatchers{
		devRelease:    legacyDevReleaseMatcher,
		stableRelease: releasePathMatcher(t, readPolicyFile(t, filepath.Join(repoRoot, ".github/workflows/goreleaser.yml"))),
	}
}

func legacyWorkflowClassification(files []string, matchers workflowLegacyMatchers) WorkflowClassifications {
	var result WorkflowClassifications

	for _, path := range files {
		sharedWorkflow := legacyCIWorkflowMatcher.MatchString(path)

		if sharedWorkflow || legacyCIMacOSMatcher.MatchString(path) {
			result.CIMacOS = true
		}

		if sharedWorkflow || legacyCoverageGUIMatcher.MatchString(path) {
			result.CoverageGUI = true
		}

		if sharedWorkflow || legacySandboxMacOSMatcher.MatchString(path) {
			result.SandboxMacOS = true
		}

		if sharedWorkflow || path == "libghostty-native.lock.json" {
			result.LibghosttyDependencyUnit = true
		}

		if sharedWorkflow || legacyLibghosttyNativeMatcher.MatchString(path) {
			result.LibghosttyNative = true
		}

		if matchers.devRelease.MatchString(path) {
			result.DevRelease = true
		}

		if matchers.stableRelease.MatchString(path) {
			result.StableRelease = true
		}

		if legacyDocsPreviewTriggerMatcher.MatchString(path) {
			result.DocsPreviewTrigger = true
			result.DocsPreviewBuild = true
		}

		if legacyDocsPreviewGlobalMatcher.MatchString(path) {
			result.DocsPreviewGlobal = true
			result.DocsPreviewBuild = true
		}
	}

	return result
}

var (
	legacyCIMacOSMatcher = regexp.MustCompile(`^gui/|^demo/|^internal/integration/|^internal/protocol/|^internal/capabilities/|^internal/daemon/|^internal/pty/|^internal/sandbox/|^Makefile$|^go\.mod$|^go\.sum$|\.swift$|^\.github/workflows/ci\.yml$`)

	legacyCoverageGUIMatcher = regexp.MustCompile(`^gui/`)

	legacySandboxMacOSMatcher = regexp.MustCompile(`^internal/sandbox/|^go\.mod$|^go\.sum$|^\.github/workflows/sandbox\.yml$`)

	legacyLibghosttyNativeMatcher = regexp.MustCompile(`^\.goreleaser-dev\.yaml$|^\.github/workflows/(dev-release|libghostty-native)\.yml$|^gui/shared/Package\.swift$|^gui/shared/Sources/CGhosttyVT/include/|^go\.(mod|sum)$|^internal/(cli|daemon|integration|libghosttydeps|pty|release)/|^libghostty-native\.(lock|spdx)\.json$|^scripts/(dev-release-version|libghostty-native)\.sh$|^THIRD_PARTY_NOTICES\.libghostty\.md$`)

	legacyDevReleaseMatcher = regexp.MustCompile(`^\.github/workflows/dev-release\.yml$|^\.goreleaser-dev\.yaml$|^THIRD_PARTY_NOTICES\.libghostty\.md$|^libghostty-native\.(lock|spdx)\.json$|^scripts/(dev-release-base-tag|dev-release-version|libghostty-native)\.sh$|^cmd/ciclassify/|^internal/(ciworkflow|daemonservice)/|^macos/(notifier|service)/`)

	legacyCIWorkflowMatcher = regexp.MustCompile(`^\.github/(actions|workflows)/|^cmd/ciclassify/|^internal/ciworkflow/`)

	legacyDocsPreviewTriggerMatcher = regexp.MustCompile(`^website/|^\.github/(ci-tool-versions\.env|workflows/(docs|docs-preview)\.yml)$`)

	legacyDocsPreviewGlobalMatcher = regexp.MustCompile(`^website/(\.ci/|archetypes/|assets/|config/|data/|hugo\.toml|go\.(mod|sum)|i18n/|layouts/|static/|themes/)`)
)
