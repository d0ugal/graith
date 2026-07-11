'use strict';

// Batch driver for the docs-preview diff step. For every changed page ×
// viewport it decides what image the PR comment should show and writes it into
// the flat `shots/` dir the publish step reads, plus a `manifest.json`
// describing each entry's kind:
//
//   diff  — the page existed at the base commit and changed visually: a
//           base-|-head composite of just the changed bands (docs-diff.js).
//   same  — existed at base but renders row-identical (a no-op markdown edit):
//           no image, a note in the comment.
//   new   — no base version (page added in this PR): the full head render.
//
// I/O orchestration only; the diff logic it calls lives in docs-diff.js and is
// unit-tested there. Run as:
//   node docs-diff-run.js <pages.json> <baseShotsDir> <headShotsDir> <outDir>

const fs = require('fs');
const path = require('path');
const { PNG } = require('pngjs');
const { hashRows, diffRows, denoiseSegments, buildHunks, renderDiff } = require('./docs-diff.js');

const VIEWPORTS = ['desktop', 'mobile'];

function readPng(p) {
  return PNG.sync.read(fs.readFileSync(p));
}

function main(argv) {
  const [pagesPath, baseDir, headDir, outDir] = argv;
  if (!pagesPath || !baseDir || !headDir || !outDir) {
    process.stderr.write('usage: docs-diff-run.js <pages.json> <baseDir> <headDir> <outDir>\n');
    return 2;
  }
  const pages = JSON.parse(fs.readFileSync(pagesPath, 'utf8'));
  fs.mkdirSync(outDir, { recursive: true });

  const manifest = {};
  for (const page of pages) {
    const entry = {};
    for (const vp of VIEWPORTS) {
      const file = `${page.name}-${vp}.png`;
      const headPath = path.join(headDir, file);
      if (!fs.existsSync(headPath)) continue; // page/viewport not shot
      const basePath = path.join(baseDir, file);
      const hasBase = page.hasBase && fs.existsSync(basePath);

      if (!hasBase) {
        // New page (or missing baseline): show the full head render.
        fs.copyFileSync(headPath, path.join(outDir, file));
        entry[vp] = { kind: 'new', file };
        continue;
      }

      const base = readPng(basePath);
      const head = readPng(headPath);
      const segs = denoiseSegments(diffRows(hashRows(base), hashRows(head)));
      if (segs.length === 0) {
        // Identical, or the only differences were sub-pixel reflow jitter.
        entry[vp] = { kind: 'same' };
        continue;
      }
      const hunks = buildHunks(segs, { baseHeight: base.height, headHeight: head.height });
      const out = renderDiff(base, head, hunks);
      fs.writeFileSync(path.join(outDir, file), PNG.sync.write(out));
      entry[vp] = { kind: 'diff', file };
    }
    if (Object.keys(entry).length > 0) manifest[page.name] = entry;
  }

  fs.writeFileSync(path.join(outDir, 'manifest.json'), JSON.stringify(manifest, null, 2));
  const counts = Object.values(manifest)
    .flatMap((e) => Object.values(e))
    .reduce((acc, v) => ((acc[v.kind] = (acc[v.kind] || 0) + 1), acc), {});
  process.stdout.write(`docs-diff: ${JSON.stringify(counts)}\n`);
  return 0;
}

if (require.main === module) {
  process.exitCode = main(process.argv.slice(2));
}

module.exports = { main };
