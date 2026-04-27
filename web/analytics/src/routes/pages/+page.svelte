<script lang="ts">
  import { onDestroy } from 'svelte';
  import { goto } from '$app/navigation';
  import { base } from '$app/paths';
  import { analytics } from '$lib/api/analytics';
  import { filters, setFilter, getSnapshot } from '$lib/filters';
  import { createQuery } from '$lib/useQuery';
  import { downloadBlob, csvFilename } from '$lib/csvDownload';
  import ReportTable, { type Column } from '$lib/components/ReportTable.svelte';
  import ErrorCard from '$lib/components/ErrorCard.svelte';
  import type { TableRow } from '$lib/api/types';
  import { formatShort, formatPct } from '$lib/charts/utils';

  const query = createQuery((state, signal) =>
    analytics.pages({
      serverId: state.server_id,
      filters: state.filters,
      limit: 500,
      signal,
    })
  );
  const { state, retry } = query;

  onDestroy(() => (state as unknown as { __cleanup?: () => void }).__cleanup?.());

  const columns: Column<TableRow>[] = [
    { key: 'key', label: 'Path', align: 'left' },
    { key: 'visitors', label: 'Visitors', align: 'right', format: (v: unknown) => formatShort(Number(v ?? 0)) },
    { key: 'pageviews', label: 'Pageviews', align: 'right', format: (v: unknown) => formatShort(Number(v ?? 0)) },
    {
      key: 'unique_pageviews',
      label: 'Unique',
      align: 'right',
      format: (v: unknown) => (v ? formatShort(Number(v)) : '—'),
    },
    {
      key: 'bounce_rate',
      label: 'Bounce',
      align: 'right',
      format: (v: unknown) => (v ? formatPct(Number(v) * 100) : '—'),
    },
    {
      key: 'avg_time_on_page_seconds',
      label: 'Avg time',
      align: 'right',
      format: (v: unknown) => {
        const n = Number(v ?? 0);
        if (!n) return '—';
        if (n < 60) return `${Math.round(n)}s`;
        const m = Math.floor(n / 60);
        return `${m}m ${Math.round(n - m * 60)}s`;
      },
    },
  ];

  function onRowClick(ev: CustomEvent<{ row: TableRow }>) {
    // Drill-down: set the path filter chip so every subsequent query
    // narrows to this page. Stay on /pages — the user sees the same
    // page filtered.
    setFilter('path', ev.detail.row.key);
  }

  async function onExport() {
    const snap = getSnapshot();
    try {
      const blob = await analytics.csv('pages', { serverId: snap.server_id, filters: snap.filters });
      downloadBlob(blob, csvFilename('pages'));
    } catch (err) {
      console.error('CSV export failed', err);
    }
  }

  $: rows = $state.data?.rows ?? [];
</script>

<section class="space-y-4">
  <header class="flex items-center justify-between gap-4">
    <div>
      <h1 class="text-2xl font-semibold tracking-tight">Pages</h1>
      <p class="text-sm text-muted-foreground">
        Top paths by pageviews. Click a row to narrow every report to that path.
      </p>
    </div>
    {#if $filters.filters.path}
      <button
        type="button"
        class="rounded border px-3 py-1.5 text-xs text-muted-foreground hover:text-foreground"
        on:click={() => setFilter('path', undefined)}
      >
        Clear path filter
      </button>
    {/if}
  </header>

  {#if $state.error}
    <ErrorCard error={$state.error} onRetry={retry} />
  {:else}
    <ReportTable
      {rows}
      {columns}
      loading={$state.loading}
      pageSize={50}
      initialSort={{ key: 'pageviews', dir: 'desc' }}
      onExportCsv={onExport}
      on:rowClick={onRowClick}
    />
  {/if}
</section>
