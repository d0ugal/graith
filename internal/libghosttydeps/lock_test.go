package libghosttydeps

import (
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

func TestVerifyRejectsStaleNoticeProjection(t *testing.T) {
	sourceRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}

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

	fromHeaders := filepath.Join(sourceRoot, "gui/shared/Sources/CGhosttyVT/include/ghostty")

	toHeaders := filepath.Join(root, "gui/shared/Sources/CGhosttyVT/include/ghostty")
	if err := copyTree(fromHeaders, toHeaders); err != nil {
		t.Fatal(err)
	}

	noticesPath := filepath.Join(root, NoticesFilename)

	notices, err := os.ReadFile(noticesPath)
	if err != nil {
		t.Fatal(err)
	}

	stale := strings.Replace(string(notices), "wrapper-tested Ghostty commit", "stale wrapper-tested Ghostty commit", 1)
	if err := os.WriteFile(noticesPath, []byte(stale), 0o644); err != nil { //nolint:gosec // test fixture
		t.Fatal(err)
	}

	if err := Verify(root); err == nil || !strings.Contains(err.Error(), "notice inventory is stale") {
		t.Fatalf("stale notice verification error = %v", err)
	}
}
