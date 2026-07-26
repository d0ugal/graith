package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/d0ugal/graith/internal/cipolicy"
)

func TestGenerateWritesPrivateValidManifestFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "braw-policy.json")
	inventory := filepath.Join("..", "..", "internal", "cibaseline", "inventory.json")

	if err := run([]string{"-inventory", inventory, "-output", path, "generate"}); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("manifest mode = %#o, want 0600", got)
	}

	if err := run([]string{"-inventory", inventory, "-manifest", path, "validate"}); err != nil {
		t.Fatal(err)
	}
}

func TestOutputFlagIsOnlyValidForGenerate(t *testing.T) {
	err := run([]string{"-output", filepath.Join(t.TempDir(), "canny.json"), "digest"})
	if err == nil || !strings.Contains(err.Error(), "-output is only valid with generate or plan") {
		t.Fatalf("run() error = %v, want output flag rejection", err)
	}
}

func TestPlanWritesDigestBoundRunPlan(t *testing.T) {
	tempDir := t.TempDir()

	changedFiles := filepath.Join(tempDir, "braw-files.txt")
	if err := os.WriteFile(changedFiles, []byte("internal/daemon/session.go\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	output := filepath.Join(tempDir, "canny-plan.json")
	manifestPath := filepath.Join("..", "..", "internal", "cipolicy", "manifest.json")
	now := stableCLIPlanTime(t)
	createdAt := now.Format(time.RFC3339)
	expiresAt := now.Add(time.Hour).Format(time.RFC3339)

	if err := run([]string{
		"-manifest", manifestPath,
		"-changed-files", changedFiles,
		"-event", "pull_request",
		"-ref", "refs/pull/17/merge",
		"-base-ref", "refs/heads/main",
		"-head-ref", "refs/heads/canny",
		"-head-repository", cipolicy.DefaultRepository,
		"-same-repository-agent",
		"-commit", strings.Repeat("1", 40),
		"-tree", strings.Repeat("2", 40),
		"-created-at", createdAt,
		"-expires-at", expiresAt,
		"-output", output,
		"plan",
	}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}

	var plan cipolicy.RunPlan
	if err := json.Unmarshal(data, &plan); err != nil {
		t.Fatal(err)
	}

	manifest, err := cipolicy.ReadManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}

	if err := plan.ValidateAt(manifest, now); err != nil {
		t.Fatal(err)
	}

	if plan.Superset {
		t.Fatalf("Superset = true, want narrowed replay")
	}
}

func TestReadChangedFilesPreservesPathWhitespace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dreich-files.txt")
	if err := os.WriteFile(path, []byte(" internal/daemon/session.go\nwebsite/content/docs/canny.md \r\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	files, exact, err := readChangedFiles(path)
	if err != nil {
		t.Fatal(err)
	}

	if !exact {
		t.Fatalf("exact = false, want true")
	}

	want := []string{" internal/daemon/session.go", "website/content/docs/canny.md ", ""}
	if len(files) != len(want) {
		t.Fatalf("files = %v, want %v", files, want)
	}

	for i := range want {
		if files[i] != want[i] {
			t.Fatalf("files[%d] = %q, want %q", i, files[i], want[i])
		}
	}
}

func TestPlanPreservesBlankChangedFileRowsAsDetectorErrors(t *testing.T) {
	tempDir := t.TempDir()

	changedFiles := filepath.Join(tempDir, "thrawn-files.txt")
	if err := os.WriteFile(changedFiles, []byte("internal/daemon/session.go\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	output := filepath.Join(tempDir, "strath-plan.json")
	manifestPath := filepath.Join("..", "..", "internal", "cipolicy", "manifest.json")
	now := stableCLIPlanTime(t)

	if err := run([]string{
		"-manifest", manifestPath,
		"-changed-files", changedFiles,
		"-event", "pull_request",
		"-ref", "refs/pull/17/merge",
		"-base-ref", "refs/heads/main",
		"-head-ref", "refs/heads/canny",
		"-head-repository", cipolicy.DefaultRepository,
		"-same-repository-agent",
		"-detector-error", "dreich detector",
		"-detector-error", "thrawn detector",
		"-commit", strings.Repeat("1", 40),
		"-tree", strings.Repeat("2", 40),
		"-created-at", now.Format(time.RFC3339),
		"-expires-at", now.Add(time.Hour).Format(time.RFC3339),
		"-output", output,
		"plan",
	}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}

	var plan cipolicy.RunPlan
	if err := json.Unmarshal(data, &plan); err != nil {
		t.Fatal(err)
	}

	if !plan.Superset || !slices.Contains(plan.SupersetReasons, "detector-error") {
		t.Fatalf("superset = %v reasons = %v, want detector-error superset", plan.Superset, plan.SupersetReasons)
	}

	if !slices.Contains(plan.DetectorErrors, "dreich detector") || !slices.Contains(plan.DetectorErrors, "thrawn detector") {
		t.Fatalf("detector errors = %v, want both repeated flag values", plan.DetectorErrors)
	}
}

func TestPlanWithoutChangedFilesSelectsUnknownFileListSuperset(t *testing.T) {
	tempDir := t.TempDir()

	output := filepath.Join(tempDir, "bothy-plan.json")
	manifestPath := filepath.Join("..", "..", "internal", "cipolicy", "manifest.json")
	now := stableCLIPlanTime(t)

	if err := run([]string{
		"-manifest", manifestPath,
		"-event", "pull_request",
		"-ref", "refs/pull/17/merge",
		"-base-ref", "refs/heads/main",
		"-head-ref", "refs/heads/canny",
		"-head-repository", cipolicy.DefaultRepository,
		"-same-repository-agent",
		"-commit", strings.Repeat("1", 40),
		"-tree", strings.Repeat("2", 40),
		"-created-at", now.Format(time.RFC3339),
		"-expires-at", now.Add(time.Hour).Format(time.RFC3339),
		"-output", output,
		"plan",
	}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}

	var plan cipolicy.RunPlan
	if err := json.Unmarshal(data, &plan); err != nil {
		t.Fatal(err)
	}

	if plan.ExactFileList {
		t.Fatalf("ExactFileList = true, want false")
	}

	if plan.ChangedFilesDigest != "" {
		t.Fatalf("ChangedFilesDigest = %q, want empty", plan.ChangedFilesDigest)
	}

	if !plan.Superset || !slices.Contains(plan.SupersetReasons, "file-list-unknown") {
		t.Fatalf("superset = %v reasons = %v, want file-list-unknown", plan.Superset, plan.SupersetReasons)
	}
}

func TestSummaryWritesDiagnosticMarkdown(t *testing.T) {
	tempDir := t.TempDir()
	manifestPath := filepath.Join("..", "..", "internal", "cipolicy", "manifest.json")
	inventoryPath := filepath.Join("..", "..", "internal", "cibaseline", "inventory.json")
	changedFiles := filepath.Join(tempDir, "braw-files.txt")

	if err := os.WriteFile(changedFiles, []byte(".github/workflows/ci.yml\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	manifest, err := cipolicy.ReadManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}

	plan, err := cipolicy.BuildPlan(manifest, cipolicy.PlanOptions{
		Event: cipolicy.EventInput{
			GitHubEvent:         "pull_request",
			Ref:                 "refs/pull/17/merge",
			BaseRef:             "refs/heads/main",
			HeadRef:             "refs/heads/canny",
			BaseRepository:      cipolicy.DefaultRepository,
			HeadRepository:      cipolicy.DefaultRepository,
			Commit:              strings.Repeat("1", 40),
			Tree:                strings.Repeat("2", 40),
			SameRepositoryAgent: true,
		},
		ChangedFiles:  []string{".github/workflows/ci.yml"},
		ExactFileList: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	planData, err := plan.MarshalCanonical()
	if err != nil {
		t.Fatal(err)
	}

	planPath := filepath.Join(tempDir, "canny-plan.json")
	if err := os.WriteFile(planPath, planData, 0o600); err != nil {
		t.Fatal(err)
	}

	output := captureStdout(t, func() error {
		return run([]string{
			"-inventory", inventoryPath,
			"-manifest", manifestPath,
			"-plan-input", planPath,
			"-changed-files", changedFiles,
			"-event", "pull_request",
			"-ref", "refs/pull/17/merge",
			"-head-sha", strings.Repeat("1", 40),
			"-run-url", "https://github.com/d0ugal/graith/actions/runs/123",
			"-macos-detector-result", "success",
			"-macos-detector-output", "false",
			"summary",
		})
	})

	for _, want := range []string{
		"# CI shadow summary",
		"Diagnostic only",
		"Current required checks still decide mergeability",
		"`Native backend gate`",
		"does not aggregate repository-wide observed job results",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("summary output missing %q:\n%s", want, output)
		}
	}
}

func stableCLIPlanTime(t *testing.T) time.Time {
	t.Helper()

	manifestPath := filepath.Join("..", "..", "internal", "cipolicy", "manifest.json")

	manifest, err := cipolicy.ReadManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}

	var earliest time.Time

	for _, decision := range manifest.Unsupported {
		expires, err := time.Parse(time.DateOnly, decision.Expires)
		if err != nil {
			t.Fatal(err)
		}

		if earliest.IsZero() || expires.Before(earliest) {
			earliest = expires
		}
	}

	if earliest.IsZero() {
		return time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	}

	candidate := earliest.AddDate(0, -1, 0).Add(10 * time.Hour).UTC()
	current := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)

	if candidate.After(current) {
		return current
	}

	return candidate
}

func captureStdout(t *testing.T, fn func() error) string {
	t.Helper()

	original := os.Stdout

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	os.Stdout = writer
	runErr := fn()
	closeErr := writer.Close()
	os.Stdout = original

	if runErr != nil {
		t.Fatal(runErr)
	}

	if closeErr != nil {
		t.Fatal(closeErr)
	}

	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}

	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}

	return string(data)
}
