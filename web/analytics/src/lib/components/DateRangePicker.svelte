<script lang="ts">
  import { filters, presetToRange, rangeToPreset, setDateRange, DATE_PRESETS } from '$lib/filters';

  let showCustom = false;
  let customFrom = '';
  let customTo = '';

  $: active = rangeToPreset($filters);

  function choose(value: string) {
    if (value === 'custom') {
      showCustom = true;
      return;
    }
    const { from, to } = presetToRange(value as any);
    setDateRange(from, to);
    showCustom = false;
  }

  function applyCustom() {
    if (!customFrom || !customTo) return;
    // Append a seconds-zero suffix if the user left off time. HTML
    // date inputs produce "YYYY-MM-DD"; we need RFC 3339.
    const from = customFrom.length === 10 ? `${customFrom}T00:00:00Z` : customFrom;
    const to = customTo.length === 10 ? `${customTo}T23:59:59Z` : customTo;
    setDateRange(from, to);
    showCustom = false;
  }
</script>

<div class="flex items-center gap-1 rounded border bg-background p-0.5">
  {#each DATE_PRESETS as p (p.value)}
    <button
      type="button"
      class="rounded px-2 py-1 text-xs transition
             {active === p.value ? 'bg-primary text-primary-foreground' : 'text-muted-foreground hover:text-foreground'}"
      on:click={() => choose(p.value)}
      aria-pressed={active === p.value}
    >
      {p.label}
    </button>
  {/each}
</div>

{#if showCustom}
  <div class="flex items-center gap-2 rounded border bg-background p-2 text-sm" role="dialog" aria-label="Custom date range">
    <label class="flex flex-col">
      <span class="text-xs text-muted-foreground">From</span>
      <input type="date" class="rounded border bg-background px-2 py-1" bind:value={customFrom} />
    </label>
    <label class="flex flex-col">
      <span class="text-xs text-muted-foreground">To</span>
      <input type="date" class="rounded border bg-background px-2 py-1" bind:value={customTo} />
    </label>
    <button type="button" class="rounded bg-primary px-3 py-1.5 text-primary-foreground" on:click={applyCustom}>
      Apply
    </button>
    <button type="button" class="rounded border px-3 py-1.5 text-muted-foreground" on:click={() => (showCustom = false)}>
      Cancel
    </button>
  </div>
{/if}
