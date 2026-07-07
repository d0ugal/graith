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

const {
  listBlobsOrFail,
  isStaleRunDir,
  commitToBranch,
  cleanup,
  prune,
} = require('./docs-preview.js');

const http = (status) => Object.assign(new Error(`HTTP ${status}`), { status });

// A github stub for commitToBranch's compare-and-retry (issue #765). It tracks
// a single branch tip (null = absent) and can be told to fail the next N
// updateRef/createRef calls with a 422 — the non-fast-forward race that
// interleaved docs-preview writers hit in production.
function raceGithub({ tip = null } = {}) {
  const calls = { getRef: 0, getCommit: 0, createRef: 0, updateRef: 0 };
  const state = { tip };
  let updateFails = 0;
  let createFails = 0;
  return {
    calls,
    state,
    setUpdateRefFailures(n) { updateFails = n; },
    setCreateRefFailures(n) { createFails = n; },
    rest: {
      git: {
        getRef: async () => {
          calls.getRef++;
          if (state.tip == null) throw http(404);
          return { data: { object: { sha: state.tip } } };
        },
        getCommit: async ({ commit_sha }) => {
          calls.getCommit++;
          return { data: { tree: { sha: `tree@${commit_sha}` } } };
        },
        createRef: async ({ sha }) => {
          calls.createRef++;
          if (createFails > 0) {
            createFails--;
            state.tip = 'created-by-another-writer'; // someone else won
            throw http(422);
          }
          state.tip = sha;
        },
        updateRef: async ({ sha }) => {
          calls.updateRef++;
          if (updateFails > 0) {
            updateFails--;
            throw http(422);
          }
          state.tip = sha;
        },
      },
    },
  };
}

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

// --- commitToBranch compare-and-retry (#765) -------------------------------

const owner = 'clachan';
const repo = 'croft';

test('commitToBranch creates the branch when absent and createIfAbsent is set', async () => {
  const github = raceGithub({ tip: null });
  const seen = [];
  const result = await commitToBranch({
    github,
    owner,
    repo,
    createIfAbsent: true,
    buildCommit: async (tip) => { seen.push(tip); return 'commit-1'; },
  });
  assert.equal(result.outcome, 'created');
  assert.equal(github.calls.createRef, 1);
  assert.equal(github.calls.updateRef, 0);
  assert.equal(github.state.tip, 'commit-1');
  assert.deepEqual(seen, [null]);
});

test('commitToBranch returns absent (never builds) when branch missing and createIfAbsent false', async () => {
  const github = raceGithub({ tip: null });
  let built = false;
  const result = await commitToBranch({
    github,
    owner,
    repo,
    buildCommit: async () => { built = true; return 'x'; },
  });
  assert.equal(result.outcome, 'absent');
  assert.equal(built, false);
  assert.equal(github.calls.createRef, 0);
});

test('commitToBranch returns noop when buildCommit signals nothing to do', async () => {
  const github = raceGithub({ tip: 'A' });
  const result = await commitToBranch({ github, owner, repo, buildCommit: async () => null });
  assert.equal(result.outcome, 'noop');
  assert.equal(github.calls.updateRef, 0);
  assert.equal(github.state.tip, 'A');
});

test('commitToBranch, after a lost updateRef race, rebuilds on the winner tip (A -> B)', async () => {
  // The exact #765 failure: a competing writer advanced the branch to B
  // between our read and our write. The retry must re-read B and rebuild on
  // it — not replay the stale A-parented commit.
  const github = raceGithub({ tip: 'A' });
  const realUpdateRef = github.rest.git.updateRef;
  let raced = false;
  github.rest.git.updateRef = async (args) => {
    if (!raced) {
      raced = true;
      github.state.tip = 'B';
      throw http(422);
    }
    return realUpdateRef(args);
  };
  const seen = [];
  const result = await commitToBranch({
    github,
    owner,
    repo,
    buildCommit: async (tip) => { seen.push(tip); return 'commit-onto-B'; },
  });
  assert.equal(result.outcome, 'updated');
  assert.equal(result.attempts, 2);
  assert.deepEqual(seen[0], { commitSha: 'A', treeSha: 'tree@A' });
  assert.deepEqual(seen[1], { commitSha: 'B', treeSha: 'tree@B' }, 'rebuild must chain onto the new tip B');
  assert.equal(github.state.tip, 'commit-onto-B');
});

test('commitToBranch: a lost createRef race retries as an update once the branch exists', async () => {
  const github = raceGithub({ tip: null });
  github.setCreateRefFailures(1);
  const seen = [];
  const result = await commitToBranch({
    github,
    owner,
    repo,
    createIfAbsent: true,
    buildCommit: async (tip) => { seen.push(tip); return 'commit-3'; },
  });
  assert.equal(result.outcome, 'updated');
  assert.equal(result.attempts, 2);
  assert.equal(github.calls.createRef, 1);
  assert.equal(github.calls.updateRef, 1);
  assert.equal(seen[0], null);
  assert.deepEqual(seen[1], { commitSha: 'created-by-another-writer', treeSha: 'tree@created-by-another-writer' });
  assert.equal(github.state.tip, 'commit-3');
});

test('commitToBranch gives up after maxAttempts when the race never resolves', async () => {
  const github = raceGithub({ tip: 'A' });
  github.setUpdateRefFailures(99);
  await assert.rejects(
    commitToBranch({ github, owner, repo, maxAttempts: 3, buildCommit: async () => 'x' }),
    /could not update the screenshots branch after 3 attempts/,
  );
  assert.equal(github.calls.updateRef, 3);
});

test('commitToBranch propagates a non-422 write error without retrying', async () => {
  const github = raceGithub({ tip: 'A' });
  github.rest.git.updateRef = async () => { throw http(500); };
  await assert.rejects(
    commitToBranch({ github, owner, repo, maxAttempts: 5, buildCommit: async () => 'x' }),
    /HTTP 500/,
  );
});

test('commitToBranch propagates a buildCommit throw and never retries it', async () => {
  // e.g. listBlobsOrFail throwing on a truncated tree (#763), or createTree
  // rejecting an empty tree — errors outside the ref-write must surface as-is.
  const github = raceGithub({ tip: 'A' });
  let builds = 0;
  await assert.rejects(
    commitToBranch({
      github,
      owner,
      repo,
      maxAttempts: 5,
      buildCommit: async () => { builds++; throw new Error('tree is empty'); },
    }),
    /tree is empty/,
  );
  assert.equal(builds, 1);
  assert.equal(github.calls.updateRef, 0);
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
