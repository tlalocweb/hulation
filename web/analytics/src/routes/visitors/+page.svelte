<script lang="ts">
  import { onDestroy } from 'svelte';
  import { goto } from '$app/navigation';
  import { base } from '$app/paths';
  import { analytics } from '$lib/api/analytics';
  import { createQuery } from '$lib/useQuery';
  import { downloadBlob, csvFilename } from '$lib/csvDownload';
  import ReportTable, { type Column } from '$lib/components/ReportTable.svelte';
  import ErrorCard from '$lib/components/ErrorCard.svelte';
  import type { VisitorSummary } from '$lib/api/types';
  import { formatShort } from '$lib/charts/utils';
  import { getSnapshot } from '$lib/filters';

  // Visitors table. Paginated server-side at 100/page; row click →
  // /analytics/visitors/[visitor_id] drill-down.

  const PAGE_SIZE = 100;
  let page = 0;

  const query = createQuery((state, signal) =>
    analytics.visitors({
      serverId: state.server_id,
      filters: state.filters,
      limit: PAGE_SIZE,
      offset: page * PAGE_SIZE,
      signal,
    })
  );
  const { state, retry } = query;
  onDestroy(() => (state as unknown as { __cleanup?: () => void }).__cleanup?.());

  // Refire the query when the page index changes. createQuery only
  // subscribes to the filters store so pagination needs an explicit
  // retry() on `page` mutation.
  let hasLoaded = false;
  state.subscribe((s) => {
    if (s.data !== null) hasLoaded = true;
  });
  $: if (hasLoaded) page, retry();

  $: total = $state.data?.total ?? 0;
  $: rows = ($state.data?.visitors ?? []) as VisitorSummary[];
  $: pageCount = Math.max(1, Math.ceil(total / PAGE_SIZE));

  const columns: Column<VisitorSummary>[] = [
    { key: 'visitor_id', label: 'Visitor', align: 'left' },
    {
      key: 'last_seen',
      label: 'Last seen',
      align: 'left',
      format: (v: unknown) => formatWhen(String(v ?? '')),
    },
    {
      key: 'first_seen',
      label: 'First seen',
      align: 'left',
      format: (v: unknown) => formatWhen(String(v ?? '')),
    },
    {
      key: 'sessions',
      label: 'Sessions',
      align: 'right',
      format: (v: unknown) => formatShort(Number(v ?? 0)),
    },
    {
      key: 'pageviews',
      label: 'Pageviews',
      align: 'right',
      format: (v: unknown) => formatShort(Number(v ?? 0)),
    },
    { key: 'top_country', label: 'Country', align: 'left' },
    { key: 'top_device', label: 'Device', align: 'left' },
  ];

  function formatWhen(iso: string): string {
    if (!iso) return '—';
    const d = new Date(iso);
    if (Number.isNaN(d.getTime())) return iso;
    return d.toLocaleString();
  }

  function onRowClick(ev: CustomEvent<{ row: VisitorSummary }>) {
    goto(`${base}/visitors/${encodeURIComponent(ev.detail.row.visitor_id)}`);
  }

  async function doCsv() {
    const snap = getSnapshot();
    if (!snap.server_id) return;
    const blob = await analytics.csv('visitors', {
      serverId: snap.server_id,
      filters: snap.filters,
    });
    downloadBlob(blob, csvFilename('visitors'));
  }
</script>

<section class="space-y-4">
  <header class="flex items-center justify-between gap-4">
    <div>
      <h1 class="text-2xl font-semibold tracking-tight">Visitors</h1>
      <p class="text-sm text-muted-foreground">
        {formatShort(total)} visitors in the selected window.
      </p>
    </div>
    <button
      type="button"
      class="rounded border px-3 py-1.5 text-sm text-muted-foreground hover:text-foreground"
      on:click={doCsv}
      disabled={!rows.length}
    >
      Download CSV
    </button>
  </header>

  {#if $state.error}
    <ErrorCard error={$state.error} onRetry={retry} />
  {/if}

  <ReportTable {columns} rows={rows} loading={$state.loading} on:rowClick={onRowClick} />

  <footer class="flex items-center justify-between text-xs text-muted-foreground">
    <span>Page {page + 1} of {pageCount}</span>
    <div class="flex gap-2">
      <button
        type="button"
        class="rounded border px-3 py-1 disabled:opacity-40"
        on:click={() => (page = Math.max(0, page - 1))}
        disabled={page === 0}
      >
        ← Previous
      </button>
      <button
        type="button"
        class="rounded border px-3 py-1 disabled:opacity-40"
        on:click={() => (page = Math.min(pageCount - 1, page + 1))}
        disabled={page >= pageCount - 1}
      >
        Next →
      </button>
    </div>
  </footer>
</section>
