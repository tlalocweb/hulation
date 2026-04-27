import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vitest/config';

export default defineConfig({
  plugins: [sveltekit()],
  test: {
    include: ['src/**/*.{test,spec}.{js,ts}'],
    environment: 'node',
  },
  // Dev server proxies /api/v1/* to a locally running hula so `pnpm dev`
  // hits real data without CORS. Override via HULA_API_URL env var when
  // pointing at the e2e stack or a remote cluster.
  server: {
    proxy: {
      '/api': {
        target: process.env.HULA_API_URL ?? 'https://localhost:8443',
        changeOrigin: true,
        secure: false,
      },
    },
  },
});
