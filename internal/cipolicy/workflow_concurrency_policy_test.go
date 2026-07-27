package cipolicy

import (
	"path/filepath"
	"slices"
	"testing"
)

func TestExpensivePullRequestWorkflowsCancelOnlySupersededPRRuns(t *testing.T) {
	repoRoot := p11RepoRoot()

	tests := map[string]struct {
		workflowPath string
		wantGroup    string
	}{
		"ci": {
			workflowPath: ".github/workflows/ci.yml",
			wantGroup:    "ci-${{ github.event_name == 'pull_request' && github.event.pull_request.number || github.run_id }}",
		},
		"libghostty-native": {
			workflowPath: ".github/workflows/libghostty-native.yml",
			wantGroup:    "libghostty-native-${{ github.event_name == 'pull_request' && github.event.pull_request.number || github.run_id }}",
		},
		"sandbox": {
			workflowPath: ".github/workflows/sandbox.yml",
			wantGroup:    "sandbox-${{ github.event_name == 'pull_request' && github.event.pull_request.number || github.run_id }}",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			workflow := readWorkflowYAML(t, filepath.Join(repoRoot, test.workflowPath))
			events := p11EventNames(p11MappingValue(workflow, "on"))

			if !slices.Contains(events, "pull_request") {
				t.Fatalf("%s events = %v, want pull_request", test.workflowPath, events)
			}

			concurrency := p11MappingValue(workflow, "concurrency")
			if concurrency == nil {
				t.Fatalf("%s has no concurrency policy", test.workflowPath)
			}

			if got := p11Scalar(p11MappingValue(concurrency, "group")); got != test.wantGroup {
				t.Fatalf("%s concurrency group = %q, want %q", test.workflowPath, got, test.wantGroup)
			}

			const wantCancel = "${{ github.event_name == 'pull_request' }}"
			if got := p11Scalar(p11MappingValue(concurrency, "cancel-in-progress")); got != wantCancel {
				t.Fatalf("%s cancel-in-progress = %q, want %q", test.workflowPath, got, wantCancel)
			}
		})
	}
}

func TestExpensivePullRequestWorkflowGatesHonorCancellation(t *testing.T) {
	repoRoot := p11RepoRoot()
	workflow := readWorkflowYAML(t, filepath.Join(repoRoot, ".github/workflows/libghostty-native.yml"))

	jobs := p11MappingValue(workflow, "jobs")

	nativeGate := p11MappingValue(jobs, "native-gate")
	if nativeGate == nil {
		t.Fatal("libghostty-native workflow has no native-gate job")
	}

	const want = "${{ !cancelled() }}"
	if got := p11Scalar(p11MappingValue(nativeGate, "if")); got != want {
		t.Fatalf("native-gate if = %q, want %q", got, want)
	}
}
