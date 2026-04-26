// Pure-function chart helpers. Kept DOM-free so Vitest can exercise
// them without a browser environment.
//
// Everything in here is trivially tree-shakable — no D3 side effects.

import {
  extent,
  max,
  scaleLinear,
  scaleTime,
  scaleBand,
  type ScaleLinear,
  type ScaleTime,
  arc,
  line,
  pie,
  stack,
  type PieArcDatum,
} from 'd3';

export type TimeseriesPoint = {
  ts: Date;
  [series: string]: Date | number;
};

export interface LinearAxis {
  scale: ScaleLinear<number, number>;
  ticks: number[];
}

export interface TimeAxis {
  scale: ScaleTime<number, number>;
  ticks: Date[];
}

export interface LineChartComputed {
  x: TimeAxis;
  y: LinearAxis;
  paths: { series: string; d: string; color: string }[];
}

/** buildLineChart produces the SVG path data for one or more
 * timeseries. Input points share a common `ts` field; the `series`
 * array lists the numeric keys to plot. Margins are baked in so
 * components render paths straight into an <svg>. */
export function buildLineChart(
  points: TimeseriesPoint[],
  series: string[],
  colorFor: (idx: number) => string,
  size: { width: number; height: number; margin?: { t: number; r: number; b: number; l: number } }
): LineChartComputed {
  const m = size.margin ?? { t: 12, r: 8, b: 24, l: 40 };
  const innerW = Math.max(1, size.width - m.l - m.r);
  const innerH = Math.max(1, size.height - m.t - m.b);

  const timeDomain = (extent(points, (p) => p.ts as Date) as [Date, Date]) ?? [
    new Date(),
    new Date(),
  ];
  const x = scaleTime().domain(timeDomain).range([m.l, m.l + innerW]);

  let yMax = 0;
  for (const s of series) {
    const m2 = max(points, (p) => Number(p[s] ?? 0)) ?? 0;
    if (m2 > yMax) yMax = m2;
  }
  const y = scaleLinear()
    .domain([0, yMax || 1])
    .nice()
    .range([m.t + innerH, m.t]);

  const paths = series.map((s, idx) => {
    const l = line<TimeseriesPoint>()
      .x((p) => x(p.ts as Date))
      .y((p) => y(Number(p[s] ?? 0)));
    return { series: s, d: l(points) ?? '', color: colorFor(idx) };
  });

  return {
    x: { scale: x, ticks: x.ticks(Math.min(8, Math.max(2, Math.floor(innerW / 80)))) },
    y: { scale: y, ticks: y.ticks(5) },
    paths,
  };
}

/** Build a sparkline: a miniature line (no axes) sized to fit the
 * given width/height. Returns just an SVG path 'd' string. */
export function buildSparkline(
  values: number[],
  size: { width: number; height: number; pad?: number }
): string {
  const pad = size.pad ?? 2;
  const innerW = Math.max(1, size.width - pad * 2);
  const innerH = Math.max(1, size.height - pad * 2);
  if (values.length === 0) return '';
  const x = scaleLinear()
    .domain([0, Math.max(1, values.length - 1)])
    .range([pad, pad + innerW]);
  const yMax = max(values) ?? 1;
  const yMin = 0;
  const y = scaleLinear()
    .domain([yMin, yMax || 1])
    .range([pad + innerH, pad]);
  const l = line<number>()
    .x((_v, i) => x(i))
    .y((v) => y(v));
  return l(values) ?? '';
}

export interface StackedBarComputed {
  x: { scale: (v: string) => number | undefined; step: number };
  y: LinearAxis;
  stacks: {
    series: string;
    color: string;
    bars: { key: string; x: number; y: number; w: number; h: number }[];
  }[];
}

/** Build stacked-bar geometry. `rows` are keyed by category; each
 * row carries one numeric field per series. */
export function buildStackedBar(
  rows: Record<string, string | number>[],
  keyField: string,
  series: string[],
  colorFor: (idx: number) => string,
  size: { width: number; height: number; margin?: { t: number; r: number; b: number; l: number } }
): StackedBarComputed {
  const m = size.margin ?? { t: 12, r: 8, b: 24, l: 40 };
  const innerW = Math.max(1, size.width - m.l - m.r);
  const innerH = Math.max(1, size.height - m.t - m.b);
  const keys = rows.map((r) => String(r[keyField] ?? ''));

  const x = scaleBand().domain(keys).range([m.l, m.l + innerW]).padding(0.2);
  const stacker = stack<Record<string, number>>().keys(series);
  const stacked = stacker(rows as unknown as Record<string, number>[]);

  let yMax = 0;
  for (const layer of stacked) {
    for (const v of layer) {
      if (v[1] > yMax) yMax = v[1];
    }
  }

  const y = scaleLinear()
    .domain([0, yMax || 1])
    .nice()
    .range([m.t + innerH, m.t]);

  const out: StackedBarComputed = {
    x: { scale: (k: string) => x(k), step: x.bandwidth() },
    y: { scale: y, ticks: y.ticks(5) },
    stacks: stacked.map((layer, idx) => ({
      series: String(layer.key),
      color: colorFor(idx),
      bars: layer.map((v, i) => ({
        key: keys[i],
        x: x(keys[i]) ?? 0,
        y: y(v[1]),
        w: x.bandwidth(),
        h: Math.max(0, y(v[0]) - y(v[1])),
      })),
    })),
  };
  return out;
}

export interface DonutSlice {
  key: string;
  value: number;
  percent: number;
  startAngle: number;
  endAngle: number;
  path: string;
  color: string;
}

/** Build donut slices. Relies on d3-shape's pie + arc, but those are
 * pure — no DOM needed. */
export function buildDonut(
  rows: { key: string; value: number }[],
  colorFor: (idx: number) => string,
  size: { radius: number; innerRatio?: number }
): DonutSlice[] {
  const innerRadius = size.radius * (size.innerRatio ?? 0.55);
  const total = rows.reduce((s, r) => s + (r.value || 0), 0) || 1;
  const pieBuilder = pie<{ key: string; value: number }>()
    .value((r) => r.value || 0)
    .sort(null);
  const arcBuilder = arc<PieArcDatum<{ key: string; value: number }>>()
    .innerRadius(innerRadius)
    .outerRadius(size.radius);

  const arcs = pieBuilder(rows);
  return arcs.map((a, idx) => ({
    key: a.data.key,
    value: a.data.value,
    percent: (a.data.value / total) * 100,
    startAngle: a.startAngle,
    endAngle: a.endAngle,
    path: arcBuilder(a) ?? '',
    color: colorFor(idx),
  }));
}

// formatShort — compact number formatter shared across charts. 12.5k,
// 3.4M, 820. Keeps ticks readable.
//
// Defensive Number-cast at entry: the analytics RPCs return int64
// fields as JSON strings (proto3 → grpc-gateway encodes int64 as
// string to avoid JS precision loss). The TS types declare them as
// `number` but at runtime values like "12" arrive. `isFinite("12")`
// coerces and reports true, but `"12".toFixed(...)` throws because
// strings don't have toFixed. Coercing at entry makes the formatter
// robust to either input type.
export function formatShort(n: number | string): string {
  const x = Number(n);
  if (!isFinite(x)) return '—';
  const abs = Math.abs(x);
  if (abs < 1_000) return x.toFixed(0);
  if (abs < 1_000_000) return (x / 1_000).toFixed(abs < 10_000 ? 1 : 0) + 'k';
  if (abs < 1_000_000_000) return (x / 1_000_000).toFixed(abs < 10_000_000 ? 1 : 0) + 'M';
  return (x / 1_000_000_000).toFixed(1) + 'B';
}

export function formatPct(n: number | string, digits = 1): string {
  const x = Number(n);
  if (!isFinite(x)) return '—';
  return x.toFixed(digits) + '%';
}
