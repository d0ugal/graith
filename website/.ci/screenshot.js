import { browser } from 'k6/browser';

// Screenshots a list of pages at desktop + mobile widths.
// Config via env:
//   BASE_URL  base of the served site (default http://localhost:8080)
//   PAGES     JSON array of { "url": "/docs/architecture/", "name": "architecture" }
export const options = {
  scenarios: {
    ui: {
      executor: 'shared-iterations',
      options: { browser: { type: 'chromium' } },
    },
  },
};

const BASE = (__ENV.BASE_URL || 'http://localhost:8080').replace(/\/$/, '');
const PAGES = JSON.parse(__ENV.PAGES || '[]');

const VIEWPORTS = [
  { label: 'desktop', width: 1280, height: 900 },
  { label: 'mobile', width: 390, height: 844 },
];

async function shoot(page, url, name, vp) {
  await page.setViewportSize({ width: vp.width, height: vp.height });
  await page.goto(`${BASE}${url}`, { waitUntil: 'networkidle' });
  // Wait for web fonts to load.
  await page.evaluate(() => document.fonts && document.fonts.ready);
  // Wait for any Mermaid diagrams to finish rendering to SVG.
  await page.evaluate(() => {
    const blocks = () => Array.from(document.querySelectorAll('.mermaid'));
    if (blocks().length === 0) return true;
    return new Promise((resolve) => {
      const start = Date.now();
      const check = () => {
        const done = blocks().every((el) => el.querySelector('svg'));
        if (done || Date.now() - start > 8000) resolve(true);
        else setTimeout(check, 100);
      };
      check();
    });
  });
  await page.screenshot({ path: `/out/${name}-${vp.label}.png`, fullPage: true });
}

export default async function () {
  const page = await browser.newPage();
  try {
    for (const p of PAGES) {
      for (const vp of VIEWPORTS) {
        await shoot(page, p.url, p.name, vp);
      }
    }
  } finally {
    await page.close();
  }
}
