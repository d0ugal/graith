package ciworkflow

import "testing"

func FuzzClassifyWorkflowPaths(f *testing.F) {
	f.Add("internal/ciworkflow/workflow_classifier.go", "cmd/ciclassify/main.go", "go.mod")
	f.Add(".github/workflows/ci.yml", "website/content/docs/commands/_index.md", "gui/shared/Package.swift")
	f.Add("internal/sessionlabel/label.go", "scripts/session-navigator-terminal-screenshot.sh", "Makefile")
	f.Add(" libghostty-native.lock.json", "go.sum", "cmd/graith/main.go")
	f.Add("../go.mod", "/tmp/croft", "")

	f.Fuzz(func(t *testing.T, first, second, third string) {
		if len(first)+len(second)+len(third) > 4096 {
			t.Skip()
		}

		got, err := ClassifyWorkflowPaths([]string{first, second, third})
		if err != nil {
			return
		}

		reordered, err := ClassifyWorkflowPaths([]string{third, second, first, first, second})
		if err != nil {
			t.Fatalf("ClassifyWorkflowPaths rejected reordered duplicate valid input: %v", err)
		}

		if reordered != got {
			t.Fatalf("ClassifyWorkflowPaths changed after reorder/dedupe: %#v then %#v", got, reordered)
		}

		if (got.DocsPreviewGlobal || got.DocsPreviewTrigger) && !got.DocsPreviewBuild {
			t.Fatalf("docs preview classification = %#v, want build when global or trigger is true", got)
		}

		for _, path := range []string{first, second, third} {
			if !isCIWorkflowPath(path) {
				continue
			}

			if !got.CIMacOS || !got.CoverageGUI || !got.SandboxMacOS || !got.LibghosttyNative {
				t.Fatalf("shared workflow path %q classified as %#v, want all shared workflow flags", path, got)
			}
		}

		expanded, err := ClassifyWorkflowPaths([]string{first, second, third, "go.mod"})
		if err != nil {
			t.Fatalf("ClassifyWorkflowPaths rejected valid superset: %v", err)
		}

		if !classificationsInclude(expanded, got) {
			t.Fatalf("ClassifyWorkflowPaths was not monotonic: subset=%#v superset=%#v", got, expanded)
		}
	})
}

func classificationsInclude(got, want WorkflowClassifications) bool {
	return (!want.CIMacOS || got.CIMacOS) &&
		(!want.CoverageGUI || got.CoverageGUI) &&
		(!want.SandboxMacOS || got.SandboxMacOS) &&
		(!want.LibghosttyNative || got.LibghosttyNative) &&
		(!want.LibghosttyDependencyUnit || got.LibghosttyDependencyUnit) &&
		(!want.DevRelease || got.DevRelease) &&
		(!want.StableRelease || got.StableRelease) &&
		(!want.DocsPreviewTrigger || got.DocsPreviewTrigger) &&
		(!want.DocsPreviewGlobal || got.DocsPreviewGlobal) &&
		(!want.DocsPreviewBuild || got.DocsPreviewBuild) &&
		(!want.SessionNavigatorPreview || got.SessionNavigatorPreview)
}
