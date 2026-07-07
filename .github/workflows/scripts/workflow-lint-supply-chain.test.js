'use strict';

// Supply-chain regression tests for .github/workflows/workflow-lint.yml.
// Run with the Node built-in test runner (no dependencies): `node --test`.
//
// Issue #799: actionlint and zizmor were installed inside `run:` blocks with
// no checksum/provenance verification, inconsistent with the SHA-pinned
// `uses:` entries and the provenance-verified nono install in sandbox.yml. A
// tampered/MITM'd download could run in CI. The fix downloads each tool's
// release tarball and verifies its GitHub build-provenance attestation with
// `gh attestation verify` before installing (fail-closed), so these tests
// guard that the verified install path stays in place and the old unverified
// path never returns.

const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const WORKFLOW = path.join(__dirname, '..', 'workflow-lint.yml');
const yaml = fs.readFileSync(WORKFLOW, 'utf8');

// Return the text of the `run:` block for the named install step. Steps are
// `- name: <step>` entries; we slice from that step's name to the start of the
// next step (or the next job) so assertions are scoped to a single tool.
function installStep(stepName) {
  const start = yaml.indexOf(`name: ${stepName}`);
  assert.notEqual(start, -1, `step "${stepName}" not found in workflow-lint.yml`);
  const rest = yaml.slice(start + stepName.length);
  const next = rest.search(/\n {6}- name: |\n {2}\w[\w-]*:\n/);
  return next === -1 ? rest : rest.slice(0, next);
}

test('actionlint tarball is provenance-verified against rhysd/actionlint before install', () => {
  const step = installStep('Install actionlint');
  const verifyAt = step.indexOf('gh attestation verify');
  assert.ok(verifyAt !== -1, 'actionlint install must run `gh attestation verify`');
  assert.match(step, /gh attestation verify [^\n]*--repo rhysd\/actionlint/);

  // Fail closed: verification must precede extraction/install so a tampered
  // artifact is never unpacked onto the runner.
  const installAt = step.search(/sudo install|tar -xzf/);
  assert.ok(installAt !== -1, 'actionlint install must extract/install the tarball');
  assert.ok(
    verifyAt < installAt,
    'actionlint provenance must be verified BEFORE extracting/installing the tarball',
  );
});

test('zizmor tarball is provenance-verified against zizmorcore/zizmor before install', () => {
  const step = installStep('Install zizmor');
  const verifyAt = step.indexOf('gh attestation verify');
  assert.ok(verifyAt !== -1, 'zizmor install must run `gh attestation verify`');
  assert.match(step, /gh attestation verify [^\n]*--repo zizmorcore\/zizmor/);

  const extractAt = step.indexOf('tar -xzf');
  assert.ok(extractAt !== -1, 'zizmor install must extract the tarball');
  assert.ok(
    verifyAt < extractAt,
    'zizmor provenance must be verified BEFORE extracting the tarball',
  );
});

test('zizmor is no longer installed via unpinned uvx', () => {
  // The old path (`uvx "zizmor==..."` + astral-sh/setup-uv) was version-pinned
  // but not hash-locked. It must not reappear.
  assert.doesNotMatch(yaml, /uvx\b/, 'zizmor must not be installed via uvx');
  assert.doesNotMatch(yaml, /setup-uv/, 'the setup-uv action is no longer needed');
});

test('provenance-verifying jobs grant attestations:read and pass GH_TOKEN', () => {
  // gh attestation verify reads the repo's attestations via the GitHub API.
  const attestationsRead = (yaml.match(/attestations: read/g) || []).length;
  assert.ok(
    attestationsRead >= 2,
    'both the actionlint and zizmor jobs must grant `attestations: read`',
  );
  const ghToken = (yaml.match(/GH_TOKEN: \$\{\{ github\.token \}\}/g) || []).length;
  assert.ok(
    ghToken >= 2,
    'both install steps must set GH_TOKEN for `gh attestation verify`',
  );
});

test('downloads pin https + TLS 1.2 (curl --proto/--tlsv1.2)', () => {
  for (const tool of ['Install actionlint', 'Install zizmor']) {
    const step = installStep(tool);
    assert.match(
      step,
      /curl [^\n]*--proto '=https' --tlsv1\.2/,
      `${tool} must download over pinned https/TLS1.2`,
    );
  }
});
