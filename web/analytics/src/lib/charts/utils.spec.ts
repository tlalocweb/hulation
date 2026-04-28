import { describe, it, expect } from 'vitest';
import {
  buildLineChart,
  buildSparkline,
  buildStackedBar,
  buildDonut,
  formatShort,
  formatPct,
  type TimeseriesPoint,
} from './utils';

const palette = (i: number) => ['#1', '#2', '#3', '#4'][i] ?? '#fallback';

describe('buildLineChart', () => {
  const ts = (day: number) => new Date(Date.UTC(2026, 3, day));
  const points: TimeseriesPoint[] = [
    { ts: ts(1), visitors: 100, pageviews: 250 },
    { ts: ts(2), visitors: 120, pageviews: 300 },
    { ts: ts(3), visitors: 150, pageviews: 280 },
  ];

  it('emits one path per series', () => {
    const c = buildLineChart(points, ['visitors', 'pageviews'], palette, {
      width: 400,
      height: 200,
    });
    expect(c.paths).toHaveLength(2);
    expect(c.paths[0].series).toBe('visitors');
    expect(c.paths[0].color).toBe('#1');
    expect(c.paths[0].d.startsWith('M')).toBe(true);
  });

  it('gracefully handles an empty series list', () => {
    const c = buildLineChart(points, [], palette, { width: 400, height: 200 });
    expect(c.paths).toHaveLength(0);
  });

  it('survives single-point input (no extent collapse)', () => {
    const one = [{ ts: ts(1), visitors: 5 }];
    const c = buildLineChart(one, ['visitors'], palette, { width: 400, height: 200 });
    expect(c.paths[0].d).toBeTypeOf('string');
  });
});

describe('buildSparkline', () => {
  it('returns an SVG path for 2+ values', () => {
    const d = buildSparkline([1, 3, 2, 5], { width: 100, height: 20 });
    expect(d.startsWith('M')).toBe(true);
  });

  it('returns empty string on empty input', () => {
    expect(buildSparkline([], { width: 100, height: 20 })).toBe('');
  });
});

describe('buildStackedBar', () => {
  const rows = [
    { key: 'Mon', desktop: 10, mobile: 5 },
    { key: 'Tue', desktop: 12, mobile: 8 },
  ];

  it('stacks in the order given by series', () => {
    const c = buildStackedBar(rows, 'key', ['desktop', 'mobile'], palette, {
      width: 400,
      height: 200,
    });
    expect(c.stacks).toHaveLength(2);
    expect(c.stacks[0].series).toBe('desktop');
    expect(c.stacks[1].series).toBe('mobile');
    expect(c.stacks[0].bars).toHaveLength(2);
  });

  it('bar heights are non-negative', () => {
    const c = buildStackedBar(rows, 'key', ['desktop', 'mobile'], palette, {
      width: 400,
      height: 200,
    });
    for (const layer of c.stacks) {
      for (const b of layer.bars) {
        expect(b.h).toBeGreaterThanOrEqual(0);
      }
    }
  });
});

describe('buildDonut', () => {
  it('produces slices that sum to 2π', () => {
    const rows = [
      { key: 'a', value: 30 },
      { key: 'b', value: 50 },
      { key: 'c', value: 20 },
    ];
    const slices = buildDonut(rows, palette, { radius: 80 });
    expect(slices).toHaveLength(3);
    const span = slices[slices.length - 1].endAngle - slices[0].startAngle;
    expect(span).toBeCloseTo(2 * Math.PI, 5);
  });

  it('percentages round to 100%', () => {
    const slices = buildDonut(
      [
        { key: 'a', value: 30 },
        { key: 'b', value: 70 },
      ],
      palette,
      { radius: 80 }
    );
    const total = slices.reduce((s, x) => s + x.percent, 0);
    expect(total).toBeCloseTo(100, 5);
  });
});

describe('formatters', () => {
  it('formatShort handles common magnitudes', () => {
    expect(formatShort(0)).toBe('0');
    expect(formatShort(999)).toBe('999');
    expect(formatShort(1500)).toBe('1.5k');
    expect(formatShort(12_500)).toBe('13k');
    expect(formatShort(1_200_000)).toBe('1.2M');
  });

  it('formatPct respects digits', () => {
    expect(formatPct(12.345)).toBe('12.3%');
    expect(formatPct(12.345, 0)).toBe('12%');
    expect(formatPct(NaN)).toBe('—');
  });
});
