package ciworkflow

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestDocsPreviewWorkflowRoutesPreMergeHugoBuildInputs(t *testing.T) {
	workflowPath := filepath.Join(p11RepoRoot(), ".github", "workflows", "docs-preview.yml")

	assertStringsEqual(t, "pull_request paths", docsPreviewPullRequestPaths(t, workflowPath), []string{
		"website/**",
		"demo/graith.gif",
		"cmd/ciclassify/**",
		"cmd/docsdiff/**",
		"cmd/docspreview/**",
		"internal/ciworkflow/**",
		"internal/docspreview/**",
		"Makefile",
		"go.mod",
		"go.sum",
		".github/ci-tool-versions.env",
		".github/workflows/docs.yml",
		".github/workflows/docs-preview.yml",
		"scripts/install-dart-sass.sh",
		"scripts/install-hugo.sh",
		"scripts/render-session-navigator-doc-screenshots.sh",
	})

	workflow, err := ReadP11WorkflowSummary(workflowPath)
	if err != nil {
		t.Fatal(err)
	}

	changesJob := p11WorkflowJob(t, workflow, "changes")
	p11AssertJobIf(t, "changes", changesJob, `github.event_name == 'pull_request' && github.event.action != 'closed'`)

	if want := map[string]string{"contents": "read", "pull-requests": "read"}; !reflect.DeepEqual(changesJob.Permissions, want) {
		t.Fatalf("changes permissions = %#v, want %#v", changesJob.Permissions, want)
	}

	checkout, ok := docsPreviewCheckoutStep(changesJob)
	if !ok {
		t.Fatalf("changes job has no actions/checkout step: %#v", changesJob.Steps)
	}

	if checkout.With["ref"] != "${{ github.event.pull_request.base.sha }}" ||
		checkout.With["persist-credentials"] != "false" {
		t.Fatalf("changes checkout with = %#v", checkout.With)
	}

	detector := workflowDetectorScript(t, ".github/workflows/docs-preview.yml", "changes", "docs-preview")
	for _, want := range []string{
		`gh api "repos/$REPO/pulls/$PR/files" --paginate --jq '.[].filename'`,
		`go run ./cmd/ciclassify -mode docs-preview`,
		`echo "build=true"`,
		`echo "global=false"`,
		`echo "trigger=true"`,
		`} >> "$GITHUB_OUTPUT"`,
		"Could not list PR files; running the Hugo build to be safe.",
		"Shared classifier failed; running the Hugo build to be safe.",
	} {
		if !strings.Contains(detector, want) {
			t.Fatalf("docs-preview detector does not contain %q:\n%s", want, detector)
		}
	}

	preview := p11WorkflowJob(t, workflow, "preview")
	assertStringsEqual(t, "preview needs", preview.Needs, []string{"changes"})
	p11AssertJobIf(t, "preview", preview, `!cancelled() &&
github.event_name == 'pull_request' &&
github.event.action != 'closed' &&
(needs.changes.result != 'success' || needs.changes.outputs.trigger != 'false')`)

	changed := p11WorkflowStep(t, preview, "Determine changed pages")
	if changed.Env["CLASSIFIER_BUILD"] != "${{ needs.changes.outputs.build }}" ||
		changed.Env["CLASSIFIER_GLOBAL"] != "${{ needs.changes.outputs.global }}" {
		t.Fatalf("Determine changed pages classifier env = %#v", changed.Env)
	}

	for _, want := range []string{
		`git diff --name-only "$BASE" "$HEAD" -- \`,
		"website/ \\\n  demo/graith.gif \\",
		"cmd/ciclassify/",
		"cmd/docsdiff/",
		"cmd/docspreview/",
		"internal/ciworkflow/",
		"internal/docspreview/",
		"Makefile",
		"go.mod",
		"go.sum",
		"scripts/install-dart-sass.sh",
		"scripts/install-hugo.sh",
		"scripts/render-session-navigator-doc-screenshots.sh",
		"detector_failed=1",
		`classifier_build="${CLASSIFIER_BUILD:-true}"`,
		`classifier_global="${CLASSIFIER_GLOBAL:-false}"`,
		`[ "$classifier_global" = "true" ]`,
		`grep -qE '^website/(\.ci/|archetypes/|assets/|config/|data/|hugo\.toml|go\.(mod|sum)|package(-lock)?\.json|i18n/|layouts/|static/|themes/)' <<<"$changed"`,
		`grep -qx '.github/ci-tool-versions.env' <<<"$changed"`,
		`[ "$classifier_build" != "false" ]`,
		`grep -qE '^website/|^demo/graith\.gif$|^cmd/(ciclassify|docsdiff|docspreview)/|^internal/(ciworkflow|docspreview)/|^Makefile$|^go\.(mod|sum)$|^\.github/(ci-tool-versions\.env|workflows/(docs|docs-preview)\.yml)$|^scripts/(install-(dart-sass|hugo)|render-session-navigator-doc-screenshots)\.sh$' <<<"$changed"`,
		`grep -qx 'demo/graith.gif' <<<"$changed"`,
		`emit "website/content/docs/_index.md" false`,
		`echo "build=$build"`,
		"find website/content/docs -name '*.md'",
	} {
		if !strings.Contains(changed.Run, want) {
			t.Fatalf("Determine changed pages step does not contain %q:\n%s", want, changed.Run)
		}
	}

	build := p11WorkflowStep(t, preview, "Build head + base sites")
	if got, want := build.If, "steps.changed.outputs.build == '1'"; got != want {
		t.Fatalf("Build head + base sites if = %q, want %q", got, want)
	}

	if !strings.Contains(build.Run, "make package-graph-check") {
		t.Fatalf("Build head + base sites step does not run package-graph-check:\n%s", build.Run)
	}

	if !strings.Contains(build.Run, "make docs-faro-smoke") {
		t.Fatalf("Build head + base sites step does not smoke-build Faro:\n%s", build.Run)
	}
}

func TestDocsWorkflowSmokeBuildsFaroBundle(t *testing.T) {
	workflowPath := filepath.Join(p11RepoRoot(), ".github", "workflows", "docs.yml")

	workflow, err := ReadP11WorkflowSummary(workflowPath)
	if err != nil {
		t.Fatal(err)
	}

	build := p11WorkflowJob(t, workflow, "build")
	smokeIndex := p11WorkflowStepIndex(t, build, "Smoke-build Faro bundle")

	siteIndex := p11WorkflowStepIndex(t, build, "Build site")
	if smokeIndex >= siteIndex {
		t.Fatalf("Smoke-build Faro bundle step index = %d, want before Build site index %d", smokeIndex, siteIndex)
	}

	smoke := p11WorkflowStep(t, build, "Smoke-build Faro bundle")
	if !strings.Contains(smoke.Run, "make docs-faro-smoke") {
		t.Fatalf("Smoke-build Faro bundle step does not run make docs-faro-smoke:\n%s", smoke.Run)
	}
}

func TestDocsWorkflowRoutesDeployInputs(t *testing.T) {
	workflowPath := filepath.Join(p11RepoRoot(), ".github", "workflows", "docs.yml")

	workflowYAML := readWorkflowYAML(t, workflowPath)
	push := p11MappingValue(p11MappingValue(workflowYAML, "on"), "push")
	assertStringsEqual(t, "docs push paths", p11StringList(p11MappingValue(push, "paths")), []string{
		"website/**",
		"demo/graith.gif",
		"cmd/**/*.go",
		"internal/**/*.go",
		"go.mod",
		"go.sum",
		".github/ci-tool-versions.env",
		".github/workflows/docs.yml",
		"scripts/install-dart-sass.sh",
		"scripts/install-hugo.sh",
	})
}

func docsPreviewPullRequestPaths(t *testing.T, path string) []string {
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

	pullRequest, ok := raw.On["pull_request"]
	if !ok {
		t.Fatalf("workflow %s has no pull_request trigger", path)
	}

	if pullRequest.Kind != yaml.MappingNode {
		t.Fatalf("workflow %s pull_request trigger kind = %v, want mapping", path, pullRequest.Kind)
	}

	for index := 0; index < len(pullRequest.Content); index += 2 {
		if pullRequest.Content[index].Value == "paths" {
			return p11StringList(pullRequest.Content[index+1])
		}
	}

	t.Fatalf("workflow %s pull_request trigger has no paths filter", path)

	return nil
}

func docsPreviewCheckoutStep(job P11WorkflowJob) (P11WorkflowStep, bool) {
	for _, step := range job.Steps {
		if strings.HasPrefix(step.Uses, "actions/checkout@") {
			return step, true
		}
	}

	return P11WorkflowStep{}, false
}
