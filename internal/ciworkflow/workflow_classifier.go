package ciworkflow

import (
	"errors"
	"fmt"
	"strings"
)

const WorkflowClassifierVersion = "ci-path-classifier-v2"

type WorkflowClassifierMode string

const (
	WorkflowClassifierModeCI                      WorkflowClassifierMode = "ci"
	WorkflowClassifierModeCoverage                WorkflowClassifierMode = "coverage"
	WorkflowClassifierModeSandbox                 WorkflowClassifierMode = "sandbox"
	WorkflowClassifierModeLibghostty              WorkflowClassifierMode = "libghostty"
	WorkflowClassifierModeDevRelease              WorkflowClassifierMode = "dev-release"
	WorkflowClassifierModeStable                  WorkflowClassifierMode = "stable-release"
	WorkflowClassifierModeDocsPreview             WorkflowClassifierMode = "docs-preview"
	WorkflowClassifierModeSessionNavigatorPreview WorkflowClassifierMode = "session-navigator-preview"
)

type WorkflowClassifications struct {
	CIMacOS                  bool `json:"ci_macos"`
	CoverageGUI              bool `json:"coverage_gui"`
	SandboxMacOS             bool `json:"sandbox_macos"`
	LibghosttyNative         bool `json:"libghostty_native"`
	LibghosttyDependencyUnit bool `json:"libghostty_dependency_unit"`
	DevRelease               bool `json:"dev_release"`
	StableRelease            bool `json:"stable_release"`
	DocsPreviewTrigger       bool `json:"docs_preview_trigger"`
	DocsPreviewGlobal        bool `json:"docs_preview_global"`
	DocsPreviewBuild         bool `json:"docs_preview_build"`
	SessionNavigatorPreview  bool `json:"session_navigator_preview"`
}

type workflowPathRule struct {
	Paths    []string
	Prefixes []string
	Suffixes []string
}

var (
	ciMacOSRules = workflowPathRule{
		Paths: []string{
			".github/workflows/ci.yml",
			"Makefile",
			"go.mod",
			"go.sum",
		},
		Prefixes: []string{
			"demo/",
			"gui/",
			"internal/capabilities/",
			"internal/daemon/",
			"internal/integration/",
			"internal/protocol/",
			"internal/pty/",
			"internal/sandbox/",
		},
		Suffixes: []string{".swift"},
	}

	coverageGUIRules = workflowPathRule{
		Prefixes: []string{"gui/"},
	}

	sandboxMacOSRules = workflowPathRule{
		Paths: []string{
			".github/workflows/sandbox.yml",
			"go.mod",
			"go.sum",
		},
		Prefixes: []string{"internal/sandbox/"},
	}

	libghosttyDependencyUnitRules = workflowPathRule{
		Paths: []string{"libghostty-native.lock.json"},
	}

	ciWorkflowRules = workflowPathRule{
		Prefixes: []string{
			".github/actions/",
			".github/workflows/",
			"cmd/ciclassify/",
			"internal/ciworkflow/",
		},
	}

	devReleaseRules = workflowPathRule{
		Paths: []string{
			".github/ci-tool-versions.env",
			".github/workflows/dev-release.yml",
			".goreleaser-dev.yaml",
			"THIRD_PARTY_NOTICES.libghostty.md",
			"libghostty-native.lock.json",
			"libghostty-native.spdx.json",
			"scripts/dev-release-base-tag.sh",
			"scripts/dev-release-version.sh",
			"scripts/libghostty-native.sh",
		},
		Prefixes: []string{
			"cmd/ciclassify/",
			"internal/ciworkflow/",
			"internal/daemonservice/",
			"macos/notifier/",
			"macos/service/",
		},
	}

	libghosttyNativeRules = workflowPathRule{
		Paths: []string{
			".github/workflows/dev-release.yml",
			".github/workflows/libghostty-native.yml",
			".goreleaser-dev.yaml",
			"THIRD_PARTY_NOTICES.libghostty.md",
			"go.mod",
			"go.sum",
			"gui/shared/Package.swift",
			"libghostty-native.lock.json",
			"libghostty-native.spdx.json",
			"scripts/dev-release-version.sh",
			"scripts/libghostty-native.sh",
		},
		Prefixes: []string{
			"gui/shared/Sources/CGhosttyVT/include/",
			"internal/cli/",
			"internal/daemon/",
			"internal/integration/",
			"internal/libghosttydeps/",
			"internal/pty/",
			"internal/release/",
		},
	}

	stableReleaseRules = workflowPathRule{
		Paths: []string{
			".github/ci-tool-versions.env",
			".github/workflows/goreleaser.yml",
			".github/workflows/release-please.yml",
			".goreleaser-linux.yaml",
			".goreleaser.yaml",
			".release-please-config.json",
			".release-please-manifest.json",
			"CHANGELOG.md",
			"THIRD_PARTY_NOTICES.libghostty.md",
			"internal/integration/libghostty_daemon_test.go",
			"libghostty-native.lock.json",
			"libghostty-native.spdx.json",
			"scripts/libghostty-native.sh",
			"scripts/publish-linux-repositories.sh",
			"scripts/publish-push.sh",
			"scripts/render-stable-aur.sh",
			"scripts/render-stable-homebrew.sh",
			"scripts/rpm-preset-keygrips.sh",
		},
		Prefixes: []string{
			"internal/daemonservice/",
			"internal/release/goreleaser",
			"internal/release/stable",
			"macos/notifier/",
			"macos/service/",
		},
	}

	docsPreviewTriggerRules = workflowPathRule{
		Paths: []string{
			".github/ci-tool-versions.env",
			".github/workflows/docs-preview.yml",
			".github/workflows/docs.yml",
			"go.mod",
			"go.sum",
			"Makefile",
			"scripts/install-dart-sass.sh",
			"scripts/install-hugo.sh",
			"scripts/render-session-navigator-doc-screenshots.sh",
		},
		Prefixes: []string{
			"cmd/ciclassify/",
			"cmd/docsdiff/",
			"cmd/docspreview/",
			"internal/ciworkflow/",
			"internal/docspreview/",
			"website/",
		},
	}

	docsPreviewGlobalRules = workflowPathRule{
		Paths: []string{
			"website/go.mod",
			"website/go.sum",
			"website/hugo.toml",
			"website/package-lock.json",
			"website/package.json",
		},
		Prefixes: []string{
			"website/.ci/",
			"website/archetypes/",
			"website/assets/",
			"website/config/",
			"website/data/",
			"website/i18n/",
			"website/layouts/",
			"website/static/",
			"website/themes/",
		},
	}

	sessionNavigatorPreviewRules = workflowPathRule{
		Paths: []string{
			".github/workflows/session-navigator-preview.yml",
			"Makefile",
			"go.mod",
			"go.sum",
			"internal/protocol/messages.go",
			"scripts/session-navigator-terminal-screenshot.sh",
		},
		Prefixes: []string{
			"cmd/ciclassify/",
			"cmd/docsdiff/",
			"cmd/docspreview/",
			"cmd/sessionnavshots/",
			"internal/ciworkflow/",
			"internal/client/",
			"internal/config/",
			"internal/docspreview/",
			"internal/sessionlabel/",
		},
	}
)

func ClassifyWorkflowPaths(changedFiles []string) (WorkflowClassifications, error) {
	files, err := validatedWorkflowChangedFiles(changedFiles)
	if err != nil {
		return WorkflowClassifications{}, err
	}

	var result WorkflowClassifications

	for _, path := range files {
		sharedWorkflow := isCIWorkflowPath(path)

		if sharedWorkflow || workflowRuleMatches(ciMacOSRules, path) {
			result.CIMacOS = true
		}

		if sharedWorkflow || workflowRuleMatches(coverageGUIRules, path) {
			result.CoverageGUI = true
		}

		if sharedWorkflow || workflowRuleMatches(sandboxMacOSRules, path) {
			result.SandboxMacOS = true
		}

		if sharedWorkflow || workflowRuleMatches(libghosttyNativeRules, path) {
			result.LibghosttyNative = true
		}

		if workflowRuleMatches(libghosttyDependencyUnitRules, path) {
			result.LibghosttyDependencyUnit = true
		}

		if workflowRuleMatches(devReleaseRules, path) {
			result.DevRelease = true
		}

		if workflowRuleMatches(stableReleaseRules, path) {
			result.StableRelease = true
		}

		if workflowRuleMatches(docsPreviewTriggerRules, path) {
			result.DocsPreviewTrigger = true
			result.DocsPreviewBuild = true
		}

		if workflowRuleMatches(docsPreviewGlobalRules, path) {
			result.DocsPreviewGlobal = true
			result.DocsPreviewBuild = true
		}

		if workflowRuleMatches(sessionNavigatorPreviewRules, path) {
			result.SessionNavigatorPreview = true
		}
	}

	return result, nil
}

func WorkflowModeOutputs(mode WorkflowClassifierMode, result WorkflowClassifications) (map[string]bool, error) {
	switch mode {
	case WorkflowClassifierModeCI:
		return map[string]bool{"macos": result.CIMacOS}, nil
	case WorkflowClassifierModeCoverage:
		return map[string]bool{"gui": result.CoverageGUI}, nil
	case WorkflowClassifierModeSandbox:
		return map[string]bool{"macos": result.SandboxMacOS}, nil
	case WorkflowClassifierModeLibghostty:
		return map[string]bool{
			"dependency-unit": result.LibghosttyDependencyUnit,
			"native":          result.LibghosttyNative,
		}, nil
	case WorkflowClassifierModeDevRelease:
		return map[string]bool{"release": result.DevRelease}, nil
	case WorkflowClassifierModeStable:
		return map[string]bool{"release": result.StableRelease}, nil
	case WorkflowClassifierModeDocsPreview:
		return map[string]bool{
			"build":   result.DocsPreviewBuild,
			"global":  result.DocsPreviewGlobal,
			"trigger": result.DocsPreviewTrigger,
		}, nil
	case WorkflowClassifierModeSessionNavigatorPreview:
		return map[string]bool{"trigger": result.SessionNavigatorPreview}, nil
	default:
		return nil, fmt.Errorf("unknown workflow classifier mode %q", mode)
	}
}

func validatedWorkflowChangedFiles(changedFiles []string) ([]string, error) {
	files := canonicalChangedFiles(changedFiles)
	if len(files) == 0 {
		return nil, errors.New("changed-file list is empty")
	}

	for _, path := range files {
		if path == "" {
			return nil, errors.New("changed-file list contains a blank row")
		}

		if invalidChangedPath(path) {
			return nil, fmt.Errorf("changed-file list contains invalid path %q", path)
		}
	}

	return files, nil
}

func workflowRuleMatches(rule workflowPathRule, path string) bool {
	for _, exact := range rule.Paths {
		if path == exact {
			return true
		}
	}

	for _, prefix := range rule.Prefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}

	for _, suffix := range rule.Suffixes {
		if strings.HasSuffix(path, suffix) {
			return true
		}
	}

	return false
}
