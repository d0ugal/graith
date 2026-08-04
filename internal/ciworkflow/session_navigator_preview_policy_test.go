package ciworkflow

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSessionNavigatorPreviewWorkflowPolicy(t *testing.T) {
	t.Parallel()

	workflowPath := filepath.Join(p11RepoRoot(), ".github", "workflows", "session-navigator-preview.yml")

	assertStringsEqual(t, "pull_request paths", docsPreviewPullRequestPaths(t, workflowPath), []string{
		"internal/client/**",
		"internal/config/**",
		"internal/sessionlabel/**",
		"internal/protocol/messages.go",
		"cmd/ciclassify/**",
		"cmd/docsdiff/**",
		"cmd/docspreview/**",
		"cmd/sessionnavshots/**",
		"internal/ciworkflow/**",
		"internal/docspreview/**",
		"scripts/session-navigator-terminal-screenshot.sh",
		"Makefile",
		"go.mod",
		"go.sum",
		".github/workflows/session-navigator-preview.yml",
	})

	workflow, err := ReadP11WorkflowSummary(workflowPath)
	if err != nil {
		t.Fatal(err)
	}

	assertStringsEqual(t, "workflow events", workflow.Events, []string{"pull_request", "schedule"})

	if want := map[string]string{"contents": "read"}; !reflect.DeepEqual(workflow.Permissions, want) {
		t.Fatalf("workflow permissions = %#v, want %#v", workflow.Permissions, want)
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

	detector := workflowDetectorScript(t, ".github/workflows/session-navigator-preview.yml", "changes", "session-navigator-preview")
	for _, want := range []string{
		`gh api "repos/$REPO/pulls/$PR/files" --paginate --jq '.[].filename'`,
		`go run ./cmd/ciclassify -mode session-navigator-preview`,
		`echo "trigger=true" >> "$GITHUB_OUTPUT"`,
		"Could not list PR files; running Session Navigator preview to be safe.",
		"Shared classifier failed; running Session Navigator preview to be safe.",
	} {
		if !strings.Contains(detector, want) {
			t.Fatalf("session navigator detector does not contain %q:\n%s", want, detector)
		}
	}

	preview := p11WorkflowJob(t, workflow, "preview")
	assertStringsEqual(t, "preview needs", preview.Needs, []string{"changes"})
	p11AssertJobIf(t, "preview", preview, `!cancelled() &&
github.event_name == 'pull_request' &&
github.event.action != 'closed' &&
(needs.changes.result != 'success' || needs.changes.outputs.trigger != 'false')`)

	if want := map[string]string{"contents": "read"}; !reflect.DeepEqual(preview.Permissions, want) {
		t.Fatalf("preview permissions = %#v, want %#v", preview.Permissions, want)
	}

	installTerminal := p11WorkflowStep(t, preview, "Install terminal screenshot dependencies")
	for _, want := range []string{
		"sudo apt-get update",
		"fonts-dejavu-core",
		"imagemagick",
		"xdotool",
		"xterm",
		"xvfb",
	} {
		if !strings.Contains(installTerminal.Run, want) {
			t.Fatalf("Install terminal screenshot dependencies step does not contain %q:\n%s", want, installTerminal.Run)
		}
	}

	render := p11WorkflowStep(t, preview, "Render head and base snapshots")
	for _, want := range []string{
		"go run ./cmd/sessionnavshots",
		`git cat-file -e "$BASE_SHA:cmd/sessionnavshots/main.go"`,
		"git worktree add --detach /tmp/base-tree",
		"Base Session Navigator snapshot render failed; treating screenshots as new.",
		"Base Session Navigator worktree setup failed; treating screenshots as new.",
		`jq 'map(.hasBase = true)' nav/pages.json`,
		`jq 'map(.hasBase = false)' nav/pages.json`,
	} {
		if !strings.Contains(render.Run, want) {
			t.Fatalf("Render head and base snapshots step does not contain %q:\n%s", want, render.Run)
		}
	}

	for _, forbidden := range []string{
		"SNAPSHOT_SIZES",
		"-sizes",
	} {
		if strings.Contains(render.Run, forbidden) {
			t.Fatalf("Render head and base snapshots step should rely on cmd/sessionnavshots default sizes, but contains %q:\n%s", forbidden, render.Run)
		}
	}

	screenshot := p11WorkflowStep(t, preview, "Screenshot head and base with xterm")
	for _, want := range []string{
		"xvfb-run -a",
		"scripts/session-navigator-terminal-screenshot.sh",
		"$PWD/nav/head-ansi",
		"$PWD/nav/base-ansi",
	} {
		if !strings.Contains(screenshot.Run, want) {
			t.Fatalf("Screenshot step does not contain %q:\n%s", want, screenshot.Run)
		}
	}

	terminalScript := readPolicyFile(t, filepath.Join(p11RepoRoot(), "scripts", "session-navigator-terminal-screenshot.sh"))
	for _, want := range []string{
		"ready_title=",
		`printf "\033]0;%s\007"`,
		"-e bash -c",
		"identify -quiet -format",
		"GRAITH_SESSION_NAV_SHOT_MIN_BYTES",
		"captured undersized screenshot",
		"captured blank or uniform screenshot",
		"screenshot validation failed",
		"could not capture nonblank screenshot",
	} {
		if !strings.Contains(terminalScript, want) {
			t.Fatalf("terminal screenshot script does not contain %q:\n%s", want, terminalScript)
		}
	}

	if strings.Contains(terminalScript, "bash -lc") {
		t.Fatalf("terminal screenshot script should not start a login shell:\n%s", terminalScript)
	}

	diff := p11WorkflowStep(t, preview, "Build diffs")
	if !strings.Contains(diff.Run, "go run ./cmd/docsdiff") || !strings.Contains(diff.Run, "nav/pages.json") {
		t.Fatalf("Build diffs step does not run docsdiff on navigator pages:\n%s", diff.Run)
	}

	upload := p11WorkflowStep(t, preview, "Upload diff assets")
	if upload.Uses != "actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a" ||
		upload.With["path"] != "nav/out" ||
		upload.With["if-no-files-found"] != "error" {
		t.Fatalf("upload step = %#v", upload)
	}

	publish := p11WorkflowJob(t, workflow, "publish")
	assertStringsEqual(t, "publish needs", publish.Needs, []string{"preview"})
	p11AssertJobIf(t, "publish", publish, `!cancelled() &&
github.event_name == 'pull_request' &&
github.event.action != 'closed' &&
needs.preview.result == 'success' &&
github.event.pull_request.head.repo.full_name == github.repository`)

	if want := map[string]string{"contents": "write", "pull-requests": "write"}; !reflect.DeepEqual(publish.Permissions, want) {
		t.Fatalf("publish permissions = %#v, want %#v", publish.Permissions, want)
	}

	publishCheckout, ok := docsPreviewCheckoutStep(publish)
	if !ok {
		t.Fatalf("publish job has no actions/checkout step: %#v", publish.Steps)
	}

	if publishCheckout.With["ref"] != "${{ github.event.pull_request.base.sha }}" ||
		publishCheckout.With["persist-credentials"] != "false" ||
		!strings.Contains(publishCheckout.With["sparse-checkout"], "cmd/docspreview") ||
		!strings.Contains(publishCheckout.With["sparse-checkout"], "internal/docspreview") {
		t.Fatalf("publish checkout with = %#v", publishCheckout.With)
	}

	support := p11WorkflowStep(t, publish, "Check trusted publisher support")
	if !strings.Contains(support.Run, "grep -Rqs 'session-navigator'") ||
		!strings.Contains(support.Run, "supported=true") ||
		!strings.Contains(support.Run, "skipping publish") {
		t.Fatalf("trusted publisher support step = %#v", support)
	}

	download := p11WorkflowStep(t, publish, "Download diff assets")
	if download.Uses != "actions/download-artifact@3e5f45b2cfb9172054b4087a40e8e0b5a5461e7c" ||
		download.If != "steps.trusted-publisher.outputs.supported == 'true'" ||
		download.With["path"] != "nav/out" {
		t.Fatalf("download step = %#v", download)
	}

	publishStep := p11WorkflowStep(t, publish, "Publish screenshots and comment")
	if publishStep.If != "steps.trusted-publisher.outputs.supported == 'true'" {
		t.Fatalf("publish step if = %q, want trusted publisher support guard", publishStep.If)
	}

	if !strings.Contains(publishStep.Run, "go run ./cmd/docspreview publish -suite session-navigator") {
		t.Fatalf("publish command missing session suite:\n%s", publishStep.Run)
	}

	cleanup := p11WorkflowJob(t, workflow, "cleanup")
	p11AssertJobIf(t, "cleanup", cleanup, `github.event.action == 'closed'`)

	if want := map[string]string{"contents": "write", "pull-requests": "write"}; !reflect.DeepEqual(cleanup.Permissions, want) {
		t.Fatalf("cleanup permissions = %#v, want %#v", cleanup.Permissions, want)
	}

	cleanupCheckout, ok := docsPreviewCheckoutStep(cleanup)
	if !ok {
		t.Fatalf("cleanup job has no actions/checkout step: %#v", cleanup.Steps)
	}

	if cleanupCheckout.With["ref"] != "${{ github.event.pull_request.base.sha }}" ||
		cleanupCheckout.With["persist-credentials"] != "false" ||
		!strings.Contains(cleanupCheckout.With["sparse-checkout"], "cmd/docspreview") ||
		!strings.Contains(cleanupCheckout.With["sparse-checkout"], "internal/docspreview") {
		t.Fatalf("cleanup checkout with = %#v", cleanupCheckout.With)
	}

	cleanupStep := p11WorkflowStep(t, cleanup, "Remove PR screenshots from the screenshots branch")
	if !strings.Contains(cleanupStep.Run, "go run ./cmd/docspreview cleanup -suite session-navigator") {
		t.Fatalf("cleanup command missing session suite:\n%s", cleanupStep.Run)
	}

	if !strings.Contains(cleanupStep.Run, "grep -Rqs 'session-navigator'") ||
		!strings.Contains(cleanupStep.Run, "skipping cleanup") {
		t.Fatalf("cleanup step missing trusted suite fallback:\n%s", cleanupStep.Run)
	}

	prune := p11WorkflowJob(t, workflow, "prune")
	p11AssertJobIf(t, "prune", prune, `github.event_name == 'schedule'`)

	if want := map[string]string{"contents": "write"}; !reflect.DeepEqual(prune.Permissions, want) {
		t.Fatalf("prune permissions = %#v, want %#v", prune.Permissions, want)
	}
}
