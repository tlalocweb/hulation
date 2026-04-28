<script lang="ts">
  import Sparkline from '$lib/charts/Sparkline.svelte';
  import { formatShort, formatPct } from '$lib/charts/utils';

  /** Label shown under the big number. */
  export let label: string;
  /** Kind drives the formatter — 'count' gives 12.5k, 'pct' gives 12.3%, 'duration' gives 3m 24s. */
  export let kind: 'count' | 'pct' | 'duration' = 'count';
  export let value: number | null = null;
  export let sparkline: number[] = [];
  export let colorIndex = 0;
  /** Loading / error state. */
  export let loading = false;

  function formatValue(n: number | null, k: typeof kind): string {
    if (n === null || Number.isNaN(n)) return '—';
    if (k === 'count') return formatShort(n);
    if (k === 'pct') return formatPct(n * 100);
    // duration: n is seconds.
    const s = Math.round(n);
    if (s < 60) return `${s}s`;
    const m = Math.floor(s / 60);
    const rem = s % 60;
    if (m < 60) return `${m}m ${rem}s`;
    const h = Math.floor(m / 60);
    return `${h}h ${m % 60}m`;
  }
</script>

<article class="rounded-lg border bg-card p-5 text-card-foreground">
  <p class="text-sm text-muted-foreground">{label}</p>
  {#if loading}
    <div class="mt-1 h-9 w-24 animate-pulse rounded bg-muted"></div>
    <div class="mt-2 h-6 w-full animate-pulse rounded bg-muted/50"></div>
  {:else}
    <p class="mt-1 text-3xl font-semibold tracking-tight tabular-nums">
      {formatValue(value, kind)}
    </p>
    <div class="mt-2 h-6">
      {#if sparkline.length > 1}
        <Sparkline values={sparkline} {colorIndex} width={140} height={24} />
      {/if}
    </div>
  {/if}
</article>
