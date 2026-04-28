<script lang="ts">
  import { filters, clearFilter, clearAllChips } from '$lib/filters';
  import type { Filters } from '$lib/api/types';

  // Non-chrome filter fields. `from`, `to`, `granularity`, `compare`,
  // and `server_ids` are surfaced through other controls; everything
  // else is represented as a removable chip.
  const CHROME: (keyof Filters)[] = ['from', 'to', 'granularity', 'compare', 'server_ids'];

  $: chips = Object.entries($filters.filters)
    .filter(([k, v]) => v !== undefined && v !== '' && !CHROME.includes(k as keyof Filters))
    .map(([k, v]) => ({ key: k as keyof Filters, value: String(v) }));
</script>

<div class="flex flex-wrap items-center gap-1.5">
  {#each chips as chip (chip.key)}
    <button
      type="button"
      class="inline-flex items-center gap-1 rounded-full bg-secondary px-2.5 py-1 text-xs text-secondary-foreground transition hover:bg-destructive hover:text-destructive-foreground"
      on:click={() => clearFilter(chip.key)}
      aria-label={`Remove filter ${chip.key}=${chip.value}`}
    >
      <span class="font-medium">{chip.key}</span>
      <span class="text-muted-foreground hover:text-destructive-foreground">=</span>
      <span>{chip.value}</span>
      <span class="text-muted-foreground hover:text-destructive-foreground">×</span>
    </button>
  {/each}
  {#if chips.length > 1}
    <button
      type="button"
      class="text-xs text-muted-foreground underline decoration-dotted hover:text-foreground"
      on:click={clearAllChips}
    >
      clear all
    </button>
  {/if}
</div>
