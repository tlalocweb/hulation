<script lang="ts">
  import { createEventDispatcher } from 'svelte';
  import { buildDonut, formatPct, formatShort } from './utils';
  import { colorFor, MUTED_FG } from './palette';

  export let rows: { key: string; value: number }[] = [];
  export let size = 180;
  export let innerRatio = 0.55;

  const dispatch = createEventDispatcher<{ segmentClick: { key: string } }>();

  $: radius = size / 2 - 4;
  $: slices = buildDonut(rows, colorFor, { radius, innerRatio });
  $: total = rows.reduce((s, r) => s + (r.value || 0), 0);
</script>

<div class="flex flex-col items-center gap-2">
  <svg
    role="img"
    aria-label="Donut chart"
    width={size}
    height={size}
    viewBox={`${-size / 2} ${-size / 2} ${size} ${size}`}
  >
    {#each slices as s (s.key)}
      <path
        role="button"
        tabindex="0"
        aria-label={`${s.key}: ${formatPct(s.percent)}`}
        d={s.path}
        fill={s.color}
        class="cursor-pointer"
        on:click={() => dispatch('segmentClick', { key: s.key })}
        on:keydown={(e) => {
          if (e.key === 'Enter' || e.key === ' ') dispatch('segmentClick', { key: s.key });
        }}
      >
        <title>{`${s.key}: ${formatShort(s.value)} (${formatPct(s.percent)})`}</title>
      </path>
    {/each}
    <text
      x={0}
      y={0}
      text-anchor="middle"
      dominant-baseline="central"
      fill={MUTED_FG}
      font-size="12"
    >
      {formatShort(total)}
    </text>
  </svg>

  <ul class="w-full max-w-[220px] space-y-1 text-xs">
    {#each slices as s (s.key)}
      <li class="flex items-center gap-2">
        <span class="inline-block size-2.5 shrink-0 rounded-sm" style={`background: ${s.color}`}></span>
        <span class="flex-1 truncate text-muted-foreground">{s.key}</span>
        <span class="tabular-nums">{formatPct(s.percent)}</span>
      </li>
    {/each}
  </ul>
</div>
