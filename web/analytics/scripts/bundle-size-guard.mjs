#!/usr/bin/env node
// Fails with a non-zero exit when the gzipped first-load bundle
// exceeds the Phase-2 budget. Runs after `pnpm build`.
//
// First-load = the JS + CSS shipped for the initial route (/). We
// approximate by summing the entry chunks + the landing-page node
// chunk. Dynamic chunks (d3-geo + world-atlas, loaded only on
// /geography) don't count against the first-load number.

import { readFileSync, readdirSync, statSync } from 'fs';
import { gzipSync } from 'zlib';
import { join } from 'path';

const BUDGET_KB = 150;
const BUILD_DIR = 'build';

function walk(dir) {
  const out = [];
  for (const entry of readdirSync(dir)) {
    const full = join(dir, entry);
    const s = statSync(full);
    if (s.isDirectory()) out.push(...walk(full));
    else out.push(full);
  }
  return out;
}

const files = walk(BUILD_DIR);

// Read the root HTML to find which chunks are in the first load.
let indexHtml;
try {
  indexHtml = readFileSync(join(BUILD_DIR, 'index.html'), 'utf-8');
} catch (err) {
  console.error(`bundle-size-guard: index.html missing — run pnpm build first`);
  process.exit(1);
}

const firstLoadPaths = new Set();
// Match src="/analytics/_app/..." and href="/analytics/_app/..."
for (const m of indexHtml.matchAll(/["']\/analytics\/(_app\/[^"']+)["']/g)) {
  firstLoadPaths.add(m[1]);
}
// Include index.html itself in the first-load cost.
firstLoadPaths.add('index.html');

let total = 0;
for (const path of firstLoadPaths) {
  const full = join(BUILD_DIR, path);
  try {
    const raw = readFileSync(full);
    total += gzipSync(raw).length;
  } catch {
    // Missing — ignore (could be a comment match).
  }
}

const kb = total / 1024;
const budgetBytes = BUDGET_KB * 1024;

if (total > budgetBytes) {
  console.error(
    `bundle-size-guard: first-load gzipped ${kb.toFixed(1)} KB exceeds budget ${BUDGET_KB} KB`
  );
  console.error(`  chunks counted:`);
  for (const path of firstLoadPaths) {
    try {
      const size = gzipSync(readFileSync(join(BUILD_DIR, path))).length;
      console.error(`    ${(size / 1024).toFixed(1).padStart(6)} KB  ${path}`);
    } catch {
      /* ignore */
    }
  }
  process.exit(1);
}

console.log(
  `bundle-size-guard: first-load gzipped ${kb.toFixed(1)} KB (budget ${BUDGET_KB} KB) — ${firstLoadPaths.size} chunk${
    firstLoadPaths.size === 1 ? '' : 's'
  }`
);
