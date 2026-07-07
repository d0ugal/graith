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
function step(stepName) {
  const start = yaml.indexOf(`name: ${stepName}`);
  assert.notEqual(start, -1, `step "${stepName}" not found in workflow-lint.yml`);
  const rest = yaml.slice(start + stepName.length);
  const next = rest.search(/\n {6}- name: |\n {2}\w[\w-]*:\n/);
  return next === -1 ? rest : rest.slice(0, next);
}

// The shell of an install step with YAML/shell comment lines stripped, so
// assertions match executable code — not prose that merely mentions a command.
// (A comment like "# ... `gh attestation verify` ..." must NOT satisfy an
// assertion about the actual command; see the ordering tests below.)
function stepCode(stepName) {
  return step(stepName)
    .split('\n')
    .filter((line) => !/^\s*#/.test(line))
    .join('\n');
}

// Each tool: install step name, the repo its attestation must bind to, and the
// command that unpacks/installs the artifact (which must run AFTER verify).
const TOOLS = [
  { name: 'Install actionlint', repo: 'rhysd/actionlint', extract: /tar -xzf|sudo install/ },
  { name: 'Install zizmor', repo: 'zizmorcore/zizmor', extract: /tar -xzf/ },
];

for (const tool of TOOLS) {
  test(`${tool.name}: tarball is provenance-verified against ${tool.repo}`, () => {
    const code = stepCode(tool.name);
    const verify = new RegExp(`gh attestation verify [^\\n]*--repo ${tool.repo.replace('/', '\\/')}`);
    assert.match(code, verify, `must run \`gh attestation verify --repo ${tool.repo}\``);
  });

  test(`${tool.name}: provenance is verified BEFORE extract/install (fail-closed ordering)`, () => {
    const code = stepCode(tool.name);
    // Anchor to the real command in executable code, NOT a comment mention —
    // otherwise a reordering regression (verify after extract) passes silently.
    const verifyAt = code.search(/gh attestation verify /);
    const extractAt = code.search(tool.extract);
    assert.ok(verifyAt !== -1, 'verify command must exist in the run block');
    assert.ok(extractAt !== -1, 'extract/install command must exist in the run block');
    assert.ok(
      verifyAt < extractAt,
      `${tool.name}: provenance must be verified BEFORE the artifact is extracted/installed`,
    );
  });

  test(`${tool.name}: verification is not defeated (set -euo pipefail, no || true)`, () => {
    const code = stepCode(tool.name);
    // A failed `gh attestation verify` must abort the job. `set -e` does that;
    // a `|| true` / `|| :` guard, or `set +e`, would silently swallow the
    // failure and defeat the fail-closed guarantee.
    assert.match(code, /set -euo pipefail/, 'run block must use `set -euo pipefail`');
    assert.doesNotMatch(code, /gh attestation verify[^\n]*\|\|/, 'verify must not be guarded with `|| ...`');
    assert.doesNotMatch(code, /set \+e/, 'run block must not disable errexit with `set +e`');
  });

  test(`${tool.name}: sets GH_TOKEN for gh attestation verify`, () => {
    // gh attestation verify reads the repo's attestations via the GitHub API,
    // so the token must be scoped to THIS install step (not merely present
    // somewhere else in the file).
    assert.match(
      step(tool.name),
      /GH_TOKEN: \$\{\{ github\.token \}\}/,
      `${tool.name} must set GH_TOKEN`,
    );
  });

  test(`${tool.name}: download pins https + TLS 1.2 (curl --proto/--tlsv1.2)`, () => {
    assert.match(
      stepCode(tool.name),
      /curl [^\n]*--proto '=https' --tlsv1\.2/,
      `${tool.name} must download over pinned https/TLS1.2`,
    );
  });
}

test('zizmor is no longer installed via unpinned uvx', () => {
  // The old path (`uvx "zizmor==..."` + astral-sh/setup-uv) was version-pinned
  // but not hash-locked. It must not reappear. Strip comments first so a prose
  // mention of the old tooling in an explanatory comment doesn't trip this.
  const code = yaml
    .split('\n')
    .filter((line) => !/^\s*#/.test(line))
    .join('\n');
  assert.doesNotMatch(code, /uvx\b/, 'zizmor must not be installed via uvx');
  assert.doesNotMatch(code, /setup-uv/, 'the setup-uv action is no longer needed');
});

test('both provenance-verifying jobs grant attestations:read', () => {
  // Scope the check to the two jobs, not a global count: each of the actionlint
  // and zizmor jobs must independently grant `attestations: read`.
  for (const job of ['actionlint', 'zizmor']) {
    const start = yaml.search(new RegExp(`\\n  ${job}:\\n`));
    assert.notEqual(start, -1, `job "${job}" not found`);
    const rest = yaml.slice(start + 1);
    const next = rest.search(/\n  \w[\w-]*:\n/);
    const jobBlock = next === -1 ? rest : rest.slice(0, next);
    assert.match(
      jobBlock,
      /attestations: read/,
      `job "${job}" must grant \`attestations: read\` for gh attestation verify`,
    );
  }
});
