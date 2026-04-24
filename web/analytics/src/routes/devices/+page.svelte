<script lang="ts">
  import { onDestroy } from 'svelte';
  import { analytics } from '$lib/api/analytics';
  import { filters, setFilter, getSnapshot } from '$lib/filters';
  import { createQuery } from '$lib/useQuery';
  import { downloadBlob, csvFilename } from '$lib/csvDownload';
  import Donut from '$lib/charts/Donut.svelte';
  import ErrorCard from '$lib/components/ErrorCard.svelte';
  import type { TableRow } from '$lib/api/types';
  import { formatShort, formatPct } from '$lib/charts/utils';

  // One API call, three parallel arrays.
  const query = createQuery((state, signal) =>
    analytics.devices({ serverId: state.server_id, filters: state.filters, signal })
  );
  const { state, retry } = query;

  onDestroy(() => (state as unknown as { __cleanup?: () => void }).__cleanup?.());

  function rowsToSlices(rows: TableRow[]): { key: string; value: number }[] {
    return rows.map((r) => ({ key: r.key, value: r.visitors }));
  }

  $: deviceSlices = rowsToSlices($state.data?.device_category ?? []);
  $: browserSlices = rowsToSlices($state.data?.browser ?? []);
  $: osSlices = rowsToSlices($state.data?.os ?? []);

  function onDeviceClick(ev: CustomEvent<{ key: string }>) {
    setFilter('device', ev.detail.key);
  }
  function onBrowserClick(ev: CustomEvent<{ key: string }>) {
    setFilter('browser', ev.detail.key);
  }
  function onOsClick(ev: CustomEvent<{ key: string }>) {
    setFilter('os', ev.detail.key);
  }

  async function onExport() {
    const snap = getSnapshot();
    const blob = await analytics.csv('devices', { serverId: snap.server_id, filters: snap.filters });
    downloadBlob(blob, csvFilename('devices'));
  }

  $: activeChips = {
    device: $filters.filters.device,
    browser: $filters.filters.browser,
    os: $filters.filters.os,
  };

  type PanelKind = 'device' | 'browser' | 'os';
  interface Panel {
    label: string;
    kind: PanelKind;
    slices: { key: string; value: number }[];
    click: (ev: CustomEvent<{ key: string }>) => void;
    activeKey: string | undefined;
  }

  function isLoading(kind: PanelKind): boolean {
    if (!$state.loading) return false;
    return (
      (kind === 'device' && deviceSlices.length === 0) ||
      (kind === 'browser' && browserSlices.length === 0) ||
      (kind === 'os' && osSlices.length === 0)
    );
  }

  $: panels = [
    { label: 'Device category', kind: 'device', slices: deviceSlices, click: onDeviceClick, activeKey: activeChips.device },
    { label: 'Browser', kind: 'browser', slices: browserSlices, click: onBrowserClick, activeKey: activeChips.browser },
    { label: 'Operating system', kind: 'os', slices: osSlices, click: onOsClick, activeKey: activeChips.os },
  ] satisfies Panel[];
</script>

<section class="space-y-4">
  <header class="flex items-start justify-between gap-4">
    <div>
      <h1 class="text-2xl font-semibold tracking-tight">Devices</h1>
      <p class="text-sm text-muted-foreground">
        Device category, browser, and operating system share of visitors. Click a slice to filter every
        report by that category.
      </p>
    </div>
    <button
      type="button"
      class="rounded border bg-background px-3 py-1.5 text-xs text-muted-foreground hover:text-foreground"
      on:click={onExport}
    >
      Download CSV
    </button>
  </header>

  {#if $state.error}
    <ErrorCard error={$state.error} onRetry={retry} />
  {:else}
    <div class="grid grid-cols-1 gap-4 md:grid-cols-3">
      {#each panels as panel (panel.kind)}
        <article class="rounded-lg border bg-card p-5 text-card-foreground">
          <div class="mb-3 flex items-center justify-between gap-2">
            <h2 class="text-base font-medium">{panel.label}</h2>
            {#if panel.activeKey}
              <button
                type="button"
                class="text-xs text-muted-foreground underline decoration-dotted hover:text-foreground"
                on:click={() => {
                  if (panel.kind === 'device') setFilter('device', undefined);
                  else if (panel.kind === 'browser') setFilter('browser', undefined);
                  else setFilter('os', undefined);
                }}
              >
                clear
              </button>
            {/if}
          </div>
          {#if isLoading(panel.kind)}
            <div class="flex h-48 items-center justify-center text-sm text-muted-foreground">
              Loading…
            </div>
          {:else if panel.slices.length === 0}
            <div class="flex h-48 items-center justify-center text-sm text-muted-foreground">
              No data.
            </div>
          {:else}
            <Donut rows={panel.slices} on:segmentClick={panel.click} />
          {/if}
        </article>
      {/each}
    </div>
  {/if}
</section>
