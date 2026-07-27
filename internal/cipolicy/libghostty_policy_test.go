package cipolicy

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestLibghosttyIntegrationCoverageRemainsRuntimeValidated(t *testing.T) {
	repoRoot := p11RepoRoot()
	ci := readPolicyFile(t, filepath.Join(repoRoot, ".github/workflows/ci.yml"))
	native := readPolicyFile(t, filepath.Join(repoRoot, ".github/workflows/libghostty-native.yml"))

	assertContains(t, native, "name: libghostty core runtime")
	assertContains(t, native, "name: Core runtime and pinned artifact (macOS arm64)")
	assertContains(t, native, "name: Core runtime source build (${{ matrix.target }})")
	assertContains(t, native, "name: Native backend gate")
	assertContains(t, native, "macOS arm64 core runtime")
	assertContains(t, native, "Linux core runtime matrix")
	assertNotContains(t, native, "Adapter and pinned artifact")
	assertNotContains(t, native, "Adapter source build")

	if got := regexp.MustCompile(`go test -v -race -count=1 -run '\^\$' -tags=integration \./internal/integration/\.\.\.`).FindAllString(ci, -1); len(got) != 2 {
		t.Fatalf("generic integration compile-only command count = %d, want Linux and macOS", len(got))
	}

	linux := mustMatchString(t, native, `(?ms)^  linux-adapter:\n.*?(?:\n  [a-z][\w-]+:|\z)`)
	assertRegexp(t, linux, `goarch: amd64\n\s+run_tests: true`)
	assertRegexp(t, linux, `goarch: arm64\n\s+run_tests: false`)

	runtime := mustSubmatch(t, linux, `(?ms)if \[ "\$RUN_TESTS" = "true" \]; then(.*?)\n\s+else`)
	integration := mustMatchString(t, runtime, `(?ms)run_timed integration.*?(?:\n\s+run_timed |\z)`)
	assertRegexp(t, integration, `go test -v -race -count=1 \\\s*\n\s+-tags='libghostty integration' \./internal/integration/\.\.\.`)
	assertNotRegexp(t, integration, `-run`)

	if got := strings.Count(native, "-tags='libghostty integration' ./internal/integration/..."); got != 1 {
		t.Fatalf("native integration command count = %d, want 1", got)
	}

	assertNotRegexp(t, native, `default-builds:`)
}

func TestLibghosttyDefaultBuildsRemainNativeFree(t *testing.T) {
	repoRoot := p11RepoRoot()
	ci := readPolicyFile(t, filepath.Join(repoRoot, ".github/workflows/ci.yml"))
	native := readPolicyFile(t, filepath.Join(repoRoot, ".github/workflows/libghostty-native.yml"))

	build := mustMatchString(t, ci, `(?ms)^  build:\n.*?(?:\n  [a-z][\w-]+:|\z)`)
	assertContains(t, build, "Verify untagged fail-closed binaries")

	for _, target := range []string{"darwin/arm64", "linux/amd64", "linux/arm64"} {
		assertContains(t, build, target)
	}

	assertContains(t, build, "CGO_ENABLED=0 go build")
	assertContains(t, build, "verify-default-binary")
	assertNotRegexp(t, native, `default-builds:`)
}

func TestLibghosttyNativePathRouting(t *testing.T) {
	repoRoot := p11RepoRoot()
	native := readPolicyFile(t, filepath.Join(repoRoot, ".github/workflows/libghostty-native.yml"))

	matcher := nativePathMatcher(t, native)
	tests := map[string]bool{
		"website/content/docs/troubleshooting.md":             false,
		"docs/design/2026-07-18-libghostty-daemon-backend.md": false,
		"internal/pty/terminal_backend_ghostty.go":            true,
		"internal/integration/daemon_test.go":                 true,
		"libghostty-native.lock.json":                         true,
		"go.sum":                                              true,
	}

	for path, want := range tests {
		if got := matcher.MatchString(path); got != want {
			t.Fatalf("native path matcher for %s = %t, want %t", path, got, want)
		}
	}

	failure := mustMatchString(t, native, `(?ms)if ! files="\$\(gh api.*?\n\s+fi`)
	assertContains(t, failure, "pulls/$PR/files")
	assertContains(t, failure, `echo "native=true" >> "$GITHUB_OUTPUT"`)
	assertContains(t, failure, `echo "dependency-unit=true" >> "$GITHUB_OUTPUT"`)

	lock := mustMatchString(t, native, `(?ms)if grep -Fxq 'libghostty-native\.lock\.json'.*?\n\s+fi`)
	assertContains(t, lock, `echo "dependency-unit=true" >> "$GITHUB_OUTPUT"`)
}

func TestLibghosttyReleaseRoutingAndUpgradeFixture(t *testing.T) {
	repoRoot := p11RepoRoot()
	goreleaser := readPolicyFile(t, filepath.Join(repoRoot, ".github/workflows/goreleaser.yml"))
	devRelease := readPolicyFile(t, filepath.Join(repoRoot, ".github/workflows/dev-release.yml"))

	if got := strings.Count(goreleaser, "historical_revision=00a8dc8e5806850b857b291b9a5f19088e80c580"); got != 2 {
		t.Fatalf("historical revision count = %d, want 2", got)
	}

	if got := strings.Count(goreleaser, "GRAITH_LIBGHOSTTY_HISTORICAL_UPGRADE_BINARY"); got != 2 {
		t.Fatalf("historical upgrade env count = %d, want 2", got)
	}

	executeLinux := mustMatchString(t, goreleaser, `(?ms)^  execute-linux:\n.*?(?:\n  [a-z][\w-]+:|\z)`)
	assertRegexp(t, executeLinux, `uses: actions/checkout@[\da-f]+(?s:.*?)fetch-depth: 0`)
	assertContains(t, goreleaser, `test ! -e "dist/graith-historical-pre-removal"`)
	assertNotRegexp(t, goreleaser, `TestLibghosttyCharmToNativeUpgrade|gr-charm`)

	devMatcher := releasePathMatcher(t, devRelease)
	stableMatcher := releasePathMatcher(t, goreleaser)

	for _, path := range []string{"internal/pty/terminal_backend_ghostty.go", "go.mod", "website/content/docs/installation.md"} {
		if devMatcher.MatchString(path) || stableMatcher.MatchString(path) {
			t.Fatalf("release matcher unexpectedly selected non-release path %s", path)
		}
	}

	for _, path := range []string{"scripts/dev-release-version.sh", "macos/notifier/build.sh"} {
		if !devMatcher.MatchString(path) {
			t.Fatalf("dev release matcher did not select %s", path)
		}
	}

	for _, path := range []string{".release-please-config.json", "CHANGELOG.md", "scripts/render-stable-aur.sh", "scripts/rpm-preset-keygrips.sh", "scripts/publish-linux-repositories.sh"} {
		if !stableMatcher.MatchString(path) {
			t.Fatalf("stable release matcher did not select %s", path)
		}
	}

	for name, workflow := range map[string]string{"dev-release": devRelease, "goreleaser": goreleaser} {
		assertRegexp(t, workflow, `(?ms)if \[ "\$EVENT" != "pull_request" \]; then.*?echo "release=true"`)
		assertRegexp(t, workflow, `(?ms)if ! files="\$\(gh api "repos/\$REPO/pulls/\$PR/files".*?echo "release=true"`)
		assertRegexp(t, workflow, `(?ms)release-context:.*?needs: changes`)
		assertRegexp(t, workflow, `(?ms)release-context:.*?needs\.changes\.outputs\.release == 'true'`)

		if name == "dev-release" {
			assertRegexp(t, workflow, `branches:\n      - main`)
		} else {
			assertRegexp(t, workflow, `tags:\n      - "v\*"`)
		}
	}
}

func TestLibghosttyLocalNativeBuildIsolation(t *testing.T) {
	repoRoot := p11RepoRoot()
	nativeScript := readPolicyFile(t, filepath.Join(repoRoot, "scripts/libghostty-native.sh"))

	buildLocal := mustMatchString(t, nativeScript, `(?ms)build_local\(\) \{.*?\n\}`)
	assertContains(t, buildLocal, `pkgconfig="$(write_pkg_config`)
	assertContains(t, buildLocal, `gocache="$NATIVE_WORK/go-cache"`)
	assertRegexp(t, buildLocal, `(?ms)GOCACHE="\$gocache".*?PKG_CONFIG_PATH="\$pkgconfig`)
	assertNotContains(t, buildLocal, "go clean -cache")
}

func TestLibghosttyLinuxArtifactsRemainTrustedAndLockComplete(t *testing.T) {
	repoRoot := p11RepoRoot()
	native := readPolicyFile(t, filepath.Join(repoRoot, ".github/workflows/libghostty-native.yml"))
	nativePublish := readPolicyFile(t, filepath.Join(repoRoot, ".github/workflows/libghostty-native-publish.yml"))
	nativeScript := readPolicyFile(t, filepath.Join(repoRoot, "scripts/libghostty-native.sh"))

	var lock struct {
		Ghostty struct {
			LinuxArtifacts map[string]struct {
				URL    string `json:"url"`
				SHA256 string `json:"sha256"`
			} `json:"linuxArtifacts"`
		} `json:"ghostty"`
	}
	if err := json.Unmarshal([]byte(readPolicyFile(t, filepath.Join(repoRoot, "libghostty-native.lock.json"))), &lock); err != nil {
		t.Fatal(err)
	}

	for _, arch := range []string{"amd64", "arm64"} {
		artifact, ok := lock.Ghostty.LinuxArtifacts[arch]
		if !ok {
			t.Fatalf("lock is missing linux artifact for %s", arch)
		}

		assertRegexp(t, artifact.URL, `libghostty-vt-linux-`+arch+`\.tar\.gz$`)
		assertRegexp(t, artifact.SHA256, `^[0-9a-f]{64}$`)

		if len(uniqueRunes(artifact.SHA256)) == 1 {
			t.Fatalf("%s artifact digest must not be a repeated-character placeholder", arch)
		}
	}

	assertContains(t, nativeScript, `sha256_check "$expected" "$archive"`)
	assertRegexp(t, nativeScript, `(?ms)sha256_check.*?tar -xzf`)
	assertContains(t, nativeScript, "unexpected or incomplete archive members")
	assertContains(t, nativePublish, "contents: write")
	assertContains(t, nativePublish, "github.event_name == 'workflow_dispatch' || github.ref == 'refs/heads/main'")
	assertContains(t, nativePublish, "verified immutable asset already published")
	assertRegexp(t, nativePublish, `(?ms)remote_asset_sha.*?expected_asset_sha`)
	assertContains(t, nativePublish, `export PATH="$RUNNER_TEMP/zig:$PATH"`)
	assertContains(t, nativePublish, `test "$(zig version)" = "$(jq -er '.zig.version' libghostty-native.lock.json)"`)
	assertContains(t, nativePublish, `env GOARCH=amd64 scripts/libghostty-native.sh source-build`)
	assertContains(t, nativePublish, `Cflags: -I\${prefix}/include -DGHOSTTY_STATIC`)
	assertContains(t, nativePublish, `sed "1i prefix=\${pcfiledir}/.."`)
	assertContains(t, nativePublish, "pkg-config --cflags libghostty-vt-static")
	assertContains(t, nativePublish, "cp -R gui/shared/Sources/CGhosttyVT/include")
	assertLibghosttyArchiveHelperPolicy(t, nativeScript, nativePublish)
	assertContains(t, nativeScript, "test-linux-archive-policy")
	assertContains(t, nativePublish, `prefix=\${pcfiledir}/..`)
	assertContains(t, nativePublish, `Libs: -L\${prefix} -lghostty-vt`)
	assertContains(t, nativePublish, "linuxArtifacts.amd64.url")
	assertContains(t, nativePublish, "capture(")
	assertContains(t, native, "test-linux-artifact")
	assertNotContains(t, native, `CGO_CFLAGS: -I${{ github.workspace }}/gui/shared`)
	assertContains(t, nativeScript, "pkg-config --cflags libghostty-vt-static")
	assertContains(t, nativeScript, "unexpected include path")
	assertRegexp(t, nativeScript, `realpath.*include_path`)
	assertRegexp(t, nativeScript, `cflag_tokens\[\@\].*2`)
	assertContains(t, native, "unset CGO_CFLAGS CGO_CPPFLAGS CPATH C_INCLUDE_PATH CPLUS_INCLUDE_PATH")
	assertContains(t, native, `source-build "$TARGET" "$LIBRARY"`)

	contractStep := mustMatchString(t, native, `(?ms)name: Contract-test the published Linux artifact.*?(?:\n      - name:|\z)`)
	assertNotContains(t, contractStep, "if: needs.changes.outputs.dependency-unit")
	assertRegexp(t, nativePublish, `(?ms)gh release create.*?gh release view`)
	assertRegexp(t, nativePublish, `actions/checkout@[0-9a-f]{40}`)
}

func TestLibghosttyLinuxArtifactPolicyRequiresArchiveHelperChecks(t *testing.T) {
	repoRoot := p11RepoRoot()
	nativePublish := readPolicyFile(t, filepath.Join(repoRoot, ".github/workflows/libghostty-native-publish.yml"))
	nativeScript := readPolicyFile(t, filepath.Join(repoRoot, "scripts/libghostty-native.sh"))

	tests := map[string]struct {
		nativeScript  string
		nativePublish string
		want          string
	}{
		"missing_publish_build": {
			nativeScript:  nativeScript,
			nativePublish: strings.Replace(nativePublish, `env -u GOOS -u GOARCH go build -o "$RUNNER_TEMP/libghosttyarchive" ./cmd/libghosttyarchive`, `go build -o "$RUNNER_TEMP/other" ./cmd/other`, 1),
			want:          "publish workflow must build the Go archive helper for the host",
		},
		"missing_publish_pack": {
			nativeScript:  nativeScript,
			nativePublish: strings.Replace(nativePublish, `"$RUNNER_TEMP/libghosttyarchive" pack`, `"$RUNNER_TEMP/other" pack`, 1),
			want:          "publish workflow must pack Linux artifacts with the Go archive helper",
		},
		"missing_publish_inspect": {
			nativeScript:  nativeScript,
			nativePublish: strings.Replace(nativePublish, `"$RUNNER_TEMP/libghosttyarchive" inspect`, `"$RUNNER_TEMP/other" inspect`, 1),
			want:          "publish workflow must inspect Linux artifacts with the Go archive helper",
		},
		"missing_consumer_build": {
			nativeScript:  strings.Replace(nativeScript, `env -u GOOS -u GOARCH go build -o "$helper" ./cmd/libghosttyarchive`, `go build -o "$helper" ./cmd/other`, 1),
			nativePublish: nativePublish,
			want:          "native consumer must build the Go archive helper for the host",
		},
		"missing_consumer_inspect": {
			nativeScript:  strings.Replace(nativeScript, `libghostty_archive_helper inspect "$archive"`, `other_archive_helper inspect "$archive"`, 1),
			nativePublish: nativePublish,
			want:          "native consumer must inspect Linux artifacts with the Go archive helper",
		},
		"go_run_caller": {
			nativeScript:  nativeScript + "\ngo run ./cmd/libghosttyarchive test\n",
			nativePublish: nativePublish,
			want:          "archive helper callers must use the built command, not go run",
		},
		"retired_python_caller": {
			nativeScript:  nativeScript + "\npython3 \"$REPO_DIR/scripts/libghostty-linux-archive.py\" test\n",
			nativePublish: nativePublish,
			want:          "retired Python archive helper must not be called",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			err := checkLibghosttyArchiveHelperPolicy(test.nativeScript, test.nativePublish)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("checkLibghosttyArchiveHelperPolicy() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestLibghosttyCoverageMeasuresTaggedProductionGraph(t *testing.T) {
	repoRoot := p11RepoRoot()
	coverage := readPolicyFile(t, filepath.Join(repoRoot, ".github/workflows/coverage.yml"))

	assertContains(t, coverage, "prepare-linux-artifact amd64")

	if got := strings.Count(coverage, "prepare-linux-artifact amd64"); got != 1 {
		t.Fatalf("prepare-linux-artifact amd64 count = %d, want 1", got)
	}

	assertContains(t, coverage, `go test -tags=libghostty -coverprofile="$profile" ./...`)
	assertContains(t, coverage, "run_cover cover.head.out head")
	assertContains(t, coverage, "run_cover cover.base.out base")
	assertContains(t, coverage, "HEAD and BASE use the lock and setup script")
}

func assertLibghosttyArchiveHelperPolicy(t *testing.T, nativeScript, nativePublish string) {
	t.Helper()

	if err := checkLibghosttyArchiveHelperPolicy(nativeScript, nativePublish); err != nil {
		t.Fatal(err)
	}
}

//nolint:wsl_v5 // The ordered checks mirror producer then consumer archive-helper obligations.
func checkLibghosttyArchiveHelperPolicy(nativeScript, nativePublish string) error {
	if !strings.Contains(nativePublish, `env -u GOOS -u GOARCH go build -o "$RUNNER_TEMP/libghosttyarchive" ./cmd/libghosttyarchive`) {
		return errors.New("publish workflow must build the Go archive helper for the host")
	}
	if !strings.Contains(nativePublish, `"$RUNNER_TEMP/libghosttyarchive" pack`) {
		return errors.New("publish workflow must pack Linux artifacts with the Go archive helper")
	}
	if !strings.Contains(nativePublish, `"$RUNNER_TEMP/libghosttyarchive" inspect`) {
		return errors.New("publish workflow must inspect Linux artifacts with the Go archive helper")
	}
	if !regexp.MustCompile(`env -u GOOS -u GOARCH go build -o "\$helper" \./cmd/libghosttyarchive`).MatchString(nativeScript) {
		return errors.New("native consumer must build the Go archive helper for the host")
	}
	if !regexp.MustCompile(`libghostty_archive_helper inspect "\$archive"`).MatchString(nativeScript) {
		return errors.New("native consumer must inspect Linux artifacts with the Go archive helper")
	}
	if !strings.Contains(nativeScript, `libghostty_archive_helper test`) {
		return errors.New("native archive policy test must use the Go archive helper")
	}
	if strings.Contains(nativeScript, "go run ./cmd/libghosttyarchive") || strings.Contains(nativePublish, "go run ./cmd/libghosttyarchive") {
		return errors.New("archive helper callers must use the built command, not go run")
	}
	if strings.Contains(nativeScript, "libghostty-linux-archive.py") || strings.Contains(nativePublish, "libghostty-linux-archive.py") {
		return errors.New("retired Python archive helper must not be called")
	}

	return nil
}

func nativePathMatcher(t *testing.T, workflow string) *regexp.Regexp {
	t.Helper()

	pattern := mustSubmatch(t, workflow, `if grep -Eq '([^']+)' <<<"\$files"`)

	matcher, err := regexp.Compile(pattern)
	if err != nil {
		t.Fatal(err)
	}

	return matcher
}

func releasePathMatcher(t *testing.T, workflow string) *regexp.Regexp {
	t.Helper()

	pattern := mustSubmatch(t, workflow, `if grep -Eq '([^']+)' <<<"\$files"`)

	matcher, err := regexp.Compile(pattern)
	if err != nil {
		t.Fatal(err)
	}

	return matcher
}

func mustMatchString(t *testing.T, value, pattern string) string {
	t.Helper()

	match := regexp.MustCompile(pattern).FindString(value)
	if match == "" {
		t.Fatalf("pattern %q not found", pattern)
	}

	return match
}

func mustSubmatch(t *testing.T, value, pattern string) string {
	t.Helper()

	matches := regexp.MustCompile(pattern).FindStringSubmatch(value)
	if len(matches) < 2 {
		t.Fatalf("pattern %q did not capture", pattern)
	}

	return matches[1]
}

func assertRegexp(t *testing.T, value, pattern string) {
	t.Helper()

	if !regexp.MustCompile(pattern).MatchString(value) {
		t.Fatalf("value does not match %q:\n%s", pattern, value)
	}
}

func assertNotRegexp(t *testing.T, value, pattern string) {
	t.Helper()

	if regexp.MustCompile(pattern).MatchString(value) {
		t.Fatalf("value unexpectedly matches %q:\n%s", pattern, value)
	}
}

func assertNotContains(t *testing.T, value, needle string) {
	t.Helper()

	if strings.Contains(value, needle) {
		t.Fatalf("value unexpectedly contains %q:\n%s", needle, value)
	}
}

func uniqueRunes(value string) map[rune]bool {
	result := map[rune]bool{}
	for _, r := range value {
		result[r] = true
	}

	return result
}
