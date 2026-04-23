<script lang="ts">
  // Stage 2.1 landing scaffold — populated with real Overview UI in
  // stage 2.4. Visible enough that `pnpm build` has something to
  // render, and the theme + Tailwind layers are exercised.
  import { version } from '$app/environment';
</script>

<main class="mx-auto max-w-5xl px-6 py-20">
  <header class="mb-10">
    <h1 class="text-4xl font-semibold tracking-tight">Hula Analytics</h1>
    <p class="mt-2 text-muted-foreground">
      Phase 2 scaffolding. Reports land in stages 2.4–2.7.
    </p>
  </header>

  <section class="grid grid-cols-1 gap-4 md:grid-cols-2">
    <article class="rounded-lg border bg-card p-6 text-card-foreground">
      <h2 class="mb-1 text-lg font-medium">Backend</h2>
      <p class="text-sm text-muted-foreground">
        Analytics API at <code class="font-mono text-xs">/api/v1/analytics/*</code> —
        Phase 1 is live.
      </p>
    </article>
    <article class="rounded-lg border bg-card p-6 text-card-foreground">
      <h2 class="mb-1 text-lg font-medium">Build</h2>
      <p class="text-sm text-muted-foreground">
        SvelteKit static adapter; version
        <code class="font-mono text-xs">{version}</code>.
      </p>
    </article>
  </section>
</main>
