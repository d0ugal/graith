'use strict';

// Unit tests for the docs-preview cleanup/prune helpers. Run with the Node
// built-in test runner (no dependencies): `node --test`.
//
// The central invariant under test is issue #763: cleanup and prune rebuild
// the `screenshots` branch WITHOUT a base_tree, so they must never rebuild
// from a truncated (partial) tree listing — doing so would silently delete
// every screenshot GitHub omitted from the response.

const { test } = require('node:test');
const assert = require('node:assert/strict');

const { listBlobsOrFail, isStaleRunDir, cleanup, prune } = require('./docs-preview.js');

// Minimal core stub capturing info/warning output.
function fakeCore() {
  const infos = [];
  const warnings = [];
  return {
    info: (m) => infos.push(m),
    warning: (m) => warnings.push(m),
    infos,
    warnings,
  };
}

// Build a github stub whose git API is backed by an in-memory blob list.
// Records the tree handed to createTree so tests can assert what would be
// written to the branch.
function fakeGithub({ blobs = null, truncated = false, branchExists = true, comments = [] } = {}) {
  const calls = { createTree: [], createCommit: [], updateRef: [], updatedComments: [] };
  const github = {
    paginate: async () => comments,
    rest: {
      git: {
        getRef: async () => {
          if (!branchExists) {
            const e = new Error('Not Found');
            e.status = 404;
            throw e;
          }
          return { data: { object: { sha: 'tip-commit' } } };
        },
        getCommit: async () => ({ data: { tree: { sha: 'tip-tree' } } }),
        getTree: async () => ({
          data: {
            truncated,
            // Include a `tree` (directory) entry so callers must filter to
            // blobs — listBlobsOrFail drops non-blob entries.
            tree: [
              { path: 'pr-dir', type: 'tree', mode: '040000', sha: 'dir-sha' },
              ...(blobs || []).map((path) => ({ path, type: 'blob', mode: '100644', sha: `sha-${path}` })),
            ],
          },
        }),
        createTree: async (args) => {
          // Capture the FULL args, not just `tree` — the #763 fix relies on
          // omitting base_tree (full replacement), so tests must be able to
          // assert its absence.
          calls.createTree.push(args);
          return { data: { sha: 'new-tree' } };
        },
        createCommit: async (args) => {
          calls.createCommit.push(args);
          return { data: { sha: 'new-commit' } };
        },
        updateRef: async (args) => {
          calls.updateRef.push(args);
        },
      },
      issues: {
        updateComment: async (args) => calls.updatedComments.push(args),
        createComment: async () => {},
        listComments: async () => ({ data: [] }),
      },
    },
  };
  return { github, calls };
}

const context = {
  repo: { owner: 'clachan', repo: 'croft' },
  payload: { pull_request: { number: 42 } },
};

test('listBlobsOrFail throws when the tree is truncated', async () => {
  const { github } = fakeGithub({ blobs: ['pr-1/a/x.png'], truncated: true });
  await assert.rejects(
    () => listBlobsOrFail({ github, owner: 'clachan', repo: 'croft', treeSha: 't' }),
    /truncated/i,
  );
});

test('listBlobsOrFail returns only blobs when not truncated', async () => {
  const { github } = fakeGithub({ blobs: ['pr-1/a/x.png', 'pr-2/b/y.png'] });
  const blobs = await listBlobsOrFail({ github, owner: 'clachan', repo: 'croft', treeSha: 't' });
  assert.deepEqual(blobs.map((b) => b.path), ['pr-1/a/x.png', 'pr-2/b/y.png']);
});

test('isStaleRunDir flags old run-dirs and spares recent/undated ones', () => {
  const now = Date.parse('2026-07-07T00:00:00Z');
  // 40 days old -> stale.
  assert.equal(isStaleRunDir('pr-1/20260528-abc1234-99.1/x.png', now), true);
  // 5 days old -> kept.
  assert.equal(isStaleRunDir('pr-1/20260702-abc1234-99.1/x.png', now), false);
  // No parseable date -> kept.
  assert.equal(isStaleRunDir('pr-1/nodate/x.png', now), false);
  // Not a pr- dir -> kept.
  assert.equal(isStaleRunDir('README.md', now), false);
});

// --- cleanup ---------------------------------------------------------------

test('cleanup refuses to rewrite the branch on a truncated tree (#763)', async () => {
  const { github, calls } = fakeGithub({ blobs: ['pr-42/a/x.png', 'pr-7/b/y.png'], truncated: true });
  const core = fakeCore();
  await assert.rejects(() => cleanup({ github, context, core }), /truncated/i);
  // The destructive path must not have run.
  assert.equal(calls.createTree.length, 0);
  assert.equal(calls.updateRef.length, 0);
});

test('cleanup rebuilds without base_tree, dropping only the closed PR', async () => {
  const { github, calls } = fakeGithub({
    blobs: ['pr-42/a/x.png', 'pr-42/a/y.png', 'pr-7/b/z.png'],
  });
  const core = fakeCore();
  await cleanup({ github, context, core });

  assert.equal(calls.createTree.length, 1);
  const written = calls.createTree[0].tree.map((e) => e.path);
  assert.deepEqual(written, ['pr-7/b/z.png']); // pr-42 gone, pr-7 kept
  // Full-replacement rebuild: base_tree must be omitted, else omitted paths
  // would linger instead of being deleted (the inverse of #763).
  assert.equal('base_tree' in calls.createTree[0], false);
  assert.equal(calls.updateRef.length, 1);
  assert.equal(calls.createCommit[0].parents[0], 'tip-commit');
});

test('cleanup updates the sticky comment when one exists', async () => {
  const { github, calls } = fakeGithub({
    blobs: ['pr-42/a/x.png', 'pr-7/b/z.png'],
    comments: [{ id: 99, body: '<!-- docs-preview -->\nold body' }],
  });
  const core = fakeCore();
  await cleanup({ github, context, core });
  assert.equal(calls.updatedComments.length, 1);
  assert.equal(calls.updatedComments[0].comment_id, 99);
  assert.match(calls.updatedComments[0].body, /cleaned up after it was closed/);
});

test('cleanup no-ops when the PR has no screenshots', async () => {
  const { github, calls } = fakeGithub({ blobs: ['pr-7/b/z.png'] });
  const core = fakeCore();
  await cleanup({ github, context, core });
  assert.equal(calls.createTree.length, 0);
  assert.equal(calls.updateRef.length, 0);
});

test('cleanup no-ops when the branch does not exist', async () => {
  const { github, calls } = fakeGithub({ branchExists: false });
  const core = fakeCore();
  await cleanup({ github, context, core });
  assert.equal(calls.createTree.length, 0);
});

// --- prune -----------------------------------------------------------------

test('prune refuses to rewrite the branch on a truncated tree (#763)', async () => {
  const { github, calls } = fakeGithub({
    blobs: ['pr-1/20200101-abc-1.1/x.png', 'pr-2/20260706-def-1.1/y.png'],
    truncated: true,
  });
  const core = fakeCore();
  await assert.rejects(() => prune({ github, context, core }), /truncated/i);
  assert.equal(calls.createTree.length, 0);
  assert.equal(calls.updateRef.length, 0);
});

test('prune drops only stale dirs, keeping recent ones', async () => {
  // Use a fixed clock via a blob list where staleness is unambiguous. The
  // helper reads Date.now(); "old" is 2020, guaranteed >30d before any run.
  const { github, calls } = fakeGithub({
    blobs: ['pr-1/20200101-abc-1.1/x.png', 'pr-2/29990101-def-1.1/y.png'],
  });
  const core = fakeCore();
  await prune({ github, context, core });
  assert.equal(calls.createTree.length, 1);
  const written = calls.createTree[0].tree.map((e) => e.path);
  assert.deepEqual(written, ['pr-2/29990101-def-1.1/y.png']);
  assert.equal('base_tree' in calls.createTree[0], false);
});

test('prune no-ops when nothing is stale', async () => {
  const { github, calls } = fakeGithub({ blobs: ['pr-2/29990101-def-1.1/y.png'] });
  const core = fakeCore();
  await prune({ github, context, core });
  assert.equal(calls.createTree.length, 0);
  assert.equal(calls.updateRef.length, 0);
});
