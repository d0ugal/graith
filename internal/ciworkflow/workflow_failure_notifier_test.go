package ciworkflow

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestWorkflowFailureNotifierTriggerAndWatchList(t *testing.T) {
	repoRoot := p11RepoRoot()
	workflowPath := filepath.Join(repoRoot, ".github/workflows/mainline-failure-notifier.yml")
	workflowText := readPolicyFile(t, workflowPath)
	workflowYAML := readWorkflowYAML(t, workflowPath)
	workflowRun := p11MappingValue(p11MappingValue(workflowYAML, "on"), "workflow_run")

	if workflowRun == nil {
		t.Fatal("mainline failure notifier is not triggered by workflow_run")
	}

	assertContains(t, workflowText, "zizmor: ignore[dangerous-triggers]")
	assertStringsEqual(t, "workflow_run types", p11StringList(p11MappingValue(workflowRun, "types")), []string{"completed"})
	assertStringsEqual(t, "watched workflows", p11StringList(p11MappingValue(workflowRun, "workflows")), []string{
		"CI",
		"Release Please",
		"Stable Release",
	})

	for _, watched := range p11StringList(p11MappingValue(workflowRun, "workflows")) {
		if watched == "Mainline Failure Notifier" {
			t.Fatal("mainline failure notifier must not watch itself")
		}
	}

	concurrency := p11MappingValue(workflowYAML, "concurrency")
	if concurrency == nil {
		t.Fatal("mainline failure notifier has no concurrency policy")
	}

	const wantGroup = "mainline-failure-notifier-${{ github.event.workflow_run.name }}-${{ github.event.workflow_run.head_branch }}"
	if got := p11Scalar(p11MappingValue(concurrency, "group")); got != wantGroup {
		t.Fatalf("mainline failure notifier concurrency group = %q, want %q", got, wantGroup)
	}

	if got := p11Scalar(p11MappingValue(concurrency, "cancel-in-progress")); got != "false" {
		t.Fatalf("mainline failure notifier cancel-in-progress = %q, want false", got)
	}
}

func TestWorkflowFailureNotifierPermissionsAreNarrow(t *testing.T) {
	workflow := readWorkflowFailureNotifier(t)

	want := map[string]string{
		"actions": "read",
		"issues":  "write",
	}
	if !reflect.DeepEqual(workflow.Permissions, want) {
		t.Fatalf("mainline failure notifier permissions = %#v, want %#v", workflow.Permissions, want)
	}

	if workflow.PermissionsExpression != "" {
		t.Fatalf("mainline failure notifier permissions expression = %q, want mapping", workflow.PermissionsExpression)
	}

	job := p11WorkflowJob(t, workflow, "notify")
	if job.PermissionsExpression != "" || len(job.Permissions) != 0 {
		t.Fatalf("notify job overrides workflow permissions: expression=%q map=%#v", job.PermissionsExpression, job.Permissions)
	}
}

func TestWorkflowFailureNotifierTrustGateAndIssueLifecycle(t *testing.T) {
	workflow := readWorkflowFailureNotifier(t)
	job := p11WorkflowJob(t, workflow, "notify")
	step := p11WorkflowStep(t, job, "Open, update, or close workflow failure issue")
	script := step.Run

	for name, want := range map[string]string{
		"push-only gate":            `if [ "$TRIGGER_EVENT" != "push" ]; then`,
		"ci release branch":         `"CI"|"Release Please")`,
		"main gate":                 `if [ "$REF_NAME" != "main" ]; then`,
		"stable version tag gate":   `[[ ! "$REF_NAME" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ]]`,
		"stable marker":             `graith-workflow-failure-notifier:v1 workflow=${WORKFLOW_NAME} ref=${REF_NAME}`,
		"failure issue create":      `gh api --method POST "repos/$REPO/issues"`,
		"failure issue update":      `gh api --method PATCH "repos/$REPO/issues/$issue_number"`,
		"repeat failure comment":    `comment_on_repeat_failure "$issue_number" "$failed_jobs"`,
		"success issue comment":     `gh api --method POST "repos/$REPO/issues/$issue_number/comments"`,
		"success issue close":       `-f state=closed`,
		"success close reason":      `-f state_reason=completed`,
		"failed job lookup":         `gh api --paginate --slurp "repos/$REPO/actions/runs/$RUN_ID/jobs?per_page=100"`,
		"failure conclusions":       `failure|timed_out|cancelled|action_required)`,
		"failure open branch":       `create_or_update_failure_issue`,
		"success close branch":      `comment_and_close_issue`,
		"release blocker label":     `ensure_label "release-blocker"`,
		"best-effort label warning": `Could not apply notification labels`,
		"latest run lookup":         `actions/workflows/$WORKFLOW_ID/runs?event=$TRIGGER_EVENT&status=completed&per_page=100`,
		"latest run ordering":       `sort_by(.run_number)`,
		"stale run guard":           `Ignoring stale $WORKFLOW_NAME run #$RUN_NUMBER ($RUN_ID)`,
		"bot-owned issue lookup":    `select(.user.login == "github-actions[bot]")`,
	} {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(script, want) {
				t.Fatalf("notifier script missing %q:\n%s", want, script)
			}
		})
	}
}

func TestWorkflowFailureNotifierIgnoresStaleCompletedRuns(t *testing.T) {
	workflow := readWorkflowFailureNotifier(t)
	job := p11WorkflowJob(t, workflow, "notify")
	step := p11WorkflowStep(t, job, "Open, update, or close workflow failure issue")

	tests := map[string]struct {
		currentConclusion string
		latestConclusion  string
	}{
		"stale failure after newer success": {
			currentConclusion: "failure",
			latestConclusion:  "success",
		},
		"stale success after newer failure": {
			currentConclusion: "success",
			latestConclusion:  "failure",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			output, calls := runWorkflowFailureNotifier(t, step.Run, test.currentConclusion, test.latestConclusion)

			assertContains(t, output, "Ignoring stale CI run #101 (101) on main; latest completed run is 102.")
			assertContains(t, calls, "actions/workflows/88/runs?event=push&status=completed&per_page=100")
			assertNotContains(t, calls, "issues")
		})
	}
}

func TestWorkflowFailureNotifierDoesNotRunTriggeringCodeOrExternalActions(t *testing.T) {
	workflow := readWorkflowFailureNotifier(t)
	job := p11WorkflowJob(t, workflow, "notify")

	if p11JobUsesAction(job, "actions/checkout") {
		t.Fatal("notifier must not check out code from the triggering workflow run")
	}

	if p11JobRunsRepositoryControlledCode(job) {
		t.Fatal("notifier must not execute repository-controlled code")
	}

	for _, step := range job.Steps {
		if step.Uses != "" {
			t.Fatalf("notifier uses action %q; want only the runner shell and GitHub API", step.Uses)
		}
	}
}

func readWorkflowFailureNotifier(t *testing.T) P11WorkflowSummary {
	t.Helper()

	workflow, err := ReadP11WorkflowSummary(filepath.Join(p11RepoRoot(), ".github/workflows/mainline-failure-notifier.yml"))
	if err != nil {
		t.Fatal(err)
	}

	return workflow
}

func runWorkflowFailureNotifier(t *testing.T, script, currentConclusion, latestConclusion string) (string, string) {
	t.Helper()

	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "notifier.sh")
	ghPath := filepath.Join(dir, "gh")
	ghLogPath := filepath.Join(dir, "gh.log")

	if err := os.WriteFile(scriptPath, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}

	ghScript := `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$GRAITH_FAKE_GH_LOG"
case "$*" in
  *"actions/workflows/"*"runs?event=push&status=completed&per_page=100"*)
    cat <<JSON
[
  {
    "workflow_runs": [
      {"id": 102, "run_number": 102, "event": "push", "head_branch": "main", "status": "completed", "conclusion": "$GRAITH_FAKE_LATEST_CONCLUSION"},
      {"id": 101, "run_number": 101, "event": "push", "head_branch": "main", "status": "completed", "conclusion": "$GRAITH_FAKE_CURRENT_CONCLUSION"}
    ]
  }
]
JSON
    ;;
  *)
    printf 'unexpected gh invocation: %s\n' "$*" >&2
    exit 64
    ;;
esac
`
	if err := os.WriteFile(ghPath, []byte(ghScript), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(ghPath, 0o700); err != nil { //nolint:gosec // Fake gh command must be executable for workflow policy coverage.
		t.Fatal(err)
	}

	cmd := exec.Command("bash", scriptPath)

	cmd.Env = append(os.Environ(),
		"GH_TOKEN=braw-token",
		"REPO=d0ugal/graith",
		"WORKFLOW_NAME=CI",
		"WORKFLOW_ID=88",
		"RUN_ID=101",
		"RUN_NUMBER=101",
		"CONCLUSION="+currentConclusion,
		"TRIGGER_EVENT=push",
		"REF_NAME=main",
		"HEAD_SHA=brawcafe",
		"RUN_URL=https://github.com/d0ugal/graith/actions/runs/101",
		"GRAITH_FAKE_CURRENT_CONCLUSION="+currentConclusion,
		"GRAITH_FAKE_LATEST_CONCLUSION="+latestConclusion,
		"GRAITH_FAKE_GH_LOG="+ghLogPath,
		"PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
	)

	combinedBytes, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("notifier command failed: %v\n%s", err, combinedBytes)
	}

	ghLog, err := os.ReadFile(ghLogPath)
	if err != nil {
		t.Fatalf("read fake gh log: %v\ncommand output:\n%s", err, combinedBytes)
	}

	return string(combinedBytes), string(ghLog)
}
