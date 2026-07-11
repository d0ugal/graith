'use strict';

// Row-level visual diff for the docs-preview screenshots.
//
// The docs-preview workflow renders each changed page twice in the same job —
// once at the PR's base commit ("base") and once at its head ("head") — with
// the same pinned Hugo + Chromium, so unchanged content renders byte-identical
// between the two. A full-page doc screenshot is very tall, and a one-line
// edit is a needle in that haystack (and a naive whole-image pixel diff is
// worse than useless: adding a line shifts everything below it down, so every
// row after the edit reads as "changed").
//
// So we diff the *rows* of the two images the way `git diff` diffs the lines
// of a file: hash every pixel row, run a Myers diff over the row-hashes, and it
// realigns after an insertion. The changed rows (plus a few rows of context
// padding) become "hunks", and we crop just those bands out of both images and
// stitch them base-|-head, collapsing the identical bulk.
//
// One real-world wrinkle (measured against the actual k6 render, not just
// synthetic fixtures): identical content renders byte-for-byte identically, but
// once an edit reflows the page, content below it doesn't shift by a whole-pixel
// amount everywhere — browser text layout rounds line positions per-line, so a
// scatter of visually-identical rows land ±1px off and fail to byte-match. Left
// alone that fragments a one-line edit into dozens of phantom 1–2px "changes"
// spread across the whole page. denoiseSegments drops those; see its comment.
//
// The alignment logic (hashRows / myersOps / diffRows / buildHunks) is pure and
// unit-tested with `node --test` (see docs-diff.test.js) — no dependencies. The
// only part that needs an image library (`pngjs`) is the thin decode / crop /
// encode shell in renderDiff + main, which requires it lazily so the tests stay
// dependency-free.

// FNV-1a hash of one image row's RGBA bytes. Cheap, well-distributed, and
// collision-resistant enough that two visually different rows practically never
// share a hash — good enough to treat matching hashes as "identical row".
function hashRow(data, start, end) {
  let h = 0x811c9dc5;
  for (let i = start; i < end; i++) {
    h ^= data[i];
    // h *= 16777619, kept in 32-bit unsigned via Math.imul.
    h = Math.imul(h, 0x01000193);
  }
  return h >>> 0;
}

// Hash every row of a decoded image ({ width, height, data }, data = RGBA
// Buffer/Uint8Array of length width*height*4). Returns an array of per-row
// hashes, length === height.
function hashRows({ width, height, data }) {
  const stride = width * 4;
  const rows = new Array(height);
  for (let y = 0; y < height; y++) {
    rows[y] = hashRow(data, y * stride, y * stride + stride);
  }
  return rows;
}

// Myers O(ND) diff over two arrays of row hashes. Returns a forward-order array
// of edit tokens: 'eq' (row present in both), 'del' (row only in `a`/base),
// 'ins' (row only in `b`/head). Returns null when the number of differences
// exceeds `maxD` — the signal that the images diverge so heavily (e.g. a global
// theme change repaints every row) that a single all-encompassing hunk is the
// honest answer, and it isn't worth the O(D^2) trace memory to prove it.
function myersOps(a, b, maxD) {
  const N = a.length;
  const M = b.length;
  const MAX = N + M;
  if (MAX === 0) return [];
  const cap = Math.min(maxD == null ? MAX : maxD, MAX);
  // Size the V array so k-1 and k+1 never fall out of range at d === cap.
  const offset = cap + 1;
  const size = 2 * cap + 3;
  let v = new Int32Array(size);
  const trace = [];
  let found = -1;

  for (let d = 0; d <= cap; d++) {
    trace.push(v.slice());
    for (let k = -d; k <= d; k += 2) {
      const kIdx = k + offset;
      let x;
      if (k === -d || (k !== d && v[kIdx - 1] < v[kIdx + 1])) {
        x = v[kIdx + 1]; // move down: insertion of b[y]
      } else {
        x = v[kIdx - 1] + 1; // move right: deletion of a[x]
      }
      let y = x - k;
      while (x < N && y < M && a[x] === b[y]) {
        x++;
        y++;
      }
      v[kIdx] = x;
      if (x >= N && y >= M) {
        found = d;
        break;
      }
    }
    if (found >= 0) break;
  }

  if (found < 0) return null; // exceeded the difference cap

  // Backtrack the trace into a forward-order token list.
  const ops = [];
  let x = N;
  let y = M;
  for (let d = found; d > 0; d--) {
    const vd = trace[d];
    const k = x - y;
    const kIdx = k + offset;
    let prevK;
    if (k === -d || (k !== d && vd[kIdx - 1] < vd[kIdx + 1])) {
      prevK = k + 1; // came from down (insertion)
    } else {
      prevK = k - 1; // came from right (deletion)
    }
    const prevX = vd[prevK + offset];
    const prevY = prevX - prevK;
    while (x > prevX && y > prevY) {
      ops.push('eq');
      x--;
      y--;
    }
    if (x === prevX) {
      ops.push('ins');
    } else {
      ops.push('del');
    }
    x = prevX;
    y = prevY;
  }
  // d === 0: the remaining leading diagonal is all matches.
  while (x > 0 && y > 0) {
    ops.push('eq');
    x--;
    y--;
  }
  ops.reverse();
  return ops;
}

// Diff two row-hash arrays into change segments. Each segment is
// { base: [start, end), head: [start, end) } in full-image row coordinates: the
// base rows deleted/replaced and the head rows inserted/replaced. A pure
// insertion has an empty base range; a pure deletion an empty head range.
//
// Common prefix/suffix rows are trimmed before the Myers pass so a single edit
// in a tall page runs the expensive diff over only the handful of rows that
// actually moved.
function diffRows(base, head, { maxD = 1500 } = {}) {
  const nb = base.length;
  const nh = head.length;
  const minN = Math.min(nb, nh);

  let p = 0;
  while (p < minN && base[p] === head[p]) p++;
  let s = 0;
  while (s < minN - p && base[nb - 1 - s] === head[nh - 1 - s]) s++;

  const bMid = base.slice(p, nb - s);
  const hMid = head.slice(p, nh - s);
  if (bMid.length === 0 && hMid.length === 0) return [];

  const ops = myersOps(bMid, hMid, maxD);
  if (ops === null) {
    // Too divergent to align cheaply — treat the whole differing middle as one
    // change spanning both images.
    return [{ base: [p, nb - s], head: [p, nh - s] }];
  }

  const segs = [];
  let bi = p;
  let hi = p;
  let cur = null;
  for (const op of ops) {
    if (op === 'eq') {
      if (cur) {
        segs.push(cur);
        cur = null;
      }
      bi++;
      hi++;
    } else if (op === 'del') {
      if (!cur) cur = { base: [bi, bi], head: [hi, hi] };
      bi++;
      cur.base[1] = bi;
    } else {
      if (!cur) cur = { base: [bi, bi], head: [hi, hi] };
      hi++;
      cur.head[1] = hi;
    }
  }
  if (cur) segs.push(cur);
  return segs;
}

// Drop change segments shorter than `minRun` rows in both columns.
//
// Editing a page reflows everything below the edit, and browser text layout
// doesn't reposition on a whole-pixel grid — a line that shifts down by N
// pixels can land ±1px off where exact-row alignment expects it, so a scatter
// of visually-identical rows across the whole page fail to byte-match and show
// up as 1–2px "changes" (confirmed empirically: a one-paragraph insertion
// produced ~30 such phantom segments spread over 6000px). A real content change
// is at least a line tall (~15px+ at these fonts), so a small floor cleanly
// separates genuine edits from reflow jitter. The trade-off: a real change
// under `minRun` px (e.g. recolouring a 2px rule) is dropped — rare for a
// content edit, and such geometry-only tweaks are usually theme/CSS changes,
// which take the all-pages global path where a fuller diff is expected anyway.
function denoiseSegments(segs, { minRun = 4 } = {}) {
  if (minRun <= 0) return segs;
  return segs.filter(
    (s) => Math.max(s.base[1] - s.base[0], s.head[1] - s.head[0]) >= minRun,
  );
}

// Expand each change segment by `padding` rows of context on both sides, clamp
// to image bounds, and merge segments that overlap (or touch) after padding —
// so two edits a few rows apart become one readable band, not two abutting
// ones. Returns hunks sorted top-to-bottom.
function buildHunks(segs, { baseHeight, headHeight, padding = 40 } = {}) {
  if (segs.length === 0) return [];
  const expanded = segs.map((seg) => ({
    base: [Math.max(0, seg.base[0] - padding), Math.min(baseHeight, seg.base[1] + padding)],
    head: [Math.max(0, seg.head[0] - padding), Math.min(headHeight, seg.head[1] + padding)],
  }));
  expanded.sort((a, b) => a.head[0] - b.head[0] || a.base[0] - b.base[0]);

  const merged = [expanded[0]];
  for (let i = 1; i < expanded.length; i++) {
    const h = expanded[i];
    const last = merged[merged.length - 1];
    // Merge when either column's band overlaps or abuts the previous hunk's.
    if (h.head[0] <= last.head[1] || h.base[0] <= last.base[1]) {
      last.head[0] = Math.min(last.head[0], h.head[0]);
      last.head[1] = Math.max(last.head[1], h.head[1]);
      last.base[0] = Math.min(last.base[0], h.base[0]);
      last.base[1] = Math.max(last.base[1], h.base[1]);
    } else {
      merged.push(h);
    }
  }
  return merged;
}

// --- Image shell (needs pngjs) -------------------------------------------

// Composite the hunks into a single PNG: for each hunk a base-|-head row band,
// stacked top to bottom with a gutter between the two columns and a gap between
// hunks. `basePng`/`headPng` are pngjs PNG instances (or any { width, height,
// data } with an RGBA Buffer). Returns a new PNG instance.
function renderDiff(basePng, headPng, hunks, { gutter = 12, gap = 20 } = {}) {
  const { PNG } = require('pngjs');
  const colW = Math.max(basePng.width, headPng.width);
  const outW = colW * 2 + gutter;

  const bands = hunks.map((h) => ({
    base: h.base,
    head: h.head,
    height: Math.max(h.base[1] - h.base[0], h.head[1] - h.head[0]),
  }));
  const outH = bands.reduce((sum, b) => sum + b.height, 0) + gap * Math.max(0, bands.length - 1);

  const out = new PNG({ width: outW, height: Math.max(1, outH) });
  // Fill background: light gutter/gap grey (#e2e2e2), opaque.
  for (let i = 0; i < out.data.length; i += 4) {
    out.data[i] = 0xe2;
    out.data[i + 1] = 0xe2;
    out.data[i + 2] = 0xe2;
    out.data[i + 3] = 0xff;
  }

  const blit = (src, srcRow0, srcRow1, destX, destY, rows) => {
    const srcStride = src.width * 4;
    const dstStride = out.width * 4;
    const copyBytes = Math.min(src.width, colW) * 4;
    for (let r = 0; r < rows; r++) {
      const sr = srcRow0 + r;
      if (sr >= srcRow1 || sr >= src.height) break;
      const srcStart = sr * srcStride;
      const dstStart = (destY + r) * dstStride + destX * 4;
      src.data.copy(out.data, dstStart, srcStart, srcStart + copyBytes);
    }
  };

  let y = 0;
  for (const b of bands) {
    blit(basePng, b.base[0], b.base[1], 0, y, b.height);
    blit(headPng, b.head[0], b.head[1], colW + gutter, y, b.height);
    y += b.height + gap;
  }
  return out;
}

// CLI: node docs-diff.js <base.png> <head.png> <out.png>
// Exit 0 and write the diff PNG when there are changes; exit 3 (and write
// nothing) when the two renders are pixel-identical, so the caller can skip
// publishing a diff for an unchanged page.
function main(argv) {
  const [basePath, headPath, outPath] = argv;
  if (!basePath || !headPath || !outPath) {
    process.stderr.write('usage: docs-diff.js <base.png> <head.png> <out.png>\n');
    return 2;
  }
  const fs = require('fs');
  const { PNG } = require('pngjs');
  const base = PNG.sync.read(fs.readFileSync(basePath));
  const head = PNG.sync.read(fs.readFileSync(headPath));
  const segs = denoiseSegments(diffRows(hashRows(base), hashRows(head)));
  if (segs.length === 0) return 3; // identical (or only reflow jitter) — nothing to show
  const hunks = buildHunks(segs, { baseHeight: base.height, headHeight: head.height });
  const out = renderDiff(base, head, hunks);
  fs.writeFileSync(outPath, PNG.sync.write(out));
  return 0;
}

if (require.main === module) {
  process.exitCode = main(process.argv.slice(2));
}

module.exports = {
  hashRow,
  hashRows,
  myersOps,
  diffRows,
  denoiseSegments,
  buildHunks,
  renderDiff,
  main,
};
