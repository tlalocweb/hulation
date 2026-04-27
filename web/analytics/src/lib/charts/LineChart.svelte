<script lang="ts">
  import { onMount } from 'svelte';
  import { buildLineChart, formatShort, type TimeseriesPoint } from './utils';
  import { colorFor, MUTED_FG, BORDER } from './palette';

  /** Input points: each row has a `ts` Date + one numeric field per series. */
  export let points: TimeseriesPoint[] = [];
  /** Series names (order controls color). Default: plot `visitors` + `pageviews`. */
  export let series: string[] = ['visitors', 'pageviews'];
  /** Optional height. Width is responsive to the container. */
  export let height = 260;

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

  $: computed = buildLineChart(points, series, colorFor, { width, height });
  $: xTicks = computed.x.ticks;
  $: yTicks = computed.y.ticks;

  // Tooltip state — simple hover-follow dot.
  let hoverIdx: number | null = null;
  function onMove(ev: MouseEvent) {
    if (!points.length) return;
    const rect = (ev.currentTarget as SVGSVGElement).getBoundingClientRect();
    const x = ev.clientX - rect.left;
    const t = computed.x.scale.invert(x);
    let bestIdx = 0;
    let bestDelta = Infinity;
    for (let i = 0; i < points.length; i++) {
      const delta = Math.abs((points[i].ts as Date).getTime() - t.getTime());
      if (delta < bestDelta) {
        bestDelta = delta;
        bestIdx = i;
      }
    }
    hoverIdx = bestIdx;
  }
  function onLeave() {
    hoverIdx = null;
  }

  function fmtDate(d: Date): string {
    return d.toISOString().slice(0, 10);
  }
</script>

<div class="relative w-full" bind:this={container}>
  <svg
    role="img"
    aria-label={`Time series: ${series.join(', ')}`}
    {width}
    {height}
    viewBox={`0 0 ${width} ${height}`}
    on:mousemove={onMove}
    on:mouseleave={onLeave}
  >
    <!-- Gridlines + y-axis ticks -->
    {#each yTicks as t (t)}
      <g>
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
      </g>
    {/each}

    <!-- x-axis ticks -->
    {#each xTicks as t (t.getTime())}
      <text
        x={computed.x.scale(t)}
        y={height - 6}
        text-anchor="middle"
        fill={MUTED_FG}
        font-size="10"
      >
        {fmtDate(t)}
      </text>
    {/each}

    <!-- Series paths -->
    {#each computed.paths as p (p.series)}
      <path d={p.d} stroke={p.color} stroke-width="1.5" fill="none" stroke-linecap="round" />
    {/each}

    <!-- Hover dot -->
    {#if hoverIdx !== null && points[hoverIdx]}
      {@const hoverPt = points[hoverIdx]}
      {@const hoverTs = hoverPt.ts instanceof Date ? hoverPt.ts : new Date(String(hoverPt.ts))}
      {@const hoverX = computed.x.scale(hoverTs)}
      <g>
        {#each computed.paths as p (p.series)}
          <circle
            cx={hoverX}
            cy={computed.y.scale(Number(hoverPt[p.series] ?? 0))}
            r={3.5}
            fill={p.color}
          />
        {/each}
      </g>
    {/if}
  </svg>

  {#if hoverIdx !== null && points[hoverIdx]}
    {@const hoverPt = points[hoverIdx]}
    {@const hoverTs = hoverPt.ts instanceof Date ? hoverPt.ts : new Date(String(hoverPt.ts))}
    {@const hoverX = computed.x.scale(hoverTs)}
    <div
      class="pointer-events-none absolute rounded border bg-card px-2 py-1 text-xs shadow"
      style={`left: ${hoverX + 8}px; top: 8px;`}
    >
      <div class="font-medium">{fmtDate(hoverTs)}</div>
      {#each series as s, idx}
        <div class="flex items-center gap-2">
          <span class="inline-block size-2 rounded-full" style={`background: ${colorFor(idx)}`}></span>
          <span class="text-muted-foreground">{s}</span>
          <span class="tabular-nums">{formatShort(Number(hoverPt[s] ?? 0))}</span>
        </div>
      {/each}
    </div>
  {/if}

  <!-- Legend -->
  <div class="mt-2 flex flex-wrap gap-3 text-xs text-muted-foreground">
    {#each series as s, idx (s)}
      <span class="flex items-center gap-1.5">
        <span class="inline-block size-2.5 rounded-full" style={`background: ${colorFor(idx)}`}></span>
        {s}
      </span>
    {/each}
  </div>
</div>
