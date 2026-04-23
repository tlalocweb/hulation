<script lang="ts">
  // Overview placeholder — real KPI cards + main timeline land in
  // stage 2.4.
  import { filters } from '$lib/filters';
</script>

<section class="space-y-6">
  <header>
    <h1 class="text-2xl font-semibold tracking-tight">Overview</h1>
    <p class="text-sm text-muted-foreground">
      Phase 2 stage 2.2 — filter bar + URL state wired. Reports populate in 2.4.
    </p>
  </header>

  <div class="grid grid-cols-1 gap-4 md:grid-cols-4">
    {#each ['Visitors', 'Pageviews', 'Bounce rate', 'Avg session'] as label}
      <article class="rounded-lg border bg-card p-5 text-card-foreground">
        <p class="text-sm text-muted-foreground">{label}</p>
        <p class="mt-1 text-3xl font-semibold tracking-tight tabular-nums">—</p>
        <p class="mt-2 h-6 text-xs text-muted-foreground">sparkline · stage 2.3</p>
      </article>
    {/each}
  </div>

  <article class="rounded-lg border bg-card p-5 text-card-foreground">
    <h2 class="mb-2 text-base font-medium">Timeline</h2>
    <div class="flex h-56 items-center justify-center text-sm text-muted-foreground">
      LineChart · stage 2.3
    </div>
  </article>

  <article class="rounded-lg border bg-card p-5 text-card-foreground">
    <h2 class="mb-2 text-base font-medium">Current filter state</h2>
    <pre class="overflow-x-auto text-xs text-muted-foreground">{JSON.stringify($filters, null, 2)}</pre>
  </article>
</section>
