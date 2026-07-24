'use strict';

// Regression tests for the native workflow routing policy. These assertions
// intentionally inspect the workflow text: the policy executes in GitHub's
// shell, so keeping the test next to the workflow catches accidental routing
// changes without introducing a second implementation of the detector.

const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const REPO_ROOT = path.join(__dirname, '..', '..', '..');
const ci = fs.readFileSync(path.join(REPO_ROOT, '.github', 'workflows', 'ci.yml'), 'utf8');
const native = fs.readFileSync(
  path.join(REPO_ROOT, '.github', 'workflows', 'libghostty-native.yml'),
  'utf8',
);
const goreleaser = fs.readFileSync(
  path.join(REPO_ROOT, '.github', 'workflows', 'goreleaser.yml'),
  'utf8',
);
const nativeScript = fs.readFileSync(path.join(REPO_ROOT, 'scripts', 'libghostty-native.sh'), 'utf8');
const devRelease = fs.readFileSync(path.join(REPO_ROOT, '.github', 'workflows', 'dev-release.yml'), 'utf8');
const nativePublish = fs.readFileSync(
  path.join(REPO_ROOT, '.github', 'workflows', 'libghostty-native-publish.yml'),
  'utf8',
);
const coverage = fs.readFileSync(path.join(REPO_ROOT, '.github', 'workflows', 'coverage.yml'), 'utf8');

function nativePathMatcher() {
  const match = native.match(/if grep -Eq '([^']+)' <<<"\$files"/);
  assert.ok(match, 'native path matcher must remain discoverable');
  return new RegExp(match[1]);
}

function releasePathMatcher(workflow) {
  const match = workflow.match(/if grep -Eq '([^']+)' <<<"\$files"/);
  assert.ok(match, 'release path matcher must remain discoverable');
  return new RegExp(match[1]);
}

test('generic integration jobs are compile-only without deleting runtime coverage', () => {
  const commands = [...ci.matchAll(/go test -v -race -count=1 -run '\^\$' -tags=integration \.\/internal\/integration\/\.\.\./g)];
  assert.equal(commands.length, 2, 'Linux and macOS generic jobs must compile integration tests');
  const linux = native.match(/  linux-adapter:[\s\S]*?(?=\n  [a-z][\w-]+:\n)/)?.[0];
  assert.ok(linux, 'Linux native adapter must remain present');
  assert.match(linux, /goarch: amd64\n\s+run_tests: true/);
  assert.match(linux, /goarch: arm64\n\s+run_tests: false/);
  const runtime = linux.match(/if \[ "\$RUN_TESTS" = "true" \]; then([\s\S]*?)\n\s+else/)[1];
  const integration = runtime.match(/run_timed integration[\s\S]*?(?=\n\s+run_timed )/)?.[0];
  assert.ok(integration, 'Linux amd64 RUN_TESTS=true branch must run full integration');
  assert.match(integration, /go test -v -race -count=1 \\\s*\n\s+-tags='libghostty integration' \.\/internal\/integration\/\.\.\./);
  assert.doesNotMatch(integration, /-run/);
  assert.equal((native.match(/-tags='libghostty integration' \.\/internal\/integration\/\.\.\./g) || []).length, 1);
  assert.doesNotMatch(native, /default-builds:/);
});

test('Ubuntu build verifies every supported untagged target remains native-free', () => {
  const build = ci.match(/  build:\n[\s\S]*?(?=\n  [a-z][\w-]+:)/)?.[0];
  assert.ok(build, 'Ubuntu build job must remain present');
  assert.match(build, /Verify untagged fail-closed binaries/);
  for (const target of ['darwin\/arm64', 'linux\/amd64', 'linux\/arm64']) {
    assert.match(build, new RegExp(target));
  }
  assert.match(build, /CGO_ENABLED=0 go build/);
  assert.match(build, /verify-default-binary/);
  assert.doesNotMatch(native, /default-builds:/);
});

test('native path routing excludes docs but covers causal and dependency inputs', () => {
  const matcher = nativePathMatcher();
  assert.equal(matcher.test('website/content/docs/troubleshooting.md'), false);
  assert.equal(matcher.test('docs/design/2026-07-18-libghostty-daemon-backend.md'), false);
  assert.equal(matcher.test('internal/pty/terminal_backend_ghostty.go'), true);
  assert.equal(matcher.test('internal/integration/daemon_test.go'), true);
  assert.equal(matcher.test('libghostty-native.lock.json'), true);
  assert.equal(matcher.test('go.sum'), true);
});

test('native detector is fail-safe when the authoritative file list is unavailable', () => {
  const failure = native.match(/if ! files="\$\(gh api[\s\S]*?\n\s+fi/)[0];
  assert.match(failure, /pulls\/\$PR\/files/);
  assert.match(failure, /echo "native=true" >> "\$GITHUB_OUTPUT"/);
  assert.match(failure, /echo "dependency-unit=true" >> "\$GITHUB_OUTPUT"/);
});

test('lock routing explicitly enables dependency validation', () => {
  const lock = native.match(/if grep -Fxq 'libghostty-native\.lock\.json'[\s\S]*?\n\s+fi/)[0];
  assert.match(lock, /echo "dependency-unit=true" >> "\$GITHUB_OUTPUT"/);
});

test('historical upgrade fixture uses an immutable fetched source and stays out of artifacts', () => {
  assert.equal(
    (goreleaser.match(/historical_revision=00a8dc8e5806850b857b291b9a5f19088e80c580/g) || []).length,
    2,
  );
  assert.equal((goreleaser.match(/GRAITH_LIBGHOSTTY_HISTORICAL_UPGRADE_BINARY/g) || []).length, 2);
  const executeLinux = goreleaser.match(/  execute-linux:[\s\S]*?(?=\n  [a-z][\w-]+:\n)/)?.[0];
  assert.ok(executeLinux, 'stable execute-linux job must remain present');
  assert.match(executeLinux, /uses: actions\/checkout@[\da-f]+[\s\S]*?fetch-depth: 0/);
  assert.match(goreleaser, /test ! -e "dist\/graith-historical-pre-removal"/);
  assert.doesNotMatch(goreleaser, /TestLibghosttyCharmToNativeUpgrade|gr-charm/);
});

test('local native builds isolate the Go cache from ambient pkg-config state', () => {
  const buildLocal = nativeScript.match(/build_local\(\) \{[\s\S]*?\n\}/)?.[0];
  assert.ok(buildLocal, 'build-local command must remain present');
  assert.match(buildLocal, /pkgconfig="\$\(write_pkg_config/);
  assert.match(buildLocal, /gocache="\$NATIVE_WORK\/go-cache"/);
  assert.match(buildLocal, /GOCACHE="\$gocache"[\s\S]*?PKG_CONFIG_PATH="\$pkgconfig/);
  assert.doesNotMatch(buildLocal, /go clean -cache/);
});

test('release workflows gate only pull-request release work and fail safe', () => {
  const devMatcher = releasePathMatcher(devRelease);
  const stableMatcher = releasePathMatcher(goreleaser);
  for (const path of ['internal/pty/terminal_backend_ghostty.go', 'go.mod', 'website/content/docs/installation.md']) {
    assert.equal(devMatcher.test(path), false);
    assert.equal(stableMatcher.test(path), false);
  }
  assert.equal(devMatcher.test('scripts/dev-release-version.sh'), true);
  assert.equal(devMatcher.test('macos/notifier/build.sh'), true);
  assert.equal(stableMatcher.test('.release-please-config.json'), true);
  assert.equal(stableMatcher.test('CHANGELOG.md'), true);
  assert.equal(stableMatcher.test('scripts/render-stable-aur.sh'), true);
  assert.equal(stableMatcher.test('scripts/rpm-preset-keygrips.sh'), true);
  assert.equal(stableMatcher.test('scripts/publish-linux-repositories.sh'), true);
  for (const workflow of [devRelease, goreleaser]) {
    assert.match(workflow, /if \[ "\$EVENT" != "pull_request" \]; then[\s\S]*?echo "release=true"/);
    assert.match(workflow, /if ! files="\$\(gh api "repos\/\$REPO\/pulls\/\$PR\/files"[\s\S]*?echo "release=true"/);
    assert.match(workflow, /release-context:[\s\S]*?needs: changes/);
    assert.match(workflow, /release-context:[\s\S]*?needs\.changes\.outputs\.release == 'true'/);
  }
  assert.match(devRelease, /branches:\n      - main/);
  assert.match(goreleaser, /tags:\n      - "v\*"/);
});

test('Linux artifacts are lock-complete and published only by trusted immutable workflow', () => {
  const lock = JSON.parse(fs.readFileSync(path.join(REPO_ROOT, 'libghostty-native.lock.json'), 'utf8'));
  for (const arch of ['amd64', 'arm64']) {
    const artifact = lock.ghostty.linuxArtifacts[arch];
    assert.match(artifact.url, new RegExp(`libghostty-vt-linux-${arch}\\.tar\\.gz$`));
    assert.match(artifact.sha256, /^[0-9a-f]{64}$/);
    assert.notEqual(new Set(artifact.sha256).size, 1, `${arch} artifact digest must not be a repeated-character placeholder`);
  }
  assert.match(nativeScript, /sha256_check "\$expected" "\$archive"/);
  assert.match(nativeScript, /sha256_check[\s\S]*?tar -xzf/);
  assert.match(nativeScript, /unexpected or incomplete archive members/);
  assert.match(nativePublish, /contents: write/);
  assert.match(nativePublish, /github\.event_name == 'workflow_dispatch' \|\| github\.ref == 'refs\/heads\/main'/);
  assert.match(nativePublish, /verified immutable asset already published/);
  assert.match(nativePublish, /remote_asset_sha[\s\S]*expected_asset_sha/);
  assert.match(nativePublish, /export PATH=\"\$RUNNER_TEMP\/zig:\$PATH\"/);
  assert.match(nativePublish, /test \"\$\(zig version\)\" = \"\$\(jq -er '\.zig\.version' libghostty-native\.lock\.json\)\"/);
  assert.match(nativePublish, /env GOARCH=amd64 scripts\/libghostty-native\.sh source-build/);
  assert.match(nativePublish, /Cflags: -I\\\$\{prefix\}\/include -DGHOSTTY_STATIC/);
  assert.match(nativePublish, /cp -R gui\/shared\/Sources\/CGhosttyVT\/include/);
  assert.match(nativePublish, /libghostty-linux-archive\.py pack/);
  assert.match(nativeScript, /libghostty-linux-archive\.py.*inspect/);
  assert.match(nativeScript, /test-linux-archive-policy/);
  assert.match(nativePublish, /prefix=\\\$\{pcfiledir\}\/\.\./);
  assert.match(nativePublish, /Libs: -L\\\$\{prefix\} -lghostty-vt/);
  assert.match(nativePublish, /linuxArtifacts\.amd64\.url/);
  assert.ok(nativePublish.includes('capture('));
  assert.match(native, /test-linux-artifact/);
  assert.doesNotMatch(native, /CGO_CFLAGS: -I\$\{\{ github\.workspace \}\}\/gui\/shared/);
  assert.match(nativeScript, /pkg-config --cflags libghostty-vt-static/);
  assert.match(nativeScript, /unexpected include path/);
  assert.match(native, /unset CGO_CFLAGS CGO_CPPFLAGS CPATH C_INCLUDE_PATH CPLUS_INCLUDE_PATH/);
  assert.match(native, /source-build "\$TARGET" "\$LIBRARY"/);
  assert.match(native, /test-linux-artifact/);
  assert.doesNotMatch(native.match(/name: Contract-test the published Linux artifact[\s\S]*?(?=\n      - name:)/)?.[0] || '', /if: needs\.changes\.outputs\.dependency-unit/);
  assert.match(nativePublish, /gh release create[\s\S]*?gh release view/);
  assert.match(nativePublish, /actions\/checkout@[0-9a-f]{40}/);
});

test('coverage measures the tagged production graph for both HEAD and base', () => {
  assert.match(coverage, /prepare-linux-artifact amd64/);
  assert.equal((coverage.match(/prepare-linux-artifact amd64/g) || []).length, 1);
  assert.match(coverage, /go test -tags=libghostty -coverprofile="\$profile" \.\/\.\.\./);
  assert.match(coverage, /run_cover cover\.head\.out head/);
  assert.match(coverage, /run_cover cover\.base\.out base/);
  assert.match(coverage, /HEAD and BASE use the lock and setup script/);
});
