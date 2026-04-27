<script lang="ts">
  import { filters, setFilter } from '$lib/filters';

  const options = [
    { value: '', label: 'No compare' },
    { value: 'previous', label: 'vs previous' },
    { value: 'previous_year', label: 'vs year' },
  ] as const;

  $: current = $filters.filters.compare ?? '';

  function pick(value: string) {
    setFilter('compare', value === '' ? undefined : (value as any));
  }
</script>

<div class="flex items-center gap-1 rounded border bg-background p-0.5 text-xs">
  {#each options as opt (opt.value)}
    <button
      type="button"
      class="rounded px-2 py-1 transition
             {current === opt.value ? 'bg-secondary text-secondary-foreground' : 'text-muted-foreground hover:text-foreground'}"
      on:click={() => pick(opt.value)}
      aria-pressed={current === opt.value}
    >
      {opt.label}
    </button>
  {/each}
</div>
