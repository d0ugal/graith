package release_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/testutil"
)

func newTagRepository(t *testing.T) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "bothy")
	git(t, filepath.Dir(dir), "init", "-b", "main", dir)
	writeFile(t, dir, "history.txt", "braw\n")
	git(t, dir, "add", "history.txt")
	git(t, dir, "commit", "-m", "braw history")

	return dir
}

func commitTagHistory(t *testing.T, dir, word string) {
	t.Helper()

	file := filepath.Join(dir, "history.txt")

	contents, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}

	// #nosec G703 -- file is the fixed fixture name inside this test's t.TempDir().
	if err := os.WriteFile(file, append(contents, word+"\n"...), 0o600); err != nil {
		t.Fatal(err)
	}

	git(t, dir, "add", "history.txt")
	git(t, dir, "commit", "-m", word+" history")
}

func runDevReleaseBaseTag(t *testing.T, dir string) (string, error) {
	t.Helper()

	script := filepath.Join(repoRoot(t), "scripts", "dev-release-base-tag.sh")
	command := exec.Command(script)
	command.Dir = dir
	command.Env = testutil.GitEnv()

	output, err := command.CombinedOutput()

	return string(output), err
}

func TestDevReleaseBaseTagIgnoresOperationalTags(t *testing.T) {
	dir := newTagRepository(t)
	git(t, dir, "tag", "v0.69.6")

	commitTagHistory(t, dir, "canny")
	git(t, dir, "tag", "dev")

	commitTagHistory(t, dir, "dreich")
	git(t, dir, "tag", "libghostty-vt-d4ac93a")

	commitTagHistory(t, dir, "thrawn")

	output, err := runDevReleaseBaseTag(t, dir)
	if err != nil {
		t.Fatalf("select dev release base: %v\n%s", err, output)
	}

	if output != "v0.69.6\n" {
		t.Fatalf("dev release base = %q, want v0.69.6", output)
	}
}

func TestDevReleaseBaseTagUsesDeterministicVersionOrder(t *testing.T) {
	dir := newTagRepository(t)
	git(t, dir, "config", "tag.sort", "refname")

	for _, tag := range []string{"v0.9.99", "v0.10.0", "v0.2.100", "v1.2.3-rc.1", "v01.20.0"} {
		git(t, dir, "tag", tag)
	}

	output, err := runDevReleaseBaseTag(t, dir)
	if err != nil {
		t.Fatalf("select dev release base: %v\n%s", err, output)
	}

	if output != "v0.10.0\n" {
		t.Fatalf("dev release base = %q, want v0.10.0", output)
	}
}

func TestDevReleaseBaseTagFailsWithoutStableSemver(t *testing.T) {
	dir := newTagRepository(t)

	for _, tag := range []string{"dev", "libghostty-vt-d4ac93a", "v1.2.3-rc.1", "v1.2"} {
		git(t, dir, "tag", tag)
	}

	output, err := runDevReleaseBaseTag(t, dir)
	if err == nil {
		t.Fatalf("missing stable semantic release unexpectedly succeeded: %q", output)
	}

	if !strings.Contains(output, "no stable semantic release tag (vMAJOR.MINOR.PATCH) is reachable from HEAD") {
		t.Fatalf("missing semantic release error is not actionable: %q", output)
	}
}
