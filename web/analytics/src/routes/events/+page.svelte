<script lang="ts">
  import { onDestroy } from 'svelte';
  import { analytics } from '$lib/api/analytics';
  import { filters, setFilter, getSnapshot } from '$lib/filters';
  import { createQuery } from '$lib/useQuery';
  import { downloadBlob, csvFilename } from '$lib/csvDownload';
  import ReportTable, { type Column } from '$lib/components/ReportTable.svelte';
  import ErrorCard from '$lib/components/ErrorCard.svelte';
  import Sheet from '$lib/components/Sheet.svelte';
  import type { TableRow } from '$lib/api/types';
  import { formatShort } from '$lib/charts/utils';

  // Events report. Top-level table is the per-event-code histogram
  // (count + unique_visitors + pct_of_total). Row click opens a
  // right-hand drill sheet with payload-key stats for that code.

  const query = createQuery((state, signal) =>
    analytics.events({
      serverId: state.server_id,
      filters: state.filters,
      signal,
    })
  );
  const { state, retry } = query;
  onDestroy(() => (state as unknown as { __cleanup?: () => void }).__cleanup?.());

  // The events RPC populates `count` and `unique_visitors` (see
  // pkg/analytics/query/events.go). Other TableRow fields (pageviews,
  // visitors) are zero — they're filled by other reports that share
  // the same wide row type.
  $: rows = ($state.data?.rows ?? []) as TableRow[];
  $: totalCount = rows.reduce((sum, r) => sum + Number(r.count ?? 0), 0);
  $: withPct = rows.map((r) => ({
    ...r,
    pct_of_total: totalCount ? (Number(r.count ?? 0) / totalCount) * 100 : 0,
  }));

  const columns: Column<TableRow & { pct_of_total: number }>[] = [
    { key: 'key', label: 'Event code', align: 'left' },
    {
      key: 'count',
      label: 'Count',
      align: 'right',
      format: (v: unknown) => formatShort(Number(v ?? 0)),
    },
    {
      key: 'unique_visitors',
      label: 'Unique visitors',
      align: 'right',
      format: (v: unknown) => formatShort(Number(v ?? 0)),
    },
    {
      key: 'pct_of_total',
      label: '% of total',
      align: 'right',
      format: (v: unknown) => {
        const n = Number(v ?? 0);
        return n > 0 ? `${n.toFixed(1)}%` : '—';
      },
    },
  ];

  // --- Drill sheet ---

  let drillOpen = false;
  let drillCode = '';
  let drillRow: TableRow | null = null;

  function onRowClick(ev: CustomEvent<{ row: TableRow }>) {
    drillRow = ev.detail.row;
    drillCode = String(ev.detail.row.key);
    drillOpen = true;
  }

  function applyEventFilter() {
    setFilter('event_code', drillCode);
    drillOpen = false;
  }

  async function onExport() {
    const snap = getSnapshot();
    if (!snap.server_id) return;
    try {
      const blob = await analytics.csv('events', {
        serverId: snap.server_id,
        filters: snap.filters,
      });
      downloadBlob(blob, csvFilename('events'));
    } catch (err) {
      console.error('CSV export failed', err);
    }
  }
</script>

<section class="space-y-4">
  <header class="flex items-center justify-between gap-4">
    <div>
      <h1 class="text-2xl font-semibold tracking-tight">Events</h1>
      <p class="text-sm text-muted-foreground">
        Per-code histogram. Click a row to see the payload breakdown.
      </p>
    </div>
    {#if $filters.filters.event_code}
      <button
        type="button"
        class="rounded border px-3 py-1.5 text-xs text-muted-foreground hover:text-foreground"
        on:click={() => setFilter('event_code', undefined)}
      >
        Clear event filter ({$filters.filters.event_code})
      </button>
    {/if}
  </header>

  {#if $state.error}
    <ErrorCard error={$state.error} onRetry={retry} />
  {:else}
    <ReportTable
      rows={withPct}
      {columns}
      loading={$state.loading}
      pageSize={50}
      initialSort={{ key: 'pageviews', dir: 'desc' }}
      onExportCsv={onExport}
      on:rowClick={onRowClick}
    />
  {/if}
</section>

<Sheet bind:open={drillOpen} width="w-[32rem]">
  <svelte:fragment let:close>
    <h2 class="mb-1 text-lg font-semibold">{drillCode}</h2>
    {#if drillRow}
      <p class="mb-4 text-sm text-muted-foreground">
        {formatShort(Number(drillRow.pageviews ?? 0))} fires ·
        {formatShort(Number(drillRow.visitors ?? 0))} unique visitors
      </p>

      <section class="space-y-2 text-sm">
        <h3 class="text-xs font-medium uppercase tracking-wider text-muted-foreground">
          Payload breakdown
        </h3>
        <p class="text-xs text-muted-foreground">
          Per-payload-value stats are not yet populated by the server — the events
          table only materialises top-level aggregates today. A follow-up will add
          a /api/v1/analytics/events/{drillCode}/payload endpoint with
          ranked Data.* values.
        </p>
      </section>

      <div class="mt-6 flex justify-end gap-2">
        <button type="button" class="rounded border px-3 py-1.5 text-sm" on:click={close}>
          Close
        </button>
        <button
          type="button"
          class="rounded bg-primary px-3 py-1.5 text-sm text-primary-foreground"
          on:click={applyEventFilter}
        >
          Filter by this event
        </button>
      </div>
    {/if}
  </svelte:fragment>
</Sheet>
