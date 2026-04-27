<script lang="ts">
  import { createEventDispatcher, onMount } from 'svelte';
  import { geoNaturalEarth1, geoPath, scaleSequential, interpolateBlues } from 'd3';
  import { feature } from 'topojson-client';
  import type { Topology, GeometryCollection } from 'topojson-specification';
  import type { Feature, FeatureCollection } from 'geojson';

  /** Rows to paint. `key` matches a country's ISO 3166-1 alpha-2 or
   * alpha-3 code depending on the topojson; the component tries both. */
  export let rows: { key: string; value: number }[] = [];
  export let height = 360;

  const dispatch = createEventDispatcher<{ countryClick: { key: string } }>();

  let container: HTMLDivElement;
  let width = 800;
  let geo: FeatureCollection | null = null;
  let loading = true;

  onMount(() => {
    // Responsive width. ResizeObserver is the single source of truth
    // after first mount.
    if (typeof ResizeObserver !== 'undefined' && container) {
      const ro = new ResizeObserver((entries) => {
        for (const e of entries) width = e.contentRect.width;
      });
      ro.observe(container);
      width = container.getBoundingClientRect().width;
    }
    // Load the world topojson lazily so it doesn't pull into bundles
    // for non-geography routes.
    loadWorld();
  });

  async function loadWorld() {
    try {
      const mod = await import('world-atlas/countries-110m.json');
      const topology = mod.default as unknown as Topology;
      const countries = feature(
        topology,
        topology.objects.countries as GeometryCollection
      ) as unknown as FeatureCollection;
      geo = countries;
    } catch (err) {
      console.error('ChoroplethMap: failed to load world topojson', err);
    } finally {
      loading = false;
    }
  }

  // Value lookup — both ISO alpha-2 and alpha-3 keys are supported in
  // the inbound `rows` array. world-atlas countries-110m uses
  // properties.name; we cross-reference via a small lookup map.
  $: valueByName = new Map(rows.map((r) => [r.key.toUpperCase(), r.value]));

  $: maxValue = rows.length ? Math.max(...rows.map((r) => r.value || 0)) : 1;
  $: color = scaleSequential(interpolateBlues).domain([0, maxValue || 1]);

  $: projection = geoNaturalEarth1().fitSize([width, height], geo ?? { type: 'FeatureCollection', features: [] });
  $: pathGen = geoPath(projection);

  function nameOf(f: Feature): string {
    const props = f.properties as Record<string, unknown> | null;
    const name = props?.name;
    return typeof name === 'string' ? name : '';
  }

  function valueOf(f: Feature): number {
    const name = nameOf(f);
    if (!name) return 0;
    const upper = name.toUpperCase();
    if (valueByName.has(upper)) return valueByName.get(upper)!;
    for (const [k, v] of valueByName) {
      if (upper.startsWith(k)) return v;
    }
    return 0;
  }
</script>

<div class="w-full" bind:this={container}>
  {#if loading}
    <div class="flex h-[360px] items-center justify-center text-sm text-muted-foreground">
      Loading map…
    </div>
  {:else if geo}
    <svg
      role="img"
      aria-label="Choropleth map"
      {width}
      {height}
      viewBox={`0 0 ${width} ${height}`}
    >
      {#each geo.features as f, i (i)}
        {@const v = valueOf(f)}
        {@const name = nameOf(f)}
        <path
          role="button"
          tabindex="0"
          aria-label={`${name}: ${v}`}
          d={pathGen(f) ?? ''}
          fill={v > 0 ? color(v) : 'hsl(var(--muted))'}
          stroke="hsl(var(--border))"
          stroke-width="0.5"
          class="cursor-pointer transition-opacity hover:opacity-80"
          on:click={() => dispatch('countryClick', { key: name })}
          on:keydown={(e) => {
            if (e.key === 'Enter' || e.key === ' ') dispatch('countryClick', { key: name });
          }}
        >
          <title>{`${name}: ${v || 0}`}</title>
        </path>
      {/each}
    </svg>
  {:else}
    <div class="flex h-[360px] items-center justify-center text-sm text-destructive">
      Failed to load map data.
    </div>
  {/if}
</div>
