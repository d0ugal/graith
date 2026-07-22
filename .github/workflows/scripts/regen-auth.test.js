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

function job(jobName) {
  const start = yaml.indexOf(`\n  ${jobName}:\n`);
  assert.notEqual(start, -1, `job "${jobName}" not found in regen.yml`);
  const rest = yaml.slice(start + 1);
  const next = rest.slice(1).search(/\n {2}[a-z][\w-]*:\n/);
  return next === -1 ? rest : rest.slice(0, next + 1);
}

test('same-repository guard excludes fork-controlled pull requests', () => {
  assert.match(yaml, /\non:\n  pull_request:\n/);
  assert.doesNotMatch(yaml, /pull_request_target/);
  for (const jobName of ['validate', 'prepare', 'regen']) {
    assert.match(
      job(jobName),
      /if: (?:always\(\) && )?github\.event\.pull_request\.head\.repo\.full_name == github\.repository/,
    );
  }
});

test('missing RELEASE_TOKEN fails before checkout instead of falling back', () => {
  const validation = step('Require workflow-triggering token');
  const checkout = step('Check out source head with workflow-triggering credentials');

  assert.match(validation, /RELEASE_TOKEN: \$\{\{ secrets\.RELEASE_TOKEN \}\}/);
  assert.match(validation, /if \[ -z "\$RELEASE_TOKEN" \]; then/);
  assert.match(validation, /exit 1/);
  assert.doesNotMatch(validation, /echo[^\n]*\$RELEASE_TOKEN/);
  assert.ok(
    yaml.indexOf('- name: Require workflow-triggering token') <
      yaml.indexOf('- name: Check out PR head without persisted credentials'),
    'token validation must run before the first checkout',
  );
  assert.match(checkout, /token: \$\{\{ secrets\.RELEASE_TOKEN \}\}/);
  assert.match(checkout, /persist-credentials: true/);
  assert.doesNotMatch(yaml, /https:\/\/[^\n]*RELEASE_TOKEN/);
  assert.doesNotMatch(job('validate'), /actions\/checkout|go test|make package-graph/);
});

test('RELEASE_TOKEN is exposed only for validation and fresh-runner push checkout', () => {
  assert.equal(
    (yaml.match(/\$\{\{ secrets\.RELEASE_TOKEN \}\}/g) || []).length,
    2,
    'the secret should be scoped to validation and fresh-runner push checkout only',
  );
});

test('regeneration has no default push-token fallback or manual workflow dispatch', () => {
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
  const push = step('Push generated commit');
  assert.match(push, /git push origin "HEAD:\$HEAD_REF"/);
  assert.doesNotMatch(push, /git push[^\n]*--force/);
});

test('PAT is persisted only in the fresh-runner push job', () => {
  const validate = job('validate');
  const prepare = job('prepare');
  const regen = job('regen');
  const initialCheckout = step('Check out PR head without persisted credentials');
  const pushCheckout = step('Check out source head with workflow-triggering credentials');

  assert.match(initialCheckout, /persist-credentials: false/);
  assert.doesNotMatch(initialCheckout, /secrets\.RELEASE_TOKEN/);
  assert.match(prepare, /needs: validate/);
  assert.doesNotMatch(prepare, /secrets\.RELEASE_TOKEN/);
  assert.doesNotMatch(prepare, /persist-credentials: true/);
  assert.match(regen, /needs: \[validate, prepare\]/);
  assert.match(pushCheckout, /needs\.prepare\.outputs\.changed == 'true'/);
  assert.match(pushCheckout, /ref: \$\{\{ github\.event\.pull_request\.head\.sha \}\}/);
  assert.match(pushCheckout, /persist-credentials: true/);
  assert.doesNotMatch(validate, /actions\/checkout|go test|make package-graph/);
  assert.doesNotMatch(regen, /go test|make package-graph|scripts\/libghostty-native\.sh/);
});

test('fresh-runner job verifies the transferred commit before pushing', () => {
  const push = step('Push generated commit');

  assert.match(job('prepare'), /git bundle create "\$RUNNER_TEMP\/generated\.bundle"/);
  assert.match(push, /git show -s --format=%P "\$GENERATED_SHA"/);
  assert.match(push, /git diff --no-renames --name-only -z "\$SOURCE_SHA" "\$GENERATED_SHA"/);
  assert.match(push, /Generated commit contains a non-allowlisted path/);
});
