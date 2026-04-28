# Hula Analytics UI

Phase 2 — SvelteKit dashboard that consumes the Phase-1
`/api/v1/analytics/*` surface.

## Stack

- SvelteKit 2 + TypeScript
- Tailwind 3.4 (shadcn-svelte theme via CSS variables)
- D3.js for charts (added in stage 2.3)
- Vitest for tests
- `@sveltejs/adapter-static` → `build/` (SPA; hula serves the bundle
  at `/analytics/*`).

## Commands

```bash
pnpm install           # one-time
pnpm dev               # dev server at http://localhost:5173
pnpm build             # static build into build/
pnpm preview           # serve build/ locally
pnpm check             # svelte-check TypeScript + template diagnostics
pnpm test              # vitest one-shot
pnpm test:watch        # vitest watch
```

During `pnpm dev`, `/api/*` is proxied to
`HULA_API_URL` (default `https://localhost:8443`). Run hula locally
first, set `HULA_API_URL` if it listens elsewhere.

## Auth (dev)

Until stage 2.8 ships the login page, paste an admin JWT manually:

```js
// In DevTools console
localStorage.setItem('hula:token', '<paste token here>');
```

`hulactl auth` writes a token at `~/.hula/hulactl.yaml`; extract with:

```bash
awk '/token:/ {print $2; exit}' ~/.hula/hulactl.yaml
```

## Layout (stage 2.1)

Bare scaffold. Real layout + filter bar lands in 2.2.

```
src/
├── app.html          # document shell with inline theme bootstrap
├── app.css           # Tailwind + shadcn CSS-variable theme
├── app.d.ts          # global types
├── lib/
│   └── api/
│       ├── analytics.ts   # typed fetch wrapper
│       ├── analytics.spec.ts
│       └── types.ts
└── routes/
    ├── +layout.svelte
    └── +page.svelte
```
