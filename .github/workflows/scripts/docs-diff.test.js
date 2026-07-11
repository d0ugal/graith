'use strict';

// Unit tests for the docs-diff row-alignment logic. Run with the Node built-in
// test runner (no dependencies): `node --test`.
//
// These cover the pure functions (hashRows / myersOps / diffRows / buildHunks);
// the pngjs image shell (renderDiff / main) is exercised end-to-end by the
// docs-preview workflow, not here.

const { test } = require('node:test');
const assert = require('node:assert/strict');

const {
  hashRows,
  myersOps,
  diffRows,
  denoiseSegments,
  buildHunks,
  renderDiff,
} = require('./docs-diff.js');

// Build a fake decoded image of `rows.length` rows, each row a solid colour
// given by its byte value, so distinct values hash to distinct rows.
function img(rows, width = 2) {
  const height = rows.length;
  const data = Buffer.alloc(width * height * 4);
  for (let y = 0; y < height; y++) {
    for (let x = 0; x < width; x++) {
      const i = (y * width + x) * 4;
      data[i] = data[i + 1] = data[i + 2] = rows[y];
      data[i + 3] = 255;
    }
  }
  return { width, height, data };
}

test('hashRows returns one hash per row; identical rows hash equal', () => {
  const rows = hashRows(img([1, 1, 2, 3, 3]));
  assert.equal(rows.length, 5);
  assert.equal(rows[0], rows[1]); // both value 1
  assert.equal(rows[3], rows[4]); // both value 3
  assert.notEqual(rows[0], rows[2]); // 1 vs 2
});

test('diffRows: identical pages have no change segments', () => {
  const a = hashRows(img([1, 2, 3, 4, 5]));
  assert.deepEqual(diffRows(a, a), []);
});

test('diffRows: a mid-page insertion is one hunk and realigns after it', () => {
  // "canny" base; head inserts two braw rows in the middle. Everything below
  // shifts down, but the diff must not flag the shifted tail as changed.
  const base = hashRows(img([1, 2, 3, 4, 5, 6]));
  const head = hashRows(img([1, 2, 3, 91, 92, 4, 5, 6]));
  const segs = diffRows(base, head);
  assert.equal(segs.length, 1);
  // Pure insertion: empty base range, two head rows (indices 3..5).
  assert.deepEqual(segs[0].base, [3, 3]);
  assert.deepEqual(segs[0].head, [3, 5]);
});

test('diffRows: a replaced row is a change with both ranges non-empty', () => {
  const base = hashRows(img([1, 2, 3, 4, 5]));
  const head = hashRows(img([1, 2, 99, 4, 5]));
  const segs = diffRows(base, head);
  assert.equal(segs.length, 1);
  assert.deepEqual(segs[0].base, [2, 3]);
  assert.deepEqual(segs[0].head, [2, 3]);
});

test('diffRows: two disjoint edits produce two separate segments', () => {
  const base = hashRows(img([1, 2, 3, 4, 5, 6, 7, 8]));
  const head = hashRows(img([1, 99, 3, 4, 5, 6, 88, 8]));
  const segs = diffRows(base, head);
  assert.equal(segs.length, 2);
  assert.deepEqual(segs[0].head, [1, 2]);
  assert.deepEqual(segs[1].head, [6, 7]);
});

test('diffRows: a pure deletion has an empty head range', () => {
  const base = hashRows(img([1, 2, 3, 4, 5]));
  const head = hashRows(img([1, 2, 5]));
  const segs = diffRows(base, head);
  assert.equal(segs.length, 1);
  assert.deepEqual(segs[0].base, [2, 4]); // rows 3 and 4 removed
  assert.deepEqual(segs[0].head, [2, 2]);
});

test('myersOps returns null past the difference cap (dreich global change)', () => {
  // Nothing in common — every row differs. With a tiny cap, alignment bails.
  const base = hashRows(img([1, 2, 3, 4]));
  const head = hashRows(img([5, 6, 7, 8]));
  assert.equal(myersOps(base, head, 2), null);
});

test('diffRows: a fully-divergent page falls back to one all-covering hunk', () => {
  const base = hashRows(img([1, 2, 3, 4]));
  const head = hashRows(img([5, 6, 7, 8]));
  const segs = diffRows(base, head, { maxD: 2 });
  assert.equal(segs.length, 1);
  assert.deepEqual(segs[0].base, [0, 4]);
  assert.deepEqual(segs[0].head, [0, 4]);
});

test('denoiseSegments drops sub-line reflow jitter but keeps real edits', () => {
  const segs = [
    { base: [100, 140], head: [100, 180] }, // real edit, 40px band
    { base: [900, 901], head: [900, 902] }, // 1–2px jitter
    { base: [1500, 1502], head: [1568, 1568] }, // 2px jitter
  ];
  const kept = denoiseSegments(segs, { minRun: 4 });
  assert.equal(kept.length, 1);
  assert.deepEqual(kept[0].head, [100, 180]);
});

test('denoiseSegments keeps a short-in-one-column change (a pure insertion)', () => {
  // Empty base range but a tall head range — a real insertion, not jitter.
  const kept = denoiseSegments([{ base: [50, 50], head: [50, 90] }], { minRun: 4 });
  assert.equal(kept.length, 1);
});

test('denoiseSegments with minRun 0 is a no-op', () => {
  const segs = [{ base: [1, 2], head: [1, 2] }];
  assert.deepEqual(denoiseSegments(segs, { minRun: 0 }), segs);
});

test('buildHunks pads a segment and clamps to image bounds', () => {
  const segs = [{ base: [50, 52], head: [50, 54] }];
  const hunks = buildHunks(segs, { baseHeight: 100, headHeight: 100, padding: 10 });
  assert.equal(hunks.length, 1);
  assert.deepEqual(hunks[0].base, [40, 62]);
  assert.deepEqual(hunks[0].head, [40, 64]);
});

test('buildHunks clamps padding at the top edge (no negative rows)', () => {
  const segs = [{ base: [2, 3], head: [2, 3] }];
  const hunks = buildHunks(segs, { baseHeight: 100, headHeight: 100, padding: 40 });
  assert.deepEqual(hunks[0].base, [0, 43]);
});

test('buildHunks merges segments that overlap after padding', () => {
  const segs = [
    { base: [10, 11], head: [10, 11] },
    { base: [30, 31], head: [30, 31] },
  ];
  // padding 15 makes [0..26] and [15..46] overlap → one hunk [0..46].
  const merged = buildHunks(segs, { baseHeight: 200, headHeight: 200, padding: 15 });
  assert.equal(merged.length, 1);
  assert.deepEqual(merged[0].head, [0, 46]);
});

test('buildHunks keeps distant segments separate', () => {
  const segs = [
    { base: [10, 11], head: [10, 11] },
    { base: [100, 101], head: [100, 101] },
  ];
  const merged = buildHunks(segs, { baseHeight: 300, headHeight: 300, padding: 10 });
  assert.equal(merged.length, 2);
});

test('buildHunks on no segments returns nothing', () => {
  assert.deepEqual(buildHunks([], { baseHeight: 10, headHeight: 10 }), []);
});

test('hashRow key distinguishes visually different rows (two-lane hash)', () => {
  const rows = hashRows(img([10, 20, 10]));
  assert.equal(typeof rows[0], 'string');
  assert.equal(rows[0], rows[2]);
  assert.notEqual(rows[0], rows[1]);
});

// renderDiff is dependency-free (returns a plain RGBA object), so the blit
// geometry and pixel placement are unit-testable without pngjs.
test('renderDiff sizes the canvas and blits base-left / head-right per band', () => {
  const colW = 4;
  const base = img(new Array(30).fill(0).map((_, y) => (y % 2 ? 100 : 0)), colW);
  const head = img(new Array(30).fill(0).map((_, y) => (y % 2 ? 200 : 0)), colW);
  const hunks = [{ base: [5, 9], head: [5, 9] }]; // one 4-row band
  const gutter = 12;
  const gap = 20;
  const out = renderDiff(base, head, hunks, { gutter, gap });

  // width = 2 columns + gutter; height = single band height (no gap for 1 band).
  assert.equal(out.width, colW * 2 + gutter);
  assert.equal(out.height, 4);

  const px = (x, y) => {
    const i = (y * out.width + x) * 4;
    return [out.data[i], out.data[i + 1], out.data[i + 2], out.data[i + 3]];
  };
  // Top band row 0 corresponds to base/head row 5 (odd → value 100/200).
  assert.deepEqual(px(0, 0), [100, 100, 100, 255]); // base column
  assert.deepEqual(px(colW + gutter, 0), [200, 200, 200, 255]); // head column
  // The gutter between columns stays background grey.
  assert.deepEqual(px(colW + 1, 0), [0xe2, 0xe2, 0xe2, 0xff]);
});

test('renderDiff pads a shorter column with background, not garbage', () => {
  const base = img(new Array(10).fill(50), 3); // base band will be 2 rows
  const head = img(new Array(10).fill(60), 3); // head band will be 5 rows
  const out = renderDiff(base, head, [{ base: [0, 2], head: [0, 5] }], { gutter: 4, gap: 0 });
  assert.equal(out.height, 5); // max of the two band heights
  // Base column beyond its 2 rows (row 3) is background grey, not copied pixels.
  const i = (3 * out.width + 0) * 4;
  assert.deepEqual([out.data[i], out.data[i + 1], out.data[i + 2]], [0xe2, 0xe2, 0xe2]);
});
