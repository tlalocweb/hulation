import { describe, it, expect } from 'vitest';
import {
  fromQueryString,
  toQueryString,
  presetToRange,
  rangeToPreset,
  type FilterState,
} from './filters';

describe('filter URL serialisation', () => {
  it('round-trips a populated state through encode → decode', () => {
    const original: FilterState = {
      server_id: 'testsite',
      filters: {
        from: '2026-04-01T00:00:00Z',
        to: '2026-04-08T00:00:00Z',
        granularity: 'day',
        country: 'DE',
        device: 'mobile',
        channel: 'search',
      },
    };
    const qs = toQueryString(original);
    const decoded = fromQueryString(qs);
    expect(decoded.server_id).toBe('testsite');
    expect(decoded.filters.from).toBe('2026-04-01T00:00:00Z');
    expect(decoded.filters.country).toBe('DE');
    expect(decoded.filters.channel).toBe('search');
  });

  it('omits empty and undefined filter fields', () => {
    const s: FilterState = {
      server_id: 's',
      filters: { from: '2026-01-01T00:00:00Z', to: '2026-01-02T00:00:00Z', country: '' },
    };
    const qs = toQueryString(s);
    expect(qs.has('filters.country')).toBe(false);
    expect(qs.get('filters.from')).toBe('2026-01-01T00:00:00Z');
  });

  it('handles repeated server_ids via append()', () => {
    const qs = new URLSearchParams();
    qs.append('filters.server_ids', 'a');
    qs.append('filters.server_ids', 'b');
    const decoded = fromQueryString(qs);
    expect(decoded.filters.server_ids).toEqual(['a', 'b']);
  });
});

describe('date preset helpers', () => {
  const fixedNow = new Date('2026-04-23T12:00:00Z');

  it('presetToRange 7d returns a 7-day span ending now', () => {
    const { from, to } = presetToRange('7d', fixedNow);
    expect(to).toBe(fixedNow.toISOString());
    const span = fixedNow.getTime() - new Date(from).getTime();
    expect(span).toBe(7 * 24 * 60 * 60 * 1000);
  });

  it('rangeToPreset recognises 30-day windows', () => {
    const { from, to } = presetToRange('30d', fixedNow);
    const state: FilterState = { server_id: '', filters: { from, to } };
    expect(rangeToPreset(state, fixedNow)).toBe('30d');
  });

  it('rangeToPreset reports custom for non-standard spans', () => {
    const state: FilterState = {
      server_id: '',
      filters: { from: '2026-04-01T00:00:00Z', to: '2026-04-12T05:33:00Z' },
    };
    expect(rangeToPreset(state, new Date('2026-04-12T05:33:00Z'))).toBe('custom');
  });
});
