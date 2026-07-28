package ciworkflow

import (
	"fmt"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

var workflowMatrixReferencePattern = regexp.MustCompile(`matrix(?:\.([A-Za-z_][A-Za-z0-9_-]*)|\[['"]([^'"]+)['"]\])`)

// Keep the repository policy at GitHub's six-hour default cap for every Linux
// job the workflow scanner can classify. Literal timeout values make the
// effective bound auditable instead of trusting expressions or YAML indirection.
const workflowJobTimeoutLimitMinutes = 360

func TestLinuxWorkflowJobsHaveTimeouts(t *testing.T) {
	repoRoot := p11RepoRoot()

	paths := workflowPolicyFiles(t, repoRoot)

	var (
		invalid    []string
		missing    []string
		unresolved []string
	)

	for _, path := range paths {
		workflow := readWorkflowYAML(t, path)

		jobs := p11MappingValue(workflow, "jobs")
		if jobs == nil && p11MappingValue(workflow, "on") == nil {
			continue
		}

		if jobs == nil || jobs.Kind != yaml.MappingNode {
			t.Fatalf("%s has no jobs mapping", path)
		}

		for index := 0; index < len(jobs.Content); index += 2 {
			id := jobs.Content[index].Value

			job := jobs.Content[index+1]
			if job.Kind != yaml.MappingNode {
				t.Fatalf("%s/%s job is not a mapping", filepath.Base(path), id)
			}

			linux, unresolvedRefs := workflowJobCanRunOnLinux(job)
			for _, ref := range unresolvedRefs {
				unresolved = append(unresolved, fmt.Sprintf("%s/%s references unresolved %s", filepath.Base(path), id, ref))
			}

			if !linux {
				continue
			}

			timeout := p11MappingValue(job, "timeout-minutes")
			if timeout == nil {
				missing = append(missing, fmt.Sprintf("%s/%s", filepath.Base(path), id))

				continue
			}

			if !positiveTimeout(timeout) {
				invalid = append(invalid, fmt.Sprintf("%s/%s=%s", filepath.Base(path), id, timeoutValueDescription(timeout)))
			}
		}
	}

	if len(unresolved) != 0 || len(missing) != 0 || len(invalid) != 0 {
		var failures []string

		if len(unresolved) != 0 {
			failures = append(failures, "unresolved runs-on references: "+strings.Join(sortedUniqueStrings(unresolved), ", "))
		}

		if len(missing) != 0 {
			failures = append(failures, "Linux workflow jobs missing positive literal timeout-minutes: "+strings.Join(sortedUniqueStrings(missing), ", "))
		}

		if len(invalid) != 0 {
			failures = append(failures, fmt.Sprintf("Linux workflow jobs with non-literal or out-of-range timeout-minutes (1-%d): %s", workflowJobTimeoutLimitMinutes, strings.Join(sortedUniqueStrings(invalid), ", ")))
		}

		t.Fatalf("CI-DN-06 Linux workflow timeout policy failed: %s; fix by adding job-level `timeout-minutes:` with a literal integer", strings.Join(failures, "; "))
	}
}

func TestWorkflowJobCanRunOnLinuxClassifiesRunnerForms(t *testing.T) {
	tests := map[string]struct {
		job            string
		wantLinux      bool
		wantUnresolved []string
	}{
		"braw literal linux": {
			job:       "runs-on: ubuntu-latest\n",
			wantLinux: true,
		},
		"canny literal macos": {
			job: "runs-on: macos-latest\n",
		},
		"smirr custom macos label set": {
			job:            "runs-on: [self-hosted, macos-14]\n",
			wantUnresolved: []string{"runs-on"},
		},
		"dreich custom literal": {
			job:            "runs-on: [self-hosted, X64]\n",
			wantUnresolved: []string{"runs-on"},
		},
		"blether non-matrix expression": {
			job:            "runs-on: ${{ inputs.runner }}\n",
			wantUnresolved: []string{"runs-on"},
		},
		"croft top-level matrix linux": {
			job: `runs-on: ${{ matrix.os }}
strategy:
  matrix:
    os: [ubuntu-latest, macos-latest]
`,
			wantLinux: true,
		},
		"bothy include matrix linux": {
			job: `runs-on: ${{ matrix.runner }}
strategy:
  matrix:
    include:
      - runner: ubuntu-latest
`,
			wantLinux: true,
		},
		"bairn top-level matrix nonlinux": {
			job: `runs-on: ${{ matrix.os }}
strategy:
  matrix:
    os: [macos-latest, windows-latest]
`,
		},
		"thrawn top-level matrix custom": {
			job: `runs-on: ${{ matrix.runner }}
strategy:
  matrix:
    runner: [self-hosted]
`,
			wantUnresolved: []string{"matrix.runner"},
		},
		"laverock mixed label and matrix nonlinux": {
			job: `runs-on: [self-hosted, "${{ matrix.os }}"]
strategy:
  matrix:
    os: [macos-latest]
`,
			wantUnresolved: []string{"runs-on"},
		},
		"strath reusable workflow": {
			job: "uses: ./.github/workflows/reusable.yml\n",
		},
		"haar missing runs-on": {
			job: `steps:
  - run: echo braw
`,
			wantUnresolved: []string{"runs-on"},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			linux, unresolved := workflowJobCanRunOnLinux(workflowJobYAML(t, test.job))

			if linux != test.wantLinux {
				t.Errorf("linux = %v, want %v", linux, test.wantLinux)
			}

			if !reflect.DeepEqual(unresolved, test.wantUnresolved) {
				t.Errorf("unresolved = %#v, want %#v", unresolved, test.wantUnresolved)
			}
		})
	}
}

func TestPositiveTimeout(t *testing.T) {
	tests := map[string]struct {
		job  string
		want bool
	}{
		"braw nil": {
			job: "runs-on: ubuntu-latest\n",
		},
		"canny sequence": {
			job: "timeout-minutes: [15]\n",
		},
		"smirr alias": {
			job: `default-timeout: &default-timeout 15
timeout-minutes: *default-timeout
`,
		},
		"gloaming merged": {
			job: `defaults: &defaults
  timeout-minutes: 15
<<: *defaults
`,
		},
		"dreich expression": {
			job: "timeout-minutes: ${{ inputs.timeout }}\n",
		},
		"blether zero": {
			job: "timeout-minutes: 0\n",
		},
		"croft negative": {
			job: "timeout-minutes: -1\n",
		},
		"bothy above maximum": {
			job: "timeout-minutes: 361\n",
		},
		"bairn literal": {
			job:  "timeout-minutes: 30\n",
			want: true,
		},
		"thrawn quoted literal": {
			job: "timeout-minutes: \"45\"\n",
		},
		"strath maximum": {
			job:  "timeout-minutes: 360\n",
			want: true,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got := positiveTimeout(p11MappingValue(workflowJobYAML(t, test.job), "timeout-minutes"))
			if got != test.want {
				t.Errorf("positiveTimeout() = %v, want %v", got, test.want)
			}
		})
	}
}

func workflowPolicyFiles(t *testing.T, repoRoot string) []string {
	t.Helper()

	var paths []string

	for _, extension := range []string{"*.yml", "*.yaml"} {
		matches, err := filepath.Glob(filepath.Join(repoRoot, ".github", "workflows", extension))
		if err != nil {
			t.Fatal(err)
		}

		paths = append(paths, matches...)
	}

	sort.Strings(paths)

	return paths
}

func workflowJobYAML(t *testing.T, text string) *yaml.Node {
	t.Helper()

	var root yaml.Node
	if err := yaml.Unmarshal([]byte(text), &root); err != nil {
		t.Fatalf("decode workflow job fixture: %v", err)
	}

	if len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
		t.Fatalf("workflow job fixture is not a mapping")
	}

	return root.Content[0]
}

func readWorkflowYAML(t *testing.T, path string) *yaml.Node {
	t.Helper()

	var root yaml.Node
	if err := yaml.Unmarshal([]byte(readPolicyFile(t, path)), &root); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}

	if len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
		t.Fatalf("%s is not a workflow mapping", path)
	}

	return root.Content[0]
}

func workflowJobCanRunOnLinux(job *yaml.Node) (bool, []string) {
	runsOn := p11MappingValue(job, "runs-on")
	if runsOn == nil {
		if p11MappingValue(job, "uses") != nil {
			// Local reusable workflow definitions are scanned as their own files.
			// Calls into remote reusable workflows are outside this repo-owned
			// Linux job policy because their implementation is not in this tree.
			return false, nil
		}

		return false, []string{"runs-on"}
	}

	if runsOn.Kind == yaml.AliasNode {
		if runsOn.Alias == nil {
			return false, []string{"runs-on"}
		}

		runsOn = runsOn.Alias
	}

	if runsOn.Kind != yaml.ScalarNode && runsOn.Kind != yaml.SequenceNode {
		return false, []string{"runs-on"}
	}

	if runsOn.Kind == yaml.SequenceNode && len(runsOn.Content) == 0 {
		return false, []string{"runs-on"}
	}

	linux, resolved := runnerNodeCanRunOnLinux(runsOn)
	if linux {
		return true, nil
	}

	matrixRefs := matrixRunnerReferences(runsOn)
	if len(matrixRefs) == 0 {
		if resolved {
			return false, nil
		}

		return false, []string{"runs-on"}
	}

	strategy := p11MappingValue(job, "strategy")
	matrix := p11MappingValue(strategy, "matrix")

	var unresolved []string
	if runnerNodeHasUnresolvedNonMatrixLabel(runsOn) {
		unresolved = append(unresolved, "runs-on")
	}

	for _, ref := range matrixRefs {
		linux, resolved := matrixReferenceCanRunOnLinux(matrix, ref)
		if !resolved {
			unresolved = append(unresolved, "matrix."+ref)

			continue
		}

		if linux {
			return true, nil
		}
	}

	return false, unresolved
}

func matrixRunnerReferences(node *yaml.Node) []string {
	seen := map[string]bool{}

	var refs []string

	for _, value := range p11ScalarValues(node) {
		for _, match := range workflowMatrixReferencePattern.FindAllStringSubmatch(value, -1) {
			ref := match[1]
			if ref == "" {
				ref = match[2]
			}

			if ref == "" || seen[ref] {
				continue
			}

			seen[ref] = true
			refs = append(refs, ref)
		}
	}

	sort.Strings(refs)

	return refs
}

func timeoutValueDescription(node *yaml.Node) string {
	if node == nil {
		return "<missing>"
	}

	if node.Kind == yaml.ScalarNode {
		return strconv.Quote(node.Value)
	}

	switch node.Kind {
	case yaml.SequenceNode:
		return "<sequence>"
	case yaml.MappingNode:
		return "<mapping>"
	case yaml.AliasNode:
		return "<alias>"
	default:
		return fmt.Sprintf("<yaml-kind-%d>", node.Kind)
	}
}

func sortedUniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}

	sorted := append([]string(nil), values...)
	sort.Strings(sorted)

	unique := sorted[:0]
	for _, value := range sorted {
		if len(unique) == 0 || value != unique[len(unique)-1] {
			unique = append(unique, value)
		}
	}

	return unique
}

func runnerNodeHasUnresolvedNonMatrixLabel(node *yaml.Node) bool {
	for _, nodeValue := range p11ScalarValues(node) {
		value := strings.ToLower(strings.TrimSpace(nodeValue))
		if value == "" || workflowMatrixReferencePattern.MatchString(value) {
			continue
		}

		if runnerValuesContainLinux([]string{value}) || runnerValuesAreRecognizedNonLinux([]string{value}) {
			continue
		}

		return true
	}

	return false
}

func matrixReferenceCanRunOnLinux(matrix *yaml.Node, ref string) (bool, bool) {
	var values []*yaml.Node

	if direct := p11MappingValue(matrix, ref); direct != nil {
		values = append(values, direct)
	}

	// Over-approximate matrix combinations: exclude rows do not remove runner
	// evidence, and include rows add it. Requiring an extra timeout is safer
	// than silently missing a Linux-capable matrix job.
	include := p11MappingValue(matrix, "include")
	if include != nil && include.Kind == yaml.SequenceNode {
		for _, item := range include.Content {
			if value := p11MappingValue(item, ref); value != nil {
				values = append(values, value)
			}
		}
	}

	if len(values) == 0 {
		return false, false
	}

	for _, value := range values {
		linux, resolved := matrixRunnerValueCanRunOnLinux(value)
		if !resolved {
			return false, false
		}

		if linux {
			return true, true
		}
	}

	return false, true
}

func matrixRunnerValueCanRunOnLinux(node *yaml.Node) (bool, bool) {
	if node == nil {
		return false, false
	}

	if node.Kind != yaml.SequenceNode {
		return runnerNodeCanRunOnLinux(node)
	}

	resolved := len(node.Content) != 0
	for _, child := range node.Content {
		linux, childResolved := runnerNodeCanRunOnLinux(child)
		if linux {
			return true, true
		}

		if !childResolved {
			resolved = false
		}
	}

	return false, resolved
}

func runnerNodeCanRunOnLinux(node *yaml.Node) (bool, bool) {
	values := p11ScalarValues(node)
	if len(values) == 0 {
		return false, false
	}

	if runnerValuesContainLinux(values) {
		return true, true
	}

	if runnerValuesAreRecognizedNonLinux(values) {
		return false, true
	}

	return false, false
}

func runnerValuesContainLinux(values []string) bool {
	for _, nodeValue := range values {
		value := strings.ToLower(nodeValue)
		if strings.Contains(value, "ubuntu") || strings.Contains(value, "linux") {
			return true
		}
	}

	return false
}

func runnerValuesAreRecognizedNonLinux(values []string) bool {
	foundNonLinux := false

	for _, nodeValue := range values {
		value := strings.ToLower(strings.TrimSpace(nodeValue))
		if value == "" {
			continue
		}

		if strings.Contains(value, "${{") {
			return false
		}

		if strings.Contains(value, "macos") || strings.Contains(value, "windows") {
			foundNonLinux = true

			continue
		}

		return false
	}

	return foundNonLinux
}

func positiveTimeout(node *yaml.Node) bool {
	if node == nil || node.Kind != yaml.ScalarNode || node.ShortTag() != "!!int" {
		return false
	}

	minutes, err := strconv.Atoi(node.Value)

	return err == nil && minutes >= 1 && minutes <= workflowJobTimeoutLimitMinutes
}
