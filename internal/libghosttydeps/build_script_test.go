package libghosttydeps

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestAppleBuildScriptReplacesStaleFrameworkInReusableWorkdir(t *testing.T) {
	sourceRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	shared := filepath.Join(root, "gui", "shared")
	if err := os.MkdirAll(shared, 0o750); err != nil {
		t.Fatal(err)
	}

	script, err := os.ReadFile(filepath.Join(sourceRoot, "gui", "shared", "build-libghostty.sh"))
	if err != nil {
		t.Fatal(err)
	}
	buildScript := filepath.Join(shared, "build-libghostty.sh")
	if err := os.WriteFile(buildScript, script, 0o755); err != nil { //nolint:gosec // executable test fixture
		t.Fatal(err)
	}

	lock := []byte(`{"ghostty":{"commit":"` + testGhosttyCommit + `","repository":"https://example.invalid/ghostty.git"},"zig":{"version":"0.15.2"}}`)
	if err := os.WriteFile(filepath.Join(root, LockFilename), lock, 0o644); err != nil { //nolint:gosec // test fixture
		t.Fatal(err)
	}

	fakeBin := filepath.Join(root, "bothy-bin")
	if err := os.MkdirAll(fakeBin, 0o750); err != nil {
		t.Fatal(err)
	}
	writeExecutable := func(name, content string) {
		t.Helper()

		if err := os.WriteFile(filepath.Join(fakeBin, name), []byte(content), 0o755); err != nil { //nolint:gosec // executable test fixture
			t.Fatal(err)
		}
	}
	writeExecutable("jq", `#!/bin/sh
case "$2" in
  .ghostty.commit) printf '%s\n' '`+testGhosttyCommit+`' ;;
  .ghostty.repository) printf '%s\n' 'https://example.invalid/ghostty.git' ;;
  .zig.version) printf '%s\n' '0.15.2' ;;
  *) exit 1 ;;
esac
`)
	writeExecutable("git", "#!/bin/sh\nexit 0\n")
	writeExecutable("xcodebuild", "#!/bin/sh\nexit 0\n")
	writeExecutable("zig", `#!/bin/sh
if [ "${1:-}" = version ]; then
  printf '%s\n' '0.15.2'
  exit 0
fi
if [ "${1:-}" = build ]; then
  mkdir -p "$PWD/zig-out/lib/ghostty-vt.xcframework"
  printf '%s\n' 'canny' > "$PWD/zig-out/lib/ghostty-vt.xcframework/marker"
  exit 0
fi
exit 1
`)

	work := filepath.Join(root, "croft")
	if err := os.MkdirAll(filepath.Join(work, "ghostty", ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(work, "ghostty", "zig-out", "libghostty-vt.xcframework")
	if err := os.MkdirAll(stale, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stale, "marker"), []byte("dreich\n"), 0o644); err != nil { //nolint:gosec // test fixture
		t.Fatal(err)
	}

	cmd := exec.Command("bash", buildScript) //nolint:gosec // fixed executable and test-only script path
	cmd.Env = append(os.Environ(),
		"PATH="+fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"WORK="+work,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build script failed: %v\n%s", err, output)
	}

	marker := filepath.Join(shared, "Libraries", "libghostty-vt.xcframework", "marker")
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read copied framework marker: %v\n%s", err, output)
	}
	if string(data) != "canny\n" {
		t.Fatalf("copied framework marker = %q, want fresh output", data)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale framework survived reusable build: %v", err)
	}
}

const testGhosttyCommit = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
