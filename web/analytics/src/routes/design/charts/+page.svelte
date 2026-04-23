<script lang="ts">
  import LineChart from '$lib/charts/LineChart.svelte';
  import Sparkline from '$lib/charts/Sparkline.svelte';
  import StackedBar from '$lib/charts/StackedBar.svelte';
  import Donut from '$lib/charts/Donut.svelte';
  import ChoroplethMap from '$lib/charts/ChoroplethMap.svelte';
  import type { TimeseriesPoint } from '$lib/charts/utils';

  // Representative data for each component — visible on
  // /analytics/design/charts so visual regressions are easy to spot.
  const days = 14;
  const linePoints: TimeseriesPoint[] = Array.from({ length: days }, (_, i) => ({
    ts: new Date(Date.UTC(2026, 3, 10 + i)),
    visitors: 200 + Math.round(Math.sin(i / 2) * 80 + Math.random() * 60),
    pageviews: 450 + Math.round(Math.cos(i / 3) * 120 + Math.random() * 120),
  }));

  const sparkValues = [
    12, 14, 18, 22, 27, 31, 28, 30, 35, 40, 44, 41, 46, 52,
  ];

  const stackedRows = [
    { key: 'Mon', desktop: 60, mobile: 40, tablet: 10 },
    { key: 'Tue', desktop: 70, mobile: 44, tablet: 13 },
    { key: 'Wed', desktop: 52, mobile: 55, tablet: 9 },
    { key: 'Thu', desktop: 64, mobile: 48, tablet: 12 },
    { key: 'Fri', desktop: 80, mobile: 52, tablet: 14 },
    { key: 'Sat', desktop: 38, mobile: 70, tablet: 22 },
    { key: 'Sun', desktop: 30, mobile: 60, tablet: 20 },
  ];

  const donutRows = [
    { key: 'desktop', value: 52 },
    { key: 'mobile', value: 38 },
    { key: 'tablet', value: 10 },
  ];

  const mapRows = [
    { key: 'UNITED STATES OF AMERICA', value: 540 },
    { key: 'GERMANY', value: 210 },
    { key: 'FRANCE', value: 140 },
    { key: 'JAPAN', value: 95 },
    { key: 'BRAZIL', value: 60 },
  ];
</script>

<section class="space-y-8">
  <header>
    <h1 class="text-2xl font-semibold tracking-tight">Chart sandbox</h1>
    <p class="text-sm text-muted-foreground">
      Visual reference for the D3 chart components landed in stage 2.3.
    </p>
  </header>

  <article class="rounded-lg border bg-card p-5 text-card-foreground">
    <h2 class="mb-3 text-base font-medium">LineChart · multi-series</h2>
    <LineChart points={linePoints} series={['visitors', 'pageviews']} />
  </article>

  <article class="rounded-lg border bg-card p-5 text-card-foreground">
    <h2 class="mb-3 text-base font-medium">Sparkline · inline</h2>
    <div class="flex items-center gap-4 text-sm">
      <span class="text-muted-foreground">Visitors (14d)</span>
      <Sparkline values={sparkValues} />
      <span class="tabular-nums">{sparkValues[sparkValues.length - 1]}</span>
    </div>
  </article>

  <article class="rounded-lg border bg-card p-5 text-card-foreground">
    <h2 class="mb-3 text-base font-medium">StackedBar · top-N over time</h2>
    <StackedBar
      rows={stackedRows}
      keyField="key"
      series={['desktop', 'mobile', 'tablet']}
    />
  </article>

  <article class="rounded-lg border bg-card p-5 text-card-foreground">
    <h2 class="mb-3 text-base font-medium">Donut · proportion</h2>
    <Donut rows={donutRows} />
  </article>

  <article class="rounded-lg border bg-card p-5 text-card-foreground">
    <h2 class="mb-3 text-base font-medium">ChoroplethMap · world</h2>
    <ChoroplethMap rows={mapRows} />
  </article>
</section>
