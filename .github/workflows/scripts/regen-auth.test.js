'use strict';

// Authentication and trust-boundary regression tests for regen.yml. These are
// static by design: they require no repository secret and cannot reveal one.

const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const WORKFLOW = path.join(__dirname, '..', 'regen.yml');
const yaml = fs.readFileSync(WORKFLOW, 'utf8');

function step(stepName) {
  const start = yaml.indexOf(`- name: ${stepName}`);
  assert.notEqual(start, -1, `step "${stepName}" not found in regen.yml`);
  const rest = yaml.slice(start);
  const next = rest.slice(1).search(/\n {6}- (?:name: |uses: )/);
  return next === -1 ? rest : rest.slice(0, next + 1);
}

test('same-repository guard excludes fork-controlled pull requests', () => {
  assert.match(yaml, /\non:\n  pull_request:\n/);
  assert.doesNotMatch(yaml, /pull_request_target/);
  assert.match(
    yaml,
    /if: github\.event\.pull_request\.head\.repo\.full_name == github\.repository/,
  );
});

test('missing RELEASE_TOKEN fails before checkout instead of falling back', () => {
  const validation = step('Require workflow-triggering token');
  const checkout = step('Check out PR head with workflow-triggering credentials');

  assert.match(validation, /RELEASE_TOKEN: \$\{\{ secrets\.RELEASE_TOKEN \}\}/);
  assert.match(validation, /if \[ -z "\$RELEASE_TOKEN" \]; then/);
  assert.match(validation, /exit 1/);
  assert.doesNotMatch(validation, /echo[^\n]*\$RELEASE_TOKEN/);
  assert.ok(
    yaml.indexOf('- name: Require workflow-triggering token') <
      yaml.indexOf('- name: Check out PR head with workflow-triggering credentials'),
    'token validation must run before checkout',
  );
  assert.match(checkout, /token: \$\{\{ secrets\.RELEASE_TOKEN \}\}/);
  assert.match(checkout, /persist-credentials: true/);
  assert.doesNotMatch(yaml, /https:\/\/[^\n]*RELEASE_TOKEN/);
});

test('RELEASE_TOKEN is exposed only for validation and authenticated checkout', () => {
  assert.equal(
    (yaml.match(/\$\{\{ secrets\.RELEASE_TOKEN \}\}/g) || []).length,
    2,
    'the secret should be scoped to validation and checkout only',
  );
});

test('regeneration has no default-token fallback or manual workflow dispatch', () => {
  assert.doesNotMatch(yaml, /\$\{\{\s*github\.token\s*\}\}/);
  assert.doesNotMatch(yaml, /\bGITHUB_TOKEN\b/);
  assert.doesNotMatch(yaml, /\bgh workflow run\b/);
});

test('workflow retains read-only default-token permissions', () => {
  assert.match(yaml, /\npermissions:\n  contents: read\n/);
  assert.doesNotMatch(yaml, /\bactions:\s*write\b/);
  assert.doesNotMatch(yaml, /\bcontents:\s*write\b/);
});

test('push remains branch-scoped, non-force, and authenticated by checkout', () => {
  const commit = step('Commit back if changed');
  assert.match(commit, /git push origin "HEAD:\$HEAD_REF"/);
  assert.doesNotMatch(commit, /git push[^\n]*--force/);
});
