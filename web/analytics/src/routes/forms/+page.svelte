<script lang="ts">
  import { onDestroy } from 'svelte';
  import { analytics } from '$lib/api/analytics';
  import { getSnapshot } from '$lib/filters';
  import { createQuery } from '$lib/useQuery';
  import { downloadBlob, csvFilename } from '$lib/csvDownload';
  import ReportTable, { type Column } from '$lib/components/ReportTable.svelte';
  import ErrorCard from '$lib/components/ErrorCard.svelte';
  import Sheet from '$lib/components/Sheet.svelte';
  import type { TableRow } from '$lib/api/types';
  import { formatShort, formatPct } from '$lib/charts/utils';

  // Forms report. TableRow fields populated by the server for this
  // dimension: key (form_id), visitors, submits, conversion_rate,
  // avg_time_to_submit_seconds.

  const query = createQuery((state, signal) =>
    analytics.formsReport({
      serverId: state.server_id,
      filters: state.filters,
      signal,
    })
  );
  const { state, retry } = query;
  onDestroy(() => (state as unknown as { __cleanup?: () => void }).__cleanup?.());

  $: rows = ($state.data?.rows ?? []) as TableRow[];

  const columns: Column<TableRow>[] = [
    { key: 'key', label: 'Form', align: 'left' },
    {
      key: 'visitors',
      label: 'Viewers',
      align: 'right',
      format: (v: unknown) => formatShort(Number(v ?? 0)),
    },
    {
      key: 'submits',
      label: 'Submissions',
      align: 'right',
      format: (v: unknown) => formatShort(Number(v ?? 0)),
    },
    {
      key: 'conversion_rate',
      label: 'Conversion',
      align: 'right',
      format: (v: unknown) => {
        const n = Number(v ?? 0);
        return n > 0 ? formatPct(n * 100) : '—';
      },
    },
    {
      key: 'avg_time_to_submit_seconds',
      label: 'Avg time to submit',
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

  // --- Drill sheet ---

  let drillOpen = false;
  let drillRow: TableRow | null = null;

  function onRowClick(ev: CustomEvent<{ row: TableRow }>) {
    drillRow = ev.detail.row;
    drillOpen = true;
  }

  async function onExport() {
    const snap = getSnapshot();
    if (!snap.server_id) return;
    try {
      const blob = await analytics.csv('forms', {
        serverId: snap.server_id,
        filters: snap.filters,
      });
      downloadBlob(blob, csvFilename('forms'));
    } catch (err) {
      console.error('CSV export failed', err);
    }
  }
</script>

<section class="space-y-4">
  <header>
    <h1 class="text-2xl font-semibold tracking-tight">Forms</h1>
    <p class="text-sm text-muted-foreground">
      Per-form view, submission, and conversion stats.
    </p>
  </header>

  {#if $state.error}
    <ErrorCard error={$state.error} onRetry={retry} />
  {:else}
    <ReportTable
      {rows}
      {columns}
      loading={$state.loading}
      pageSize={50}
      initialSort={{ key: 'submits', dir: 'desc' }}
      onExportCsv={onExport}
      on:rowClick={onRowClick}
    />
  {/if}
</section>

<Sheet bind:open={drillOpen} width="w-[30rem]">
  <svelte:fragment let:close>
    {#if drillRow}
      <h2 class="mb-1 text-lg font-semibold">{drillRow.key}</h2>
      <p class="mb-4 text-sm text-muted-foreground">
        {formatShort(Number(drillRow.visitors ?? 0))} viewers ·
        {formatShort(Number(drillRow.submits ?? 0))} submissions
      </p>

      <dl class="grid grid-cols-[10rem_1fr] gap-y-2 text-sm">
        <dt class="text-muted-foreground">Conversion rate</dt>
        <dd>
          {drillRow.conversion_rate
            ? formatPct(Number(drillRow.conversion_rate) * 100)
            : '—'}
        </dd>
        <dt class="text-muted-foreground">Avg time to submit</dt>
        <dd>{Math.round(Number(drillRow.avg_time_to_submit_seconds ?? 0))}s</dd>
        <dt class="text-muted-foreground">First seen</dt>
        <dd>{drillRow.first_seen || '—'}</dd>
        <dt class="text-muted-foreground">Last seen</dt>
        <dd>{drillRow.last_seen || '—'}</dd>
      </dl>

      <section class="mt-6 rounded border bg-muted/30 p-3 text-xs text-muted-foreground">
        Per-field fill-order and abandonment stats are not yet on the event
        row — shipping those requires richer form-view payloads at ingest.
        Tracked as a follow-up to this stage.
      </section>

      <div class="mt-6 flex justify-end">
        <button type="button" class="rounded border px-3 py-1.5 text-sm" on:click={close}>
          Close
        </button>
      </div>
    {/if}
  </svelte:fragment>
</Sheet>
