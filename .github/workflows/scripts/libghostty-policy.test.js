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

function nativePathMatcher() {
  const match = native.match(/if grep -Eq '([^']+)' <<<"\$files"/);
  assert.ok(match, 'native path matcher must remain discoverable');
  return new RegExp(match[1]);
}

test('generic integration jobs are compile-only without deleting runtime coverage', () => {
  const commands = [...ci.matchAll(/go test -v -race -count=1 -run '\^\$' -tags=integration \.\/internal\/integration\/\.\.\./g)];
  assert.equal(commands.length, 2, 'Linux and macOS generic jobs must compile integration tests');
  assert.match(
    native,
    /run_timed integration go test -v -race -count=1 \\\s*\n\s+-tags='libghostty integration' \.\/internal\/integration\/\.\.\./,
  );
  assert.equal(
    (native.match(/-tags='libghostty integration' \.\/internal\/integration\/\.\.\./g) || []).length,
    1,
    'the native Linux amd64 lane must own the single full integration execution',
  );
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
  assert.match(native, /if ! files="\$\(gh api "repos\/\$REPO\/pulls\/\$PR\/files" --paginate --jq '\.\[\]\.filename'\)"; then/);
  assert.match(native, /echo "native=true" >> "\$GITHUB_OUTPUT"/);
  assert.match(native, /echo "dependency-unit=true" >> "\$GITHUB_OUTPUT"/);
});
