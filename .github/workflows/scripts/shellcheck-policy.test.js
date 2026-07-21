'use strict';

// Regression tests for the repository-wide ShellCheck gate. Run with the Node
// built-in test runner (no dependencies): `node --test`.

const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const REPO_ROOT = path.join(__dirname, '..', '..', '..');
const makefile = fs.readFileSync(path.join(REPO_ROOT, 'Makefile'), 'utf8');
const workflow = fs.readFileSync(path.join(__dirname, '..', 'workflow-lint.yml'), 'utf8');

test('shellcheck target covers every tracked shell script with strict correctness checks', () => {
  assert.match(
    makefile,
    /git ls-files -z -- '\*\.sh' \| xargs -0 shellcheck --enable=all --severity=warning/,
  );
});

test('workflow runs shellcheck when nested or root shell scripts change', () => {
  assert.match(workflow, /name: Lint tracked shell scripts\n\s+run: \|\n\s+shellcheck --version\n\s+make shellcheck/);
  assert.equal((workflow.match(/- '\*\*\/\*\.sh'/g) || []).length, 2);
  assert.equal((workflow.match(/- '\*\.sh'/g) || []).length, 2);
});
