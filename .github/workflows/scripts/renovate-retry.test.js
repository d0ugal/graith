'use strict';

// Regression tests for the narrowly scoped network retry in the native
// Renovate fixture verifier. Run with the Node built-in test runner.

const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawnSync } = require('node:child_process');

const REPO_ROOT = path.join(__dirname, '..', '..', '..');
const VERIFY_SCRIPT = path.join(REPO_ROOT, 'scripts', 'verify-renovate-libghostty.sh');

const TRANSIENT_LOG = JSON.stringify({
  level: 50,
  msg: 'lookupUpdates error',
  err: {
    message:
      "fatal: unable to access 'https://tangled.org/mitchellh.com/go-libghostty/': " +
      'gnutls_handshake() failed: The TLS connection was non-properly terminated.',
  },
});

const DETERMINISTIC_LOG = JSON.stringify({
  level: 50,
  msg: 'lookupUpdates error',
  err: {
    message:
      "fatal: unable to access 'https://tangled.org/mitchellh.com/go-libghostty/': " +
      'The requested URL returned error: 403',
  },
});

const nativeDeps = [
  'Ghostty',
  'Highway',
  'SPDX tools-java',
  'Zig',
  'go-libghostty',
  'simdutf',
  'uucode',
].map((depName) => ({
  depName,
  depType: 'libghostty-native',
  updates: [{ branchName: 'renovate/libghostty-native' }],
}));

const SUCCESS_LOG = [
  {
    level: 20,
    msg: 'packageFiles with updates',
    config: { regex: [{ deps: nativeDeps }] },
  },
  {
    level: 20,
    msg: 'Repository config',
    config: {
      packageRules: [
        {
          matchDepTypes: ['libghostty-native'],
          groupSlug: 'libghostty-native',
          automerge: false,
          postUpgradeTasks: null,
        },
        {
          matchDepTypes: ['libghostty-native'],
          matchDepNames: ['Ghostty', 'Zig', 'uucode', 'Highway', 'simdutf'],
          dependencyDashboardApproval: true,
        },
        {
          matchManagers: ['gomod'],
          matchPackageNames: ['go.mitchellh.com/libghostty'],
          enabled: false,
          automerge: false,
        },
      ],
    },
  },
]
  .map(JSON.stringify)
  .join('\n');

function writeExecutable(file, contents) {
  fs.writeFileSync(file, contents, { mode: 0o755 });
}

function runVerifier(responses) {
  const temp = fs.mkdtempSync(path.join(os.tmpdir(), 'graith-renovate-retry-'));
  const bin = path.join(temp, 'bin');
  const responseDir = path.join(temp, 'responses');
  const countFile = path.join(temp, 'count');
  fs.mkdirSync(bin);
  fs.mkdirSync(responseDir);
  fs.writeFileSync(countFile, '0\n');

  responses.forEach(({ log, status }, index) => {
    fs.writeFileSync(path.join(responseDir, `${index + 1}.log`), `${log}\n`);
    fs.writeFileSync(path.join(responseDir, `${index + 1}.status`), `${status}\n`);
  });

  writeExecutable(
    path.join(bin, 'renovate-config-validator'),
    '#!/bin/sh\nexit 0\n',
  );
  writeExecutable(path.join(bin, 'sleep'), '#!/bin/sh\nexit 0\n');
  writeExecutable(
    path.join(bin, 'renovate'),
    `#!/bin/sh
count="$(cat "$FAKE_RENOVATE_COUNT")"
count=$((count + 1))
printf '%s\\n' "$count" >"$FAKE_RENOVATE_COUNT"
cat "$FAKE_RENOVATE_RESPONSES/$count.log"
exit "$(cat "$FAKE_RENOVATE_RESPONSES/$count.status")"
`,
  );

  const result = spawnSync(VERIFY_SCRIPT, {
    cwd: REPO_ROOT,
    encoding: 'utf8',
    env: {
      ...process.env,
      PATH: `${bin}:${process.env.PATH}`,
      RENOVATE_BIN: 'renovate',
      RENOVATE_CONFIG_VALIDATOR_BIN: 'renovate-config-validator',
      FAKE_RENOVATE_COUNT: countFile,
      FAKE_RENOVATE_RESPONSES: responseDir,
    },
  });
  const count = Number.parseInt(fs.readFileSync(countFile, 'utf8'), 10);
  fs.rmSync(temp, { recursive: true, force: true });
  return { ...result, count };
}

test('retries the tangled.org GnuTLS termination and accepts a later success', () => {
  const result = runVerifier([
    { log: TRANSIENT_LOG, status: 1 },
    { log: SUCCESS_LOG, status: 0 },
  ]);

  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.count, 2);
  assert.match(result.stderr, /retrying Renovate lookup \(attempt 2 of 3\)/);
  assert.match(result.stdout, /detected and grouped all seven native dependency fixtures/);
});

test('does not retry a deterministic lookup failure from the same repository', () => {
  const result = runVerifier([{ log: DETERMINISTIC_LOG, status: 1 }]);

  assert.equal(result.status, 1);
  assert.equal(result.count, 1);
  assert.doesNotMatch(result.stderr, /retrying Renovate lookup/);
  assert.match(result.stderr, /requested URL returned error: 403/);
});

test('bounds repeated transient failures to three lookup attempts', () => {
  const result = runVerifier([
    { log: TRANSIENT_LOG, status: 1 },
    { log: TRANSIENT_LOG, status: 1 },
    { log: TRANSIENT_LOG, status: 1 },
  ]);

  assert.equal(result.status, 1);
  assert.equal(result.count, 3);
  assert.match(result.stderr, /retrying Renovate lookup \(attempt 3 of 3\)/);
  assert.match(result.stderr, /Renovate lookup dry run failed/);
});
