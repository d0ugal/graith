package ciworkflow

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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

	tests := map[string]bool{
		"website/content/docs/troubleshooting.md":             false,
		"docs/design/2026-07-18-libghostty-daemon-backend.md": false,
		"internal/pty/terminal_backend_ghostty.go":            true,
		"internal/integration/daemon_test.go":                 true,
		"libghostty-native.lock.json":                         true,
		"go.sum":                                              true,
	}

	for path, want := range tests {
		classification, err := ClassifyWorkflowPaths([]string{path})
		if err != nil {
			t.Fatal(err)
		}

		if got := classification.LibghosttyNative; got != want {
			t.Fatalf("native path matcher for %s = %t, want %t", path, got, want)
		}
	}

	failure := mustMatchString(t, native, `(?ms)if ! files="\$\(gh api.*?\n\s+fi`)
	assertContains(t, failure, "pulls/$PR/files")
	assertContains(t, failure, `echo "native=true" >> "$GITHUB_OUTPUT"`)
	assertContains(t, failure, `echo "dependency-unit=true" >> "$GITHUB_OUTPUT"`)

	classifierFailure := mustMatchString(t, native, `(?ms)if ! classification="\$\(go run \./cmd/ciclassify -mode libghostty.*?\n\s+fi`)
	assertContains(t, classifierFailure, `echo "native=true" >> "$GITHUB_OUTPUT"`)
	assertContains(t, classifierFailure, `echo "dependency-unit=true" >> "$GITHUB_OUTPUT"`)
	assertContains(t, native, "Native dependency lock changed: running update-only race/fuzz gates.")
	assertContains(t, native, "go run ./cmd/ciclassify -mode libghostty")

	workflow, err := ReadP11WorkflowSummary(filepath.Join(repoRoot, ".github/workflows/libghostty-native.yml"))
	if err != nil {
		t.Fatal(err)
	}

	gate := p11WorkflowStep(t, p11WorkflowJob(t, workflow, "native-gate"), "Require relevant native jobs to pass").Run
	assertContains(t, gate, `if [ "$CHANGES_RESULT" != "success" ]; then
  echo "Change detection did not succeed ($CHANGES_RESULT); requiring all native jobs."
  NATIVE_RELEVANT=true
fi`)
	assertNotContains(t, gate, `echo "Change detection did not succeed: $CHANGES_RESULT" >&2
  exit 1`)
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

	stableMatcher := releasePathMatcher(t, goreleaser)

	for _, path := range []string{"internal/pty/terminal_backend_ghostty.go", "go.mod", "website/content/docs/installation.md"} {
		if sharedClassifierSelectsDevRelease(path) || stableMatcher.MatchString(path) {
			t.Fatalf("release matcher unexpectedly selected non-release path %s", path)
		}
	}

	for _, path := range []string{
		".github/workflows/dev-release.yml",
		".goreleaser-dev.yaml",
		"THIRD_PARTY_NOTICES.libghostty.md",
		"libghostty-native.lock.json",
		"libghostty-native.spdx.json",
		"scripts/dev-release-base-tag.sh",
		"scripts/dev-release-version.sh",
		"scripts/libghostty-native.sh",
		"cmd/ciclassify/main.go",
		"internal/ciworkflow/workflow_classifier.go",
		"macos/notifier/build.sh",
		"macos/service/release-signing-mode.sh",
		"internal/daemonservice/service_manifest.json",
	} {
		if !sharedClassifierSelectsDevRelease(path) {
			t.Fatalf("shared classifier did not select dev release for %s", path)
		}
	}

	for _, path := range []string{".release-please-config.json", "CHANGELOG.md", "scripts/render-stable-aur.sh", "scripts/rpm-preset-keygrips.sh", "scripts/publish-linux-repositories.sh"} {
		if !stableMatcher.MatchString(path) {
			t.Fatalf("stable release matcher did not select %s", path)
		}
	}

	devReleaseFilter := mustMatchString(t, devRelease, `(?ms)- id: filter\n.*?(?:\n\n  release-context:)`)

	assertRegexp(t, devRelease, `(?ms)if \[ "\$EVENT" != "pull_request" \]; then.*?echo "release=true"`)
	assertContains(t, devRelease, "go run ./cmd/ciclassify")
	assertContains(t, devRelease, "-mode dev-release")
	assertContains(t, devRelease, `-github-output "$GITHUB_OUTPUT"`)
	assertContains(t, devRelease, `ref: ${{ github.event.pull_request.base.sha }}`)
	assertContains(t, devRelease, `persist-credentials: false`)
	assertContains(t, devRelease, `pulls/$PR/files?per_page=100`)
	assertContains(t, devRelease, `Could not list PR files; running dev release to be safe.`)
	assertContains(t, devRelease, `PR file list is incomplete; running dev release to be safe.`)
	assertRegexp(t, devRelease, `(?ms)if ! go run \./cmd/ciclassify.*?echo "release=true" >> "\$GITHUB_OUTPUT"`)
	assertNotContains(t, devRelease, `go run ./cmd/cipolicy`)
	assertNotContains(t, devReleaseFilter, `jq -er '`)
	assertNotContains(t, devReleaseFilter, `dev-release-plan.json`)
	assertRegexp(t, devRelease, `(?ms)release-context:.*?needs: changes`)
	assertRegexp(t, devRelease, `(?ms)release-context:.*?needs\.changes\.outputs\.release == 'true'`)
	assertRegexp(t, devRelease, `branches:\n      - main`)

	assertRegexp(t, goreleaser, `(?ms)if \[ "\$EVENT" != "pull_request" \]; then.*?echo "release=true"`)
	assertRegexp(t, goreleaser, `(?ms)if ! files="\$\(gh api "repos/\$REPO/pulls/\$PR/files".*?echo "release=true"`)
	assertRegexp(t, goreleaser, `(?ms)release-context:.*?needs: changes`)
	assertRegexp(t, goreleaser, `(?ms)release-context:.*?needs\.changes\.outputs\.release == 'true'`)
	assertRegexp(t, goreleaser, `tags:\n      - "v\*"`)
}

func TestDevReleaseHomebrewTapCredentialsStayOutOfGitURLs(t *testing.T) {
	repoRoot := p11RepoRoot()

	workflow, err := ReadP11WorkflowSummary(filepath.Join(repoRoot, ".github/workflows/dev-release.yml"))
	if err != nil {
		t.Fatal(err)
	}

	publish := p11WorkflowJob(t, workflow, "publish-dev")
	renderFormula := p11WorkflowStep(t, publish, "Render Homebrew tap formula")
	updateTap := p11WorkflowStep(t, publish, "Update Homebrew tap")

	if p11MapHasReleaseTokenExpression(renderFormula.Env) || strings.Contains(renderFormula.Run, "RELEASE_TOKEN") {
		t.Fatal("Homebrew formula rendering must not receive the tap publication token")
	}

	if got := p11WorkflowReleaseTokenExpressionCount(workflow); got != 1 {
		t.Fatalf("dev-release RELEASE_TOKEN structural references = %d, want update tap env only", got)
	}

	if updateTap.Env["RELEASE_TOKEN"] != "${{ secrets.RELEASE_TOKEN }}" || len(updateTap.Env) != 1 {
		t.Fatalf("update tap env = %#v, want only RELEASE_TOKEN", updateTap.Env)
	}

	for _, scalar := range workflow.Scalars {
		assertNotRegexp(t, scalar, `https://[^[:space:]'"]*@github\.com`)
	}

	if p11WorkflowRunLineContainsAll(workflow, "git clone", "RELEASE_TOKEN") ||
		p11WorkflowRunLineContainsReleaseTokenExpression(workflow, "git clone") {
		t.Fatal("dev-release must not put RELEASE_TOKEN in a git clone command line")
	}

	assertContains(t, updateTap.Run, `tap_token="${RELEASE_TOKEN:?}"`)
	assertContains(t, updateTap.Run, `unset RELEASE_TOKEN`)
	assertContains(t, updateTap.Run, `RELEASE_TOKEN="$tap_token" GIT_ASKPASS="$askpass" GIT_TERMINAL_PROMPT=0 git -c credential.helper= "$@"`)
	assertContains(t, updateTap.Run, `git_with_tap_credentials clone "https://github.com/d0ugal/homebrew-tap.git" .`)
	assertContains(t, updateTap.Run, `remote_url="$(git remote get-url origin)"`)
	assertContains(t, updateTap.Run, `test "$remote_url" = "https://github.com/d0ugal/homebrew-tap.git"`)
	assertContains(t, updateTap.Run, `git_with_tap_credentials push`)
	assertContains(t, updateTap.Run, `unset GIT_ASKPASS GIT_TERMINAL_PROMPT RELEASE_TOKEN tap_token`)
	assertNotContains(t, updateTap.Run, `git remote set-url`)
	assertNotContains(t, updateTap.Run, `export GIT_ASKPASS`)
	assertNotContains(t, updateTap.Run, `credential.helper store`)
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

func TestLibghosttyNativeArtifactWorkflowPublishesBeforeGeneration(t *testing.T) {
	repoRoot := p11RepoRoot()
	workflowPath := filepath.Join(repoRoot, ".github/workflows/libghostty-native-artifacts.yml")
	nativeArtifacts := readPolicyFile(t, workflowPath)

	workflow, err := ReadP11WorkflowSummary(workflowPath)
	if err != nil {
		t.Fatal(err)
	}

	if got := p11WorkflowJobIDs(workflow); strings.Join(got, ",") != "apple-build,apple-publish,authorize,generate,linux-build,linux-publish,push,validate" {
		t.Fatalf("artifact workflow jobs = %#v", got)
	}

	assertContains(t, nativeArtifacts, "pull_request_target")
	assertContains(t, nativeArtifacts, "zizmor: ignore[dangerous-triggers]")
	assertContains(t, nativeArtifacts, "RELEASE_TOKEN is isolated in a no-code push job")
	assertContains(t, nativeArtifacts, "native-artifact-approved")
	assertContains(t, nativeArtifacts, "libghostty-native.lock.json")
	assertContains(t, nativeArtifacts, "same-repository PR branches")
	assertContains(t, nativeArtifacts, "reviewing this exact native dependency head SHA")
	assertContains(t, nativeArtifacts, "lock-only dependency PRs")
	assertContains(t, nativeArtifacts, "prepare-artifact-lock")
	assertContains(t, nativeArtifacts, "generate-artifact-inputs")
	assertContains(t, nativeArtifacts, "generate-dependency-unit")
	assertContains(t, nativeArtifacts, "libghostty-vt-${ghostty_commit:0:7}-go-${go_libghostty_commit:0:7}-zig-$zig_tag")
	assertContains(t, nativeArtifacts, "libghostty-vt-${ghostty_commit:0:7}-go-${go_libghostty_commit:0:7}-zig-$zig_tag-linux")

	if !reflect.DeepEqual(workflow.Events, []string{"pull_request_target"}) {
		t.Fatalf("artifact workflow events = %#v, want pull_request_target", workflow.Events)
	}

	if !reflect.DeepEqual(workflow.Permissions, map[string]string{"contents": "read", "pull-requests": "read"}) {
		t.Fatalf("artifact workflow permissions = %#v", workflow.Permissions)
	}

	authorize := p11WorkflowJob(t, workflow, "authorize")
	validate := p11WorkflowJob(t, workflow, "validate")
	appleBuildJob := p11WorkflowJob(t, workflow, "apple-build")
	applePublishJob := p11WorkflowJob(t, workflow, "apple-publish")
	linuxBuildJob := p11WorkflowJob(t, workflow, "linux-build")
	linuxPublishJob := p11WorkflowJob(t, workflow, "linux-publish")
	generate := p11WorkflowJob(t, workflow, "generate")
	push := p11WorkflowJob(t, workflow, "push")

	assertStringsEqual(t, "apple-publish needs", applePublishJob.Needs, []string{"authorize", "apple-build"})
	assertStringsEqual(t, "linux-build needs", linuxBuildJob.Needs, []string{"authorize", "apple-publish"})
	assertStringsEqual(t, "linux-publish needs", linuxPublishJob.Needs, []string{"authorize", "apple-build", "linux-build"})

	if p11JobUsesAction(authorize, "actions/checkout") || p11JobRunsRepositoryControlledCode(authorize) {
		t.Fatal("artifact authorization job must not check out or run repository-controlled code")
	}

	authorizeStep := p11WorkflowStep(t, authorize, "Require same-repository lock-only approved PR")
	assertContains(t, authorizeStep.Run, `[ "$EVENT_ACTION" != "labeled" ] || [ "$EVENT_LABEL" != "$APPROVAL_LABEL" ]`)
	assertContains(t, authorizeStep.Run, `repos/$REPOSITORY/compare/$BASE_SHA...$HEAD_SHA`)
	assertNotContains(t, authorizeStep.Run, `pulls/$PR_NUMBER/files`)

	if p11JobUsesAction(validate, "actions/checkout") || p11JobRunsRepositoryControlledCode(validate) {
		t.Fatal("artifact credential validation job must not check out or run repository-controlled code")
	}

	if p11JobUsesAction(applePublishJob, "actions/checkout") ||
		p11JobUsesAction(linuxPublishJob, "actions/checkout") ||
		p11JobRunsRepositoryControlledCode(applePublishJob) ||
		p11JobRunsRepositoryControlledCode(linuxPublishJob) {
		t.Fatal("artifact publisher jobs must not check out or run repository-controlled code")
	}

	if p11JobRunsRepositoryControlledCode(push) {
		t.Fatal("artifact push job must not run repository-controlled code")
	}

	if got := p11WorkflowReleaseTokenExpressionCount(workflow); got != 2 {
		t.Fatalf("artifact workflow RELEASE_TOKEN structural references = %d, want validation env and push checkout only", got)
	}

	for id, job := range map[string]P11WorkflowJob{
		"apple-build":   appleBuildJob,
		"apple-publish": applePublishJob,
		"linux-build":   linuxBuildJob,
		"linux-publish": linuxPublishJob,
		"generate":      generate,
	} {
		if p11JobHasReleaseTokenExpression(job) {
			t.Fatalf("%s job must not receive RELEASE_TOKEN", id)
		}

		if p11JobCheckoutPersistsCredentials(job) {
			t.Fatalf("%s job must not persist checkout credentials", id)
		}
	}

	for id, job := range map[string]P11WorkflowJob{
		"apple-build": appleBuildJob,
		"linux-build": linuxBuildJob,
	} {
		if job.Permissions["contents"] != "read" || job.Permissions["pull-requests"] != "read" {
			t.Fatalf("%s permissions = %#v, want read-only build permissions", id, job.Permissions)
		}
	}

	for id, job := range map[string]P11WorkflowJob{
		"apple-publish": applePublishJob,
		"linux-publish": linuxPublishJob,
	} {
		if job.Permissions["contents"] != "write" || job.Permissions["pull-requests"] != "read" {
			t.Fatalf("%s permissions = %#v, want release write plus PR read", id, job.Permissions)
		}
	}

	for id, job := range map[string]P11WorkflowJob{
		"apple-build": appleBuildJob,
		"linux-build": linuxBuildJob,
		"generate":    generate,
	} {
		checkout := p11WorkflowStep(t, job, "Check out trusted base")
		if checkout.With["ref"] != "${{ github.event.pull_request.base.sha }}" ||
			checkout.With["persist-credentials"] != "false" {
			t.Fatalf("%s trusted checkout = %#v", id, checkout.With)
		}

		overlay := p11WorkflowStep(t, job, "Overlay reviewed PR lock")
		assertContains(t, overlay.Run, "application/vnd.github.raw")
		assertContains(t, overlay.Run, "contents/libghostty-native.lock.json?ref=$SOURCE_SHA")
	}

	appleCache := p11WorkflowStep(t, appleBuildJob, "Check for existing Apple artifact")
	assertContains(t, appleCache.Run, "verified immutable Apple artifact already published")
	assertContains(t, appleCache.Run, "go-libghostty commit: $go_libghostty_commit")
	assertContains(t, appleCache.Run, "Zig version: $zig_version")

	appleBuild := p11WorkflowStep(t, appleBuildJob, "Build Apple artifact")
	if _, ok := appleBuild.Env["GH_TOKEN"]; ok {
		t.Fatal("Apple build step must not receive GH_TOKEN")
	}

	assertContains(t, appleBuild.Run, "swift package compute-checksum")
	appleLockUpload := p11WorkflowStep(t, appleBuildJob, "Upload materialized artifact lock")
	assertContains(t, appleLockUpload.Uses, "actions/upload-artifact")

	if appleLockUpload.If != "" {
		t.Fatalf("materialized lock upload must be unconditional, got if %q", appleLockUpload.If)
	}

	if got := appleLockUpload.With["name"]; got != "libghostty-native-lock-${{ github.run_id }}" {
		t.Fatalf("materialized lock artifact name = %q", got)
	}

	if got := appleLockUpload.With["path"]; got != "libghostty-native.lock.json" {
		t.Fatalf("materialized lock artifact path = %q", got)
	}

	if got := appleLockUpload.With["if-no-files-found"]; got != "error" {
		t.Fatalf("materialized lock if-no-files-found = %q", got)
	}

	if got := appleLockUpload.With["overwrite"]; got != "true" {
		t.Fatalf("materialized lock overwrite = %q, want retry-safe upload", got)
	}

	if got := appleLockUpload.With["retention-days"]; got != "2" {
		t.Fatalf("materialized lock retention-days = %q", got)
	}

	materializeIndex := p11WorkflowStepIndex(t, appleBuildJob, "Materialize artifact lock")

	uploadLockIndex := p11WorkflowStepIndex(t, appleBuildJob, "Upload materialized artifact lock")
	if uploadLockIndex <= materializeIndex {
		t.Fatalf("materialized lock upload step index = %d, want after materialization index %d", uploadLockIndex, materializeIndex)
	}

	assertContains(t, p11WorkflowStep(t, appleBuildJob, "Upload built Apple artifact").Uses, "actions/upload-artifact")

	appleLockDownload := p11WorkflowStep(t, applePublishJob, "Download materialized artifact lock")
	assertContains(t, appleLockDownload.Uses, "actions/download-artifact")

	if got := appleLockDownload.With["name"]; got != "libghostty-native-lock-${{ github.run_id }}" {
		t.Fatalf("Apple publisher materialized lock artifact name = %q", got)
	}

	if got := appleLockDownload.With["path"]; got != "${{ runner.temp }}/artifact-lock" {
		t.Fatalf("Apple publisher materialized lock path = %q", got)
	}

	applePublish := p11WorkflowStep(t, applePublishJob, "Publish Apple artifact")
	assertContains(t, applePublish.Run, "gh release upload")
	assertContains(t, applePublish.Run, "Published Apple artifact digest")
	assertContains(t, p11WorkflowStep(t, applePublishJob, "Download built Apple artifact").Uses, "actions/download-artifact")

	linuxBuild := p11WorkflowStep(t, linuxBuildJob, "Build and pack Linux artifact")
	assertContains(t, linuxBuild.Env["GRAITH_LIBGHOSTTY_ARTIFACT_INPUTS"], "1")
	assertContains(t, linuxBuild.Run, `"$RUNNER_TEMP/libghosttyarchive" pack`)
	assertContains(t, p11WorkflowStep(t, linuxBuildJob, "Check for existing Linux artifact").Run, "verified immutable Linux artifact already published")
	assertContains(t, p11WorkflowStep(t, linuxBuildJob, "Upload built Linux artifact").Uses, "actions/upload-artifact")
	linuxLockDownload := p11WorkflowStep(t, linuxPublishJob, "Download materialized artifact lock")
	assertContains(t, linuxLockDownload.Uses, "actions/download-artifact")

	if got := linuxLockDownload.With["name"]; got != "libghostty-native-lock-${{ github.run_id }}" {
		t.Fatalf("Linux publisher materialized lock artifact name = %q", got)
	}

	if got := linuxLockDownload.With["path"]; got != "${{ runner.temp }}/artifact-lock" {
		t.Fatalf("Linux publisher materialized lock path = %q", got)
	}

	assertContains(t, p11WorkflowStep(t, linuxPublishJob, "Download built Linux artifact").Uses, "actions/download-artifact")
	assertContains(t, p11WorkflowStep(t, linuxPublishJob, "Publish only absent immutable Linux asset").Run, "gh release upload")
	assertContains(t, p11WorkflowStep(t, linuxPublishJob, "Publish only absent immutable Linux asset").Run, "Published Linux artifact digest")

	for name, step := range map[string]P11WorkflowStep{
		"apple publish cache": p11WorkflowStep(t, applePublishJob, "Check for existing Apple artifact"),
		"apple publish":       applePublish,
		"linux publish cache": p11WorkflowStep(t, linuxPublishJob, "Check for existing Linux artifact"),
		"linux publish":       p11WorkflowStep(t, linuxPublishJob, "Publish only absent immutable Linux asset"),
	} {
		assertContains(t, step.Run, "$RUNNER_TEMP/artifact-lock/libghostty-native.lock.json")
		assertNotContains(t, step.Run, "contents/libghostty-native.lock.json")
		assertNotContains(t, step.Run, "application/vnd.github.raw")

		if _, ok := step.Env["SOURCE_SHA"]; ok {
			t.Fatalf("%s step must not fetch the raw PR lock after materialization", name)
		}
	}

	for name, step := range map[string]P11WorkflowStep{
		"apple build cache":   appleCache,
		"apple publish cache": p11WorkflowStep(t, applePublishJob, "Check for existing Apple artifact"),
		"apple publish":       applePublish,
		"linux build cache":   p11WorkflowStep(t, linuxBuildJob, "Check for existing Linux artifact"),
		"linux publish cache": p11WorkflowStep(t, linuxPublishJob, "Check for existing Linux artifact"),
		"linux publish":       p11WorkflowStep(t, linuxPublishJob, "Publish only absent immutable Linux asset"),
	} {
		if step.Env["REPOSITORY"] != "${{ github.repository }}" {
			t.Fatalf("%s step REPOSITORY env = %q, want github.repository", name, step.Env["REPOSITORY"])
		}

		assertGhReleaseCommandsUseExplicitRepo(t, name, step.Run)
	}

	commit := p11WorkflowStep(t, generate, "Commit generated native files if changed")
	assertContains(t, commit.Run, `git commit-tree "$tree_sha" -p "$SOURCE_SHA"`)
	assertContains(t, commit.Run, `git checkout --detach "$generated_sha"`)
	assertContains(t, commit.Run, `git bundle create "$RUNNER_TEMP/generated-native.bundle" HEAD "^$SOURCE_SHA"`)
	assertContains(t, commit.Run, "Generated native commit contains a non-allowlisted path.")

	pushCheckout := p11WorkflowStep(t, push, "Check out source head with workflow-triggering credentials")
	if pushCheckout.With["token"] != "${{ secrets.RELEASE_TOKEN }}" ||
		pushCheckout.With["persist-credentials"] != "true" {
		t.Fatalf("artifact push checkout = %#v", pushCheckout.With)
	}

	pushStep := p11WorkflowStep(t, push, "Push generated native commit")
	assertContains(t, pushStep.Run, `git ls-remote origin "refs/heads/$HEAD_REF"`)
	assertContains(t, pushStep.Run, `git diff --no-renames --name-only -z "$SOURCE_SHA" "$GENERATED_SHA"`)
	assertContains(t, pushStep.Run, `git push origin "HEAD:$HEAD_REF"`)
	assertNotContains(t, pushStep.Run, "--force")
	assertNotContains(t, pushStep.Run, "${{ github.head_ref }}")
}

func assertGhReleaseCommandsUseExplicitRepo(t *testing.T, stepName, run string) {
	t.Helper()

	for _, line := range strings.Split(run, "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "gh release ") && !strings.Contains(line, `--repo "$REPOSITORY"`) {
			t.Fatalf("%s release command %q must pass --repo \"$REPOSITORY\"", stepName, line)
		}
	}
}

func p11WorkflowStepIndex(t *testing.T, job P11WorkflowJob, name string) int {
	t.Helper()

	for index, step := range job.Steps {
		if step.Name == name {
			return index
		}
	}

	t.Fatalf("step %q not found", name)

	return -1
}

func TestLibghosttyNativeGeneratedCommitBundleRoundTrips(t *testing.T) {
	work := t.TempDir()
	run := func(args ...string) string {
		t.Helper()

		command := exec.Command(args[0], args[1:]...)
		command.Dir = work

		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("%s failed: %v\n%s", strings.Join(args, " "), err, output)
		}

		return strings.TrimSpace(string(output))
	}

	run("git", "init", "-q", "-b", "main")
	run("git", "config", "user.name", "Braw Bot")
	run("git", "config", "user.email", "braw@example.invalid")

	if err := os.WriteFile(filepath.Join(work, "lock"), []byte("canny\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	run("git", "add", "lock")
	run("git", "commit", "-qm", "base")

	if err := os.WriteFile(filepath.Join(work, "lock"), []byte("dreich\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	run("git", "commit", "-qam", "source")
	sourceSHA := run("git", "rev-parse", "HEAD")

	if err := os.WriteFile(filepath.Join(work, "generated"), []byte("blether\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	run("git", "add", "generated")
	treeSHA := run("git", "write-tree")
	generatedSHA := run("git", "commit-tree", treeSHA, "-p", sourceSHA, "-m", "generated")
	run("git", "checkout", "--detach", generatedSHA)

	bundlePath := filepath.Join(work, "generated.bundle")
	run("git", "bundle", "create", bundlePath, "HEAD", "^"+sourceSHA)
	run("git", "checkout", "-q", sourceSHA)
	run("git", "fetch", bundlePath, "HEAD")

	if got := run("git", "rev-parse", "FETCH_HEAD"); got != generatedSHA {
		t.Fatalf("FETCH_HEAD = %s, want generated commit %s", got, generatedSHA)
	}
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

func releasePathMatcher(t *testing.T, workflow string) *regexp.Regexp {
	t.Helper()

	pattern := mustSubmatch(t, workflow, `if grep -Eq '([^']+)' <<<"\$files"`)

	matcher, err := regexp.Compile(pattern)
	if err != nil {
		t.Fatal(err)
	}

	return matcher
}

func sharedClassifierSelectsDevRelease(path string) bool {
	classification, err := ClassifyWorkflowPaths([]string{path})
	if err != nil {
		return false
	}

	return classification.DevRelease
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
