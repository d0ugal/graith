'use strict';

// Shared logic for the docs-preview `cleanup` (on PR close) and `prune`
// (scheduled) jobs. Extracted from inline `github-script` blocks so the
// destructive branch-rewrite path is unit-testable (see docs-preview.test.js).
//
// Both jobs rebuild the orphan `screenshots` branch WITHOUT a `base_tree`, so
// the new tree is exactly the blob list we hand to createTree — any path we
// fail to list is deleted. That makes a complete, trustworthy blob listing a
// safety invariant, which `listBlobsOrFail` enforces (issue #763).

const SCREENSHOTS_BRANCH = 'screenshots';
const STICKY_MARKER = '<!-- docs-preview -->';
const MAX_AGE_MS = 30 * 24 * 60 * 60 * 1000; // 30 days

// Fetch every blob in the screenshots tree, failing closed if GitHub
// truncated the recursive response. `getTree(recursive: true)` caps large
// trees; because cleanup/prune rebuild the branch with no `base_tree`, any
// path omitted from a truncated page would be silently deleted — destroying
// unrelated PRs' screenshots. Refuse to proceed rather than rebuild from
// partial data (issue #763).
async function listBlobsOrFail({ github, owner, repo, treeSha }) {
  const full = await github.rest.git.getTree({
    owner,
    repo,
    tree_sha: treeSha,
    recursive: 'true',
  });
  if (full.data.truncated) {
    throw new Error(
      'Screenshots tree was truncated by the GitHub API — refusing to rebuild ' +
        'the branch from a partial listing, which would delete every omitted ' +
        'screenshot (issue #763).',
    );
  }
  return full.data.tree.filter((e) => e.type === 'blob');
}

// A path looks like pr-<num>/<YYYYMMDD>-<sha>-<run>.<attempt>/<file>. Decide
// staleness from the leading YYYYMMDD of the run-dir. Paths that don't carry a
// parseable date are left untouched.
function isStaleRunDir(p, now, maxAgeMs = MAX_AGE_MS) {
  const seg = p.split('/');
  if (seg.length < 3 || !seg[0].startsWith('pr-')) return false;
  const m = seg[1].match(/^(\d{4})(\d{2})(\d{2})-/);
  if (!m) return false;
  const t = Date.parse(`${m[1]}-${m[2]}-${m[3]}T00:00:00Z`);
  if (Number.isNaN(t)) return false;
  return now - t > maxAgeMs;
}

// Look up the tip of the screenshots branch, returning null if the branch
// doesn't exist (nothing to clean up / prune).
async function getBranchTip({ github, owner, repo }) {
  let ref;
  try {
    ref = await github.rest.git.getRef({ owner, repo, ref: `heads/${SCREENSHOTS_BRANCH}` });
  } catch (e) {
    if (e.status === 404) return null;
    throw e;
  }
  const commitSha = ref.data.object.sha;
  const commit = await github.rest.git.getCommit({ owner, repo, commit_sha: commitSha });
  return { commitSha, treeSha: commit.data.tree.sha };
}

// Rebuild the screenshots branch to contain exactly `kept`. The tree is built
// with no `base_tree`, so this is a full replacement — hence `kept` must be a
// complete listing (guaranteed by listBlobsOrFail).
async function rewriteBranch({ github, owner, repo, kept, parentSha, message }) {
  const tree = kept.map((e) => ({ path: e.path, mode: e.mode, type: e.type, sha: e.sha }));
  const newTree = await github.rest.git.createTree({ owner, repo, tree });
  const newCommit = await github.rest.git.createCommit({
    owner,
    repo,
    message,
    tree: newTree.data.sha,
    parents: [parentSha],
  });
  await github.rest.git.updateRef({
    owner,
    repo,
    ref: `heads/${SCREENSHOTS_BRANCH}`,
    sha: newCommit.data.sha,
  });
}

// Remove one PR's screenshots on close, then note the cleanup in the sticky
// comment.
async function cleanup({ github, context, core }) {
  const { owner, repo } = context.repo;
  const pr = context.payload.pull_request.number;
  const prefix = `pr-${pr}/`;

  const tip = await getBranchTip({ github, owner, repo });
  if (!tip) {
    core.info(`No ${SCREENSHOTS_BRANCH} branch — nothing to clean up.`);
    return;
  }

  const blobs = await listBlobsOrFail({ github, owner, repo, treeSha: tip.treeSha });
  const kept = blobs.filter((e) => !e.path.startsWith(prefix));

  if (kept.length === blobs.length) {
    core.info(`No screenshots found under ${prefix} — nothing to clean up.`);
    return;
  }

  await rewriteBranch({
    github,
    owner,
    repo,
    kept,
    parentSha: tip.commitSha,
    message: `docs preview: clean up PR #${pr}`,
  });
  core.info(`Removed ${blobs.length - kept.length} screenshot(s) for PR #${pr}.`);

  // Update the sticky comment to note the previews were cleaned up.
  const comments = await github.paginate(github.rest.issues.listComments, {
    owner,
    repo,
    issue_number: pr,
  });
  const existing = comments.find((c) => c.body && c.body.includes(STICKY_MARKER));
  if (existing) {
    const body = `${STICKY_MARKER}\n### 📸 Docs preview\n\n_Preview screenshots for this PR were cleaned up after it was closed._`;
    await github.rest.issues.updateComment({ owner, repo, comment_id: existing.id, body });
  }
}

// Remove screenshot dirs older than MAX_AGE_MS on the daily schedule.
async function prune({ github, context, core }) {
  const { owner, repo } = context.repo;

  const tip = await getBranchTip({ github, owner, repo });
  if (!tip) {
    core.info(`No ${SCREENSHOTS_BRANCH} branch — nothing to prune.`);
    return;
  }

  const blobs = await listBlobsOrFail({ github, owner, repo, treeSha: tip.treeSha });

  const now = Date.now();
  const kept = blobs.filter((e) => !isStaleRunDir(e.path, now));
  const removed = blobs.length - kept.length;
  if (removed === 0) {
    core.info('No stale screenshot dirs to prune.');
    return;
  }

  await rewriteBranch({
    github,
    owner,
    repo,
    kept,
    parentSha: tip.commitSha,
    message: `docs preview: prune ${removed} stale screenshot(s)`,
  });
  core.info(`Pruned ${removed} screenshot(s) older than 30 days.`);
}

module.exports = {
  SCREENSHOTS_BRANCH,
  STICKY_MARKER,
  MAX_AGE_MS,
  listBlobsOrFail,
  isStaleRunDir,
  getBranchTip,
  rewriteBranch,
  cleanup,
  prune,
};
