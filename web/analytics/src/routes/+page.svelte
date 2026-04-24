<script lang="ts">
  import { onDestroy } from 'svelte';
  import { analytics } from '$lib/api/analytics';
  import KpiCard from '$lib/components/KpiCard.svelte';
  import ErrorCard from '$lib/components/ErrorCard.svelte';
  import LineChart from '$lib/charts/LineChart.svelte';
  import { createQuery } from '$lib/useQuery';
  import type { TimeseriesPoint } from '$lib/charts/utils';

  // Two queries fire in parallel on every filter change: summary (KPI
  // numbers) and timeseries (main chart + KPI sparklines).
  const summaryQ = createQuery((state, signal) =>
    analytics.summary({ serverId: state.server_id, filters: state.filters, signal })
  );
  const timeseriesQ = createQuery((state, signal) =>
    analytics.timeseries({ serverId: state.server_id, filters: state.filters, signal })
  );

  const { state: summary, retry: retrySummary } = summaryQ;
  const { state: timeseries, retry: retryTimeseries } = timeseriesQ;

  onDestroy(() => {
    (summary as unknown as { __cleanup?: () => void }).__cleanup?.();
    (timeseries as unknown as { __cleanup?: () => void }).__cleanup?.();
  });

  // Project timeseries buckets into the shape LineChart expects and
  // derive per-metric arrays for sparklines.
  $: points = ($timeseries.data?.buckets ?? []).map(
    (b): TimeseriesPoint => ({
      ts: new Date(b.ts),
      visitors: b.visitors,
      pageviews: b.pageviews,
    })
  );
  $: visitorSeries = ($timeseries.data?.buckets ?? []).map((b) => b.visitors);
  $: pageviewSeries = ($timeseries.data?.buckets ?? []).map((b) => b.pageviews);
</script>

<section class="space-y-6">
  <header>
    <h1 class="text-2xl font-semibold tracking-tight">Overview</h1>
    <p class="text-sm text-muted-foreground">
      Visitors, pageviews, and engagement across the filtered window.
    </p>
  </header>

  {#if $summary.error}
    <ErrorCard error={$summary.error} onRetry={retrySummary} />
  {/if}

  <div class="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-4">
    <KpiCard
      label="Visitors"
      kind="count"
      value={$summary.data?.visitors ?? null}
      sparkline={visitorSeries}
      colorIndex={0}
      loading={$summary.loading}
    />
    <KpiCard
      label="Pageviews"
      kind="count"
      value={$summary.data?.pageviews ?? null}
      sparkline={pageviewSeries}
      colorIndex={1}
      loading={$summary.loading}
    />
    <KpiCard
      label="Bounce rate"
      kind="pct"
      value={$summary.data?.bounce_rate ?? null}
      colorIndex={2}
      loading={$summary.loading}
    />
    <KpiCard
      label="Avg session"
      kind="duration"
      value={$summary.data?.avg_session_duration_seconds ?? null}
      colorIndex={3}
      loading={$summary.loading}
    />
  </div>

  <article class="rounded-lg border bg-card p-5 text-card-foreground">
    <h2 class="mb-3 text-base font-medium">Visitors + pageviews</h2>
    {#if $timeseries.error}
      <ErrorCard error={$timeseries.error} onRetry={retryTimeseries} />
    {:else if $timeseries.loading && points.length === 0}
      <div class="flex h-64 items-center justify-center text-sm text-muted-foreground">
        Loading…
      </div>
    {:else if points.length === 0}
      <div class="flex h-64 items-center justify-center text-sm text-muted-foreground">
        No data for this window.
      </div>
    {:else}
      <LineChart {points} series={['visitors', 'pageviews']} height={280} />
    {/if}
  </article>
</section>
