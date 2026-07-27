package cipolicy

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
		"ci policy command":     "cmd/cipolicy/main.go",
		"ci policy workflow":    ".github/workflows/coverage.yml",
		"shared classifier":     "cmd/ciclassify/main.go",
		"shared policy package": "internal/cipolicy/plan.go",
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

	stableMatcher := currentWorkflowLegacyMatchers(t).stableRelease
	tests := map[string]string{
		"ci policy command":         "cmd/cipolicy/main.go",
		"ci policy workflow":        ".github/workflows/coverage.yml",
		"dev release workflow":      ".github/workflows/dev-release.yml",
		"stable release workflow":   ".github/workflows/goreleaser.yml",
		"shared classifier command": "cmd/ciclassify/main.go",
		"shared policy package":     "internal/cipolicy/plan.go",
	}

	for name, path := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := ClassifyWorkflowPaths([]string{path})
			if err != nil {
				t.Fatal(err)
			}

			if want := sharedClassifierSelectsDevRelease(path); got.DevRelease != want {
				t.Fatalf("DevRelease for %s = %t, want current dev-release classifier %t", path, got.DevRelease, want)
			}

			if want := stableMatcher.MatchString(path); got.StableRelease != want {
				t.Fatalf("StableRelease for %s = %t, want current stable-release classifier %t", path, got.StableRelease, want)
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
	for _, step := range job.Steps {
		if strings.Contains(step.Run, "go run ./cmd/ciclassify -mode "+mode) {
			return step.Run
		}
	}

	t.Fatalf("%s job %s does not call ciclassify -mode %s", workflowPath, jobID, mode)

	return ""
}

type workflowLegacyMatchers struct {
	stableRelease *regexp.Regexp
}

func currentWorkflowLegacyMatchers(t *testing.T) workflowLegacyMatchers {
	t.Helper()

	repoRoot := p11RepoRoot()

	return workflowLegacyMatchers{
		stableRelease: releasePathMatcher(t, readPolicyFile(t, filepath.Join(repoRoot, ".github/workflows/goreleaser.yml"))),
	}
}

func legacyWorkflowClassification(files []string, matchers workflowLegacyMatchers) WorkflowClassifications {
	var result WorkflowClassifications

	for _, path := range files {
		if legacyCIMacOSMatcher.MatchString(path) {
			result.CIMacOS = true
		}

		if legacyCoverageGUIMatcher.MatchString(path) {
			result.CoverageGUI = true
		}

		if legacySandboxMacOSMatcher.MatchString(path) {
			result.SandboxMacOS = true
		}

		if path == "libghostty-native.lock.json" {
			result.LibghosttyDependencyUnit = true
		}

		if legacyLibghosttyNativeMatcher.MatchString(path) {
			result.LibghosttyNative = true
		}

		if sharedClassifierSelectsDevRelease(path) {
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

	legacyDocsPreviewTriggerMatcher = regexp.MustCompile(`^website/|^\.github/(ci-tool-versions\.env|workflows/(docs|docs-preview)\.yml)$`)

	legacyDocsPreviewGlobalMatcher = regexp.MustCompile(`^website/(\.ci/|archetypes/|assets/|config/|data/|hugo\.toml|go\.(mod|sum)|i18n/|layouts/|static/|themes/)`)
)
