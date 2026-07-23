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

function nativePathMatcher() {
  const match = native.match(/if grep -Eq '([^']+)' <<<"\$files"/);
  assert.ok(match, 'native path matcher must remain discoverable');
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
  for (const workflow of [devRelease, goreleaser]) {
    assert.match(workflow, /if \[ "\$EVENT" != "pull_request" \]; then[\s\S]*?echo "release=true"/);
    assert.match(workflow, /if ! files="\$\(gh api "repos\/\$REPO\/pulls\/\$PR\/files"[\s\S]*?echo "release=true"/);
    assert.match(workflow, /release-context:[\s\S]*?needs: changes/);
    assert.match(workflow, /release-context:[\s\S]*?needs\.changes\.outputs\.release == 'true'/);
    assert.match(workflow, /\.release-please-\(manifest\|config\)/);
    assert.match(workflow, /render-stable-\(homebrew\|aur\)/);
    assert.match(workflow, /internal\/\(release\|daemonservice\)/);
  }
  assert.match(devRelease, /branches:\n      - main/);
  assert.match(goreleaser, /tags:\n      - "v\*"/);
});
