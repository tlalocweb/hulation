<script lang="ts">
  import { onDestroy } from 'svelte';
  import { analytics } from '$lib/api/analytics';
  import { filters, setFilter, getSnapshot } from '$lib/filters';
  import { createQuery } from '$lib/useQuery';
  import { downloadBlob, csvFilename } from '$lib/csvDownload';
  import ChoroplethMap from '$lib/charts/ChoroplethMap.svelte';
  import ReportTable, { type Column } from '$lib/components/ReportTable.svelte';
  import ErrorCard from '$lib/components/ErrorCard.svelte';
  import type { TableRow } from '$lib/api/types';
  import { formatShort, formatPct } from '$lib/charts/utils';

  const query = createQuery((state, signal) =>
    analytics.geography({ serverId: state.server_id, filters: state.filters, signal })
  );
  const { state, retry } = query;

  onDestroy(() => (state as unknown as { __cleanup?: () => void }).__cleanup?.());

  $: drilled = Boolean($filters.filters.country);
  $: rows = $state.data?.rows ?? [];
  /** Rows the map renders. When drilled (country set), the response
   * contains region rows — the world map is no longer meaningful, so
   * we fall back to the table and hide the map. */
  $: mapRows = drilled ? [] : rows.map((r) => ({ key: r.key, value: r.visitors }));

  function onCountryClick(ev: CustomEvent<{ key: string }>) {
    // ISO code matching in the topojson is name-based. Use whatever
    // the server returned for a row keyed by matching the clicked
    // country's name against the table.
    const match = rows.find((r) => r.key.toUpperCase() === ev.detail.key.toUpperCase());
    if (match) setFilter('country', match.key);
  }

  function onRowClick(ev: CustomEvent<{ row: TableRow }>) {
    if (!drilled) {
      setFilter('country', ev.detail.row.key);
    }
    // When already drilled, row click is a no-op — there's no
    // sub-region level in Phase 2 (deferred to a later follow-up
    // because the region topojson is per-country).
  }

  const topColumns: Column<TableRow>[] = [
    { key: 'key', label: 'Country', align: 'left' },
    { key: 'visitors', label: 'Visitors', align: 'right', format: (v: unknown) => formatShort(Number(v ?? 0)) },
    { key: 'percent', label: '%', align: 'right', format: (v: unknown) => (v ? formatPct(Number(v)) : '—') },
    { key: 'pageviews', label: 'Pageviews', align: 'right', format: (v: unknown) => formatShort(Number(v ?? 0)) },
    { key: 'bounce_rate', label: 'Bounce', align: 'right', format: (v: unknown) => (v ? formatPct(Number(v) * 100) : '—') },
  ];

  const drillColumns: Column<TableRow>[] = [
    { key: 'key', label: 'Region', align: 'left' },
    { key: 'visitors', label: 'Visitors', align: 'right', format: (v: unknown) => formatShort(Number(v ?? 0)) },
    { key: 'pageviews', label: 'Pageviews', align: 'right', format: (v: unknown) => formatShort(Number(v ?? 0)) },
  ];

  async function onExport() {
    const snap = getSnapshot();
    const blob = await analytics.csv('geography', { serverId: snap.server_id, filters: snap.filters });
    downloadBlob(blob, csvFilename(drilled ? `geography-${snap.filters.country}` : 'geography'));
  }
</script>

<section class="space-y-4">
  <header class="flex items-center justify-between gap-4">
    <div>
      <h1 class="text-2xl font-semibold tracking-tight">Geography</h1>
      <p class="text-sm text-muted-foreground">
        {drilled
          ? `Regions within ${$filters.filters.country}. Click "All countries" to zoom back out.`
          : 'Countries by visitor count. Click a country on the map or the table to drill into its regions.'}
      </p>
    </div>
    {#if drilled}
      <button
        type="button"
        class="rounded border px-3 py-1.5 text-xs text-muted-foreground hover:text-foreground"
        on:click={() => setFilter('country', undefined)}
      >
        ← All countries
      </button>
    {/if}
  </header>

  {#if $state.error}
    <ErrorCard error={$state.error} onRetry={retry} />
  {:else}
    <div class="grid grid-cols-1 gap-4 xl:grid-cols-5">
      {#if !drilled}
        <article class="rounded-lg border bg-card p-4 text-card-foreground xl:col-span-3">
          <h2 class="mb-2 text-sm font-medium">World</h2>
          {#if $state.loading && mapRows.length === 0}
            <div class="flex h-[360px] items-center justify-center text-sm text-muted-foreground">
              Loading map…
            </div>
          {:else}
            <ChoroplethMap rows={mapRows} on:countryClick={onCountryClick} />
          {/if}
        </article>
      {/if}
      <article
        class="rounded-lg border bg-card p-4 text-card-foreground {drilled ? 'xl:col-span-5' : 'xl:col-span-2'}"
      >
        <h2 class="mb-2 text-sm font-medium">{drilled ? 'Regions' : 'Top countries'}</h2>
        <ReportTable
          {rows}
          columns={drilled ? drillColumns : topColumns}
          loading={$state.loading}
          pageSize={drilled ? 50 : 25}
          initialSort={{ key: 'visitors', dir: 'desc' }}
          onExportCsv={onExport}
          on:rowClick={onRowClick}
        />
      </article>
    </div>
  {/if}
</section>
