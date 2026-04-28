<script lang="ts">
  import { onMount, createEventDispatcher } from 'svelte';
  import { buildStackedBar, formatShort } from './utils';
  import { colorFor, MUTED_FG, BORDER } from './palette';

  export let rows: Record<string, string | number>[] = [];
  export let keyField = 'key';
  export let series: string[] = [];
  export let height = 240;

  const dispatch = createEventDispatcher<{ barClick: { key: string; series: string } }>();

  let container: HTMLDivElement;
  let width = 600;

  onMount(() => {
    if (!container || typeof ResizeObserver === 'undefined') return;
    const ro = new ResizeObserver((entries) => {
      for (const e of entries) width = e.contentRect.width;
    });
    ro.observe(container);
    width = container.getBoundingClientRect().width;
    return () => ro.disconnect();
  });

  $: computed = buildStackedBar(rows, keyField, series, colorFor, { width, height });
</script>

<div class="w-full" bind:this={container}>
  <svg role="img" aria-label="Stacked bar chart" {width} {height} viewBox={`0 0 ${width} ${height}`}>
    {#each computed.y.ticks as t (t)}
      <line
        x1={40}
        x2={width - 8}
        y1={computed.y.scale(t)}
        y2={computed.y.scale(t)}
        stroke={BORDER}
        stroke-dasharray="2 3"
      />
      <text
        x={36}
        y={computed.y.scale(t)}
        text-anchor="end"
        dominant-baseline="central"
        fill={MUTED_FG}
        font-size="10"
      >
        {formatShort(t)}
      </text>
    {/each}

    {#each computed.stacks as layer (layer.series)}
      {#each layer.bars as b (b.key)}
        <rect
          role="button"
          tabindex="0"
          aria-label={`${layer.series} bar for ${b.key}`}
          x={b.x}
          y={b.y}
          width={b.w}
          height={b.h}
          fill={layer.color}
          class="cursor-pointer"
          on:click={() => dispatch('barClick', { key: b.key, series: layer.series })}
          on:keydown={(e) => {
            if (e.key === 'Enter' || e.key === ' ') dispatch('barClick', { key: b.key, series: layer.series });
          }}
        >
          <title>{`${layer.series}: ${formatShort(Number(rows.find((r) => String(r[keyField]) === b.key)?.[layer.series] ?? 0))}`}</title>
        </rect>
      {/each}
    {/each}

    {#each rows as row (String(row[keyField]))}
      <text
        x={(computed.x.scale(String(row[keyField])) ?? 0) + computed.x.step / 2}
        y={height - 6}
        text-anchor="middle"
        fill={MUTED_FG}
        font-size="10"
      >
        {String(row[keyField]).slice(0, 14)}
      </text>
    {/each}
  </svg>

  <div class="mt-2 flex flex-wrap gap-3 text-xs text-muted-foreground">
    {#each series as s, idx (s)}
      <span class="flex items-center gap-1.5">
        <span class="inline-block size-2.5 rounded-sm" style={`background: ${colorFor(idx)}`}></span>
        {s}
      </span>
    {/each}
  </div>
</div>
