package libghosttydeps

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommittedDependencyUnit(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}

	if err := Verify(root); err != nil {
		t.Fatal(err)
	}
}

func TestTreeSHA256BindsPathsAndContent(t *testing.T) {
	root := t.TempDir()
	write := func(name, content string) {
		t.Helper()

		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(path, []byte(content), 0o644); err != nil { //nolint:gosec // test fixture
			t.Fatal(err)
		}
	}
	write("bothy/braw.h", "canny")
	write("croft.h", "dreich")

	first, err := TreeSHA256(root)
	if err != nil {
		t.Fatal(err)
	}

	second, err := TreeSHA256(root)
	if err != nil {
		t.Fatal(err)
	}

	if first != second {
		t.Fatalf("stable tree digest = %q then %q", first, second)
	}

	write("croft.h", "thrawn")

	changed, err := TreeSHA256(root)
	if err != nil {
		t.Fatal(err)
	}

	if changed == first {
		t.Fatal("tree digest did not bind changed bytes")
	}
}

func TestReplaceNoticesInventoryRejectsMissingMarkers(t *testing.T) {
	lock := Lock{SchemaVersion: 1}

	_, err := ReplaceNoticesInventory("braw", lock)
	if err == nil || !strings.Contains(err.Error(), "markers") {
		t.Fatalf("missing-marker error = %v", err)
	}
}

func TestValidateShortGhosttyCommitDoesNotPanic(t *testing.T) {
	lock := Lock{SchemaVersion: 1}

	err := lock.Validate()
	if err == nil || !strings.Contains(err.Error(), "Ghostty commit") {
		t.Fatalf("short-commit validation error = %v", err)
	}
}

func TestDecodeLockRejectsTrailingJSON(t *testing.T) {
	_, err := DecodeLock([]byte(`{} {}`))
	if err == nil || !strings.Contains(err.Error(), "trailing JSON") {
		t.Fatalf("trailing JSON error = %v", err)
	}
}

func TestGenerationDecodeAllowsStaleDerivedURLs(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(root, LockFilename))
	if err != nil {
		t.Fatal(err)
	}

	lock, err := DecodeLock(data)
	if err != nil {
		t.Fatal(err)
	}

	lock.Ghostty.Commit = strings.Repeat("a", 40)
	lock.SPDXTools.Version += "-next"

	stale, err := json.Marshal(lock)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := DecodeLock(stale); err == nil ||
		!strings.Contains(err.Error(), "Apple artifact URL") ||
		!strings.Contains(err.Error(), "SPDX tools URL") {
		t.Fatalf("strict stale-projection error = %v", err)
	}

	if _, err := decodeLock(stale, false); err != nil {
		t.Fatalf("generation decode rejected stale derived URLs: %v", err)
	}
}

func TestFollowWrapperGhosttyPin(t *testing.T) {
	const (
		previous = "1111111111111111111111111111111111111111"
		approved = "2222222222222222222222222222222222222222"
		updated  = "3333333333333333333333333333333333333333"
	)

	lock := Lock{
		GoLibghostty: GoDependency{TestedGhosttyCommit: previous},
		Ghostty:      Ghostty{Commit: approved},
	}
	if followWrapperGhosttyPin(&lock, previous) {
		t.Fatal("unchanged wrapper pin reported an update")
	}

	if lock.Ghostty.Commit != approved {
		t.Fatalf("approved Ghostty commit = %s, want %s", lock.Ghostty.Commit, approved)
	}

	if !followWrapperGhosttyPin(&lock, updated) {
		t.Fatal("changed wrapper pin was not reported")
	}

	if lock.Ghostty.Commit != updated || lock.GoLibghostty.TestedGhosttyCommit != updated {
		t.Fatalf("wrapper pair = Ghostty %s, tested %s; want %s", lock.Ghostty.Commit, lock.GoLibghostty.TestedGhosttyCommit, updated)
	}
}

func TestReconcileVersion(t *testing.T) {
	selected := "1.0.0"
	if err := reconcileVersion("uucode", "2.0.0", &selected, false); err == nil || !strings.Contains(err.Error(), "compatible Ghostty") {
		t.Fatalf("incompatible transitive error = %v", err)
	}

	if selected != "1.0.0" {
		t.Fatalf("rejected version changed to %s", selected)
	}

	if err := reconcileVersion("uucode", "2.0.0", &selected, true); err != nil {
		t.Fatal(err)
	}

	if selected != "2.0.0" {
		t.Fatalf("derived version = %s, want 2.0.0", selected)
	}
}

func TestPrimaryDependencyDiffers(t *testing.T) {
	previous := Lock{
		GoLibghostty: GoDependency{Commit: strings.Repeat("1", 40)},
		Ghostty:      Ghostty{Commit: strings.Repeat("2", 40)},
	}
	current := previous

	current.Zig.Version = "canny"
	if primaryDependencyDiffers(previous, current) {
		t.Fatal("transitive-only change reported as a primary update")
	}

	current.Ghostty.Commit = strings.Repeat("3", 40)
	if !primaryDependencyDiffers(previous, current) {
		t.Fatal("Ghostty change was not reported as a primary update")
	}
}

func TestLicenseReviewBindsEvidenceAndConclusion(t *testing.T) {
	lock := Lock{
		GoLibghostty: GoDependency{LicenseSHA256: strings.Repeat("1", 64), LicenseConclusion: "MIT"},
		Ghostty:      Ghostty{LicenseSHA256: strings.Repeat("2", 64), LicenseConclusion: "MIT"},
		Zig:          Zig{LicenseSHA256: strings.Repeat("3", 64), LicenseConclusion: "MIT"},
		Uucode: Uucode{
			LicenseSHA256: strings.Repeat("4", 64), DecoderNoticeSHA256: strings.Repeat("5", 64),
			UnicodeNoticeSHA256: strings.Repeat("6", 64), LicenseConclusion: "MIT AND Unicode-3.0",
		},
		Highway: Highway{
			LicenseSHA256: strings.Repeat("7", 64), LicenseConclusion: "BSD-3-Clause", LicenseDeclared: "Apache-2.0 OR BSD-3-Clause",
		},
		Simdutf: Simdutf{
			LicenseSHA256: strings.Repeat("8", 64), LicenseConclusion: "MIT", LicenseDeclared: "Apache-2.0 OR MIT",
		},
	}
	AcceptLicenseReviews(&lock)

	if err := VerifyLicenseReviews(lock); err != nil {
		t.Fatal(err)
	}

	lock.Uucode.UnicodeNoticeSHA256 = strings.Repeat("9", 64)

	lock.Highway.LicenseConclusion = "Apache-2.0"
	if err := VerifyLicenseReviews(lock); err == nil || !strings.Contains(err.Error(), "uucode") || !strings.Contains(err.Error(), "Highway") {
		t.Fatalf("changed license review error = %v", err)
	}
}

func TestGeneratedUnitRollbackRestoresEveryManagedPath(t *testing.T) {
	root := t.TempDir()
	for _, name := range generatedUnitFiles {
		writeTestFile(t, filepath.Join(root, name), "old "+name)
	}

	writeTestFile(t, filepath.Join(root, generatedHeaders, "braw.h"), "canny")

	wantErr := errors.New("dreich late failure")

	err := withGeneratedUnitRollback(root, func() error {
		for _, name := range generatedUnitFiles {
			writeTestFile(t, filepath.Join(root, name), "changed "+name)
		}

		if removeErr := os.RemoveAll(filepath.Join(root, generatedHeaders)); removeErr != nil {
			t.Fatal(removeErr)
		}

		writeTestFile(t, filepath.Join(root, generatedHeaders, "thrawn.h"), "blether")

		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("rollback error = %v, want %v", err, wantErr)
	}

	for _, name := range generatedUnitFiles {
		data, readErr := os.ReadFile(filepath.Join(root, name))
		if readErr != nil {
			t.Fatal(readErr)
		}

		if string(data) != "old "+name {
			t.Fatalf("restored %s = %q", name, data)
		}
	}

	data, err := os.ReadFile(filepath.Join(root, generatedHeaders, "braw.h"))
	if err != nil || string(data) != "canny" {
		t.Fatalf("restored header = %q, %v", data, err)
	}

	if _, err := os.Stat(filepath.Join(root, generatedHeaders, "thrawn.h")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new header survived rollback: %v", err)
	}
}

func TestVerifyRejectsEveryStaleProjection(t *testing.T) {
	sourceRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}

	lock, err := LoadLock(filepath.Join(sourceRoot, LockFilename))
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		path      string
		old       string
		replace   string
		wantError string
	}{
		{"notice", NoticesFilename, "wrapper-tested Ghostty commit", "stale wrapper-tested Ghostty commit", "notice inventory is stale"},
		{"SPDX", SPDXFilename, `"spdxVersion": "SPDX-2.3"`, `"spdxVersion": "SPDX-2.2"`, "generated SPDX inventory is stale"},
		{"go.mod", "go.mod", lock.GoLibghostty.Version, "v0.0.0-20000101000000-111111111111", "go-libghostty requirement does not match"},
		{"go.sum", "go.sum", lock.GoLibghostty.ModuleSum, "h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", "module sum does not match"},
		{"Swift checksum", "gui/shared/Package.swift", lock.Ghostty.AppleArtifact.SHA256, strings.Repeat("a", 64), "checksum does not uniquely match"},
		{"header", filepath.Join(generatedHeaders, "vt.h"), "#define", "#define STALE 1\n#define", "header tree SHA-256"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := copyCommittedUnit(t, sourceRoot)
			path := filepath.Join(root, test.path)

			data, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}

			stale := strings.Replace(string(data), test.old, test.replace, 1)
			if stale == string(data) {
				t.Fatalf("stale fixture pattern %q not found in %s", test.old, test.path)
			}

			if writeErr := os.WriteFile(path, []byte(stale), 0o644); writeErr != nil { //nolint:gosec // test fixture
				t.Fatal(writeErr)
			}

			if verifyErr := Verify(root); verifyErr == nil || !strings.Contains(verifyErr.Error(), test.wantError) {
				t.Fatalf("stale %s verification error = %v", test.name, verifyErr)
			}
		})
	}
}

func TestGeneratedProjectionAllowsPendingLicenseReviewButMergeGateRejectsIt(t *testing.T) {
	sourceRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}

	root := copyCommittedUnit(t, sourceRoot)

	lockPath := filepath.Join(root, LockFilename)

	lock, err := LoadLock(lockPath)
	if err != nil {
		t.Fatal(err)
	}

	lock.Ghostty.LicenseSHA256 = strings.Repeat("a", 64)
	if err := WriteLock(lockPath, lock); err != nil {
		t.Fatal(err)
	}

	if err := writeGeneratedMetadata(root, lock); err != nil {
		t.Fatal(err)
	}

	if err := VerifyGenerated(root); err != nil {
		t.Fatalf("mechanical projection with pending review = %v", err)
	}

	if err := Verify(root); err == nil || !strings.Contains(err.Error(), "Ghostty license evidence or conclusion changed") {
		t.Fatalf("pending license review merge-gate error = %v", err)
	}
}

func copyCommittedUnit(t *testing.T, sourceRoot string) string {
	t.Helper()

	root := t.TempDir()

	files := []string{
		LockFilename,
		SPDXFilename,
		NoticesFilename,
		"go.mod",
		"go.sum",
		"gui/shared/Package.swift",
		"scripts/libghostty-native.sh",
		"gui/shared/build-libghostty.sh",
		".github/workflows/libghostty-native.yml",
	}
	for _, name := range files {
		from := filepath.Join(sourceRoot, name)

		to := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(to), 0o750); err != nil {
			t.Fatal(err)
		}

		data, err := os.ReadFile(from)
		if err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(to, data, 0o644); err != nil { //nolint:gosec // test fixture
			t.Fatal(err)
		}
	}

	fromHeaders := filepath.Join(sourceRoot, generatedHeaders)

	toHeaders := filepath.Join(root, generatedHeaders)
	if err := copyTree(fromHeaders, toHeaders); err != nil {
		t.Fatal(err)
	}

	return root
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil { //nolint:gosec // test fixture
		t.Fatal(err)
	}
}
