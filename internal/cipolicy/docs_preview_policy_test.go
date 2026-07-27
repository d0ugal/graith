package cipolicy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestDocsPreviewWorkflowRoutesPreMergeHugoBuildInputs(t *testing.T) {
	workflowPath := filepath.Join(p11RepoRoot(), ".github", "workflows", "docs-preview.yml")

	assertStringsEqual(t, "pull_request paths", docsPreviewPullRequestPaths(t, workflowPath), []string{
		"website/**",
		".github/ci-tool-versions.env",
		".github/workflows/docs.yml",
		".github/workflows/docs-preview.yml",
	})

	workflow, err := ReadP11WorkflowSummary(workflowPath)
	if err != nil {
		t.Fatal(err)
	}

	preview := p11WorkflowJob(t, workflow, "preview")
	changed := p11WorkflowStep(t, preview, "Determine changed pages")

	for _, want := range []string{
		`git diff --name-only "$BASE" "$HEAD" -- website/ .github/ci-tool-versions.env .github/workflows/docs.yml .github/workflows/docs-preview.yml`,
		"detector_failed=1",
		`grep -qE '^website/(\.ci/|archetypes/|assets/|config/|data/|hugo\.toml|go\.(mod|sum)|i18n/|layouts/|static/|themes/)' <<<"$changed"`,
		`grep -qx '.github/ci-tool-versions.env' <<<"$changed"`,
		`grep -qE '^website/|^\.github/(ci-tool-versions\.env|workflows/(docs|docs-preview)\.yml)$' <<<"$changed"`,
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
