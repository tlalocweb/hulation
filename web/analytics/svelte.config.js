import adapter from '@sveltejs/adapter-static';
import { vitePreprocess } from '@sveltejs/vite-plugin-svelte';

// SvelteKit config. Targets static adapter so `pnpm build` emits a
// self-contained asset tree under `build/` that hula embeds into the
// Docker image (stage 2.8).
//
// Paths base="/analytics" because hula serves the UI at
// https://<host>/analytics/* (see server/unified_analytics_ui.go).
/** @type {import('@sveltejs/kit').Config} */
const config = {
  preprocess: vitePreprocess(),
  kit: {
    adapter: adapter({
      pages: 'build',
      assets: 'build',
      fallback: 'index.html', // SPA fallback — SvelteKit client router owns every route
      precompress: false,
      strict: true,
    }),
    paths: {
      base: process.env.HULA_UI_BASE ?? '/analytics',
    },
  },
};

export default config;
