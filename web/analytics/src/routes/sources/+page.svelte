<script lang="ts">
  import { onDestroy } from 'svelte';
  import { analytics } from '$lib/api/analytics';
  import { filters, setFilter, getSnapshot } from '$lib/filters';
  import { createQuery } from '$lib/useQuery';
  import { downloadBlob, csvFilename } from '$lib/csvDownload';
  import ReportTable, { type Column } from '$lib/components/ReportTable.svelte';
  import ErrorCard from '$lib/components/ErrorCard.svelte';
  import type { TableRow } from '$lib/api/types';
  import { formatShort, formatPct } from '$lib/charts/utils';

  // Local group-by toggle — not part of the global filter state
  // because it only affects this page's shape. Changing it triggers a
  // new query via the reactive bumper.
  let groupBy: 'channel' | 'referer_host' | 'utm_source' = 'channel';

  // Re-create the query when groupBy changes so the in-flight request
  // gets cancelled and the new one fires.
  let queryBumper = 0;
  $: groupByKey = groupBy + String(queryBumper);

  const query = createQuery((state, signal) =>
    analytics.sources({
      serverId: state.server_id,
      filters: state.filters,
      groupBy,
      limit: 500,
      signal,
    })
  );
  const { state, retry } = query;

  onDestroy(() => (state as unknown as { __cleanup?: () => void }).__cleanup?.());

  function setGroup(g: 'channel' | 'referer_host' | 'utm_source') {
    groupBy = g;
    queryBumper++;
    // Nudge the filter store so useQuery picks up the change. We
    // write-then-clear a sentinel filter to trigger the subscriber.
    setFilter('source', getSnapshot().filters.source ?? '');
  }

  const columns: Column<TableRow>[] = [
    { key: 'key', label: 'Source', align: 'left' },
    { key: 'visitors', label: 'Visitors', align: 'right', format: (v: unknown) => formatShort(Number(v ?? 0)) },
    { key: 'pageviews', label: 'Pageviews', align: 'right', format: (v: unknown) => formatShort(Number(v ?? 0)) },
    {
      key: 'bounce_rate',
      label: 'Bounce',
      align: 'right',
      format: (v: unknown) => (v ? formatPct(Number(v) * 100) : '—'),
    },
    {
      key: 'pages_per_visit',
      label: 'Pages/visit',
      align: 'right',
      format: (v: unknown) => (v ? Number(v).toFixed(1) : '—'),
    },
  ];

  function onRowClick(ev: CustomEvent<{ row: TableRow }>) {
    // Drill-down wires to the matching filter key based on groupBy.
    const key = ev.detail.row.key;
    if (groupBy === 'channel') setFilter('channel', key.toLowerCase());
    else if (groupBy === 'referer_host') setFilter('source', key);
    else setFilter('utm_source', key);
  }

  async function onExport() {
    const snap = getSnapshot();
    try {
      const blob = await analytics.csv('sources', { serverId: snap.server_id, filters: snap.filters });
      downloadBlob(blob, csvFilename(`sources-${groupBy}`));
    } catch (err) {
      console.error('CSV export failed', err);
    }
  }

  $: rows = $state.data?.rows ?? [];

  const groupOptions = [
    { value: 'channel', label: 'Channel' },
    { value: 'referer_host', label: 'Host' },
    { value: 'utm_source', label: 'UTM source' },
  ] as const;
</script>

<section class="space-y-4">
  <header class="flex items-center justify-between gap-4">
    <div>
      <h1 class="text-2xl font-semibold tracking-tight">Sources</h1>
      <p class="text-sm text-muted-foreground">
        Traffic sources. Click a row to filter every report by this source.
      </p>
    </div>
    <div class="flex items-center gap-1 rounded border bg-background p-0.5 text-xs">
      {#each groupOptions as opt (opt.value)}
        <button
          type="button"
          class="rounded px-2 py-1 transition
                 {groupBy === opt.value ? 'bg-primary text-primary-foreground' : 'text-muted-foreground hover:text-foreground'}"
          on:click={() => setGroup(opt.value)}
          aria-pressed={groupBy === opt.value}
        >
          {opt.label}
        </button>
      {/each}
    </div>
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
