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
  // Wait for web fonts so code blocks / box-drawing glyphs render.
  await page.evaluate(() => document.fonts && document.fonts.ready);
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
