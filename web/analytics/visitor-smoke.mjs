import { chromium } from 'playwright';

const URL = 'https://www.tlaloc.us/';
const browser = await chromium.launch({ args: ['--ignore-certificate-errors'] });
const ctx = await browser.newContext({ ignoreHTTPSErrors: true });
const page = await ctx.newPage();

const hits = [];
page.on('request', (req) => {
  const u = req.url();
  if (u.includes('/v/') || u.includes('/scripts/hello')) hits.push(`${req.method()} ${u}`);
});

await page.goto(URL, { waitUntil: 'networkidle', timeout: 30000 });
await page.waitForTimeout(3000);
await browser.close();
for (const h of hits) console.log(h);
