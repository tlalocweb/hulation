// Global filter state, mirrored bidirectionally to the URL query
// string so every view is shareable and bookmarkable.
//
// Contract:
//   - `filters` is a Svelte store carrying the Phase-1 Filters shape
//     plus a `server_id` scalar (selected server).
//   - `hydrateFromURL(url)` reads the URL's search params and pushes
//     them into the store.
//   - `toQueryString(state)` serialises the store back to a
//     query-string (same wire shape the API client uses — `filters.*`
//     keys).
//   - On any store mutation during a browser session, we call
//     history.replaceState() with the updated URL; reloads preserve
//     the view.
//
// Kept deliberately small — the Filters object is flat and we only
// have ~15 keys. If this grows, consider typed helpers per key.

import { derived, get, writable, type Readable, type Writable } from 'svelte/store';
import { browser } from '$app/environment';
import type { Filters } from './api/types';

export type FilterState = {
  server_id: string;
  filters: Filters;
};

const INITIAL: FilterState = {
  server_id: '',
  filters: {},
};

function cloneState(s: FilterState): FilterState {
  return { server_id: s.server_id, filters: { ...s.filters } };
}

const state: Writable<FilterState> = writable(cloneState(INITIAL));

// Public handle: read-only store for subscribers, mutation via the
// update helpers below so URL sync is centralised.
export const filters: Readable<FilterState> = derived(state, (s) => s);

export function setServer(id: string): void {
  state.update((s) => {
    const next = cloneState(s);
    next.server_id = id;
    return next;
  });
  syncURL();
}

export function setFilter<K extends keyof Filters>(key: K, value: Filters[K] | undefined): void {
  state.update((s) => {
    const next = cloneState(s);
    if (value === undefined || value === '' || (Array.isArray(value) && value.length === 0)) {
      delete next.filters[key];
    } else {
      next.filters[key] = value;
    }
    return next;
  });
  syncURL();
}

export function setDateRange(from: string, to: string): void {
  state.update((s) => {
    const next = cloneState(s);
    next.filters.from = from;
    next.filters.to = to;
    return next;
  });
  syncURL();
}

export function clearFilter(key: keyof Filters): void {
  setFilter(key, undefined);
}

export function clearAllChips(): void {
  state.update((s) => {
    // Keep the time range + granularity + compare; drop every other
    // drill-down chip.
    const next = cloneState(s);
    const preserved: (keyof Filters)[] = ['from', 'to', 'granularity', 'compare', 'server_ids'];
    next.filters = Object.fromEntries(
      Object.entries(s.filters).filter(([k]) => preserved.includes(k as keyof Filters))
    ) as Filters;
    return next;
  });
  syncURL();
}

export function getSnapshot(): FilterState {
  return cloneState(get(state));
}

/** Encodes the current state as a URLSearchParams.
 * `server_id` is kept top-level; every Filters field becomes
 * `filters.<key>=<value>`. Array values emit repeated keys. */
export function toQueryString(s: FilterState = get(state)): URLSearchParams {
  const qs = new URLSearchParams();
  if (s.server_id) qs.set('server_id', s.server_id);
  for (const [k, v] of Object.entries(s.filters)) {
    if (v === undefined || v === null || v === '') continue;
    if (Array.isArray(v)) {
      for (const item of v) {
        if (item !== '') qs.append(`filters.${k}`, String(item));
      }
      continue;
    }
    qs.set(`filters.${k}`, String(v));
  }
  return qs;
}

/** Parses a URLSearchParams back into a FilterState. */
export function fromQueryString(qs: URLSearchParams): FilterState {
  const next: FilterState = { server_id: qs.get('server_id') ?? '', filters: {} };
  for (const [rawKey, value] of qs.entries()) {
    if (rawKey === 'server_id') continue;
    if (!rawKey.startsWith('filters.')) continue;
    const key = rawKey.slice('filters.'.length) as keyof Filters;
    if (key === 'server_ids') {
      const existing = (next.filters.server_ids ?? []) as string[];
      existing.push(value);
      next.filters.server_ids = existing;
    } else {
      (next.filters as Record<string, string>)[key] = value;
    }
  }
  return next;
}

/** hydrateFromURL — call in +layout.svelte onMount so the store
 * reflects whatever was in the URL when the page loaded. Idempotent. */
export function hydrateFromURL(url: URL): void {
  state.set(fromQueryString(url.searchParams));
}

/** syncURL — replace the browser URL to match the store without
 * triggering a navigation. No-op outside the browser (SSR, tests). */
function syncURL(): void {
  if (!browser) return;
  const qs = toQueryString();
  const target = qs.toString() ? `?${qs.toString()}` : location.pathname;
  if (location.search === (qs.toString() ? `?${qs.toString()}` : '')) return;
  window.history.replaceState(null, '', target + location.hash);
}

// ---------------------------------------------------------------------
// Date-range presets
// ---------------------------------------------------------------------

export const DATE_PRESETS = [
  { label: 'Today', value: '1d' },
  { label: '7 days', value: '7d' },
  { label: '30 days', value: '30d' },
  { label: '90 days', value: '90d' },
  { label: 'Custom', value: 'custom' },
] as const;

export type DatePreset = (typeof DATE_PRESETS)[number]['value'];

export function presetToRange(preset: DatePreset, now = new Date()): { from: string; to: string } {
  const to = now.toISOString();
  const daysMap: Record<string, number> = { '1d': 1, '7d': 7, '30d': 30, '90d': 90 };
  const days = daysMap[preset] ?? 7;
  const from = new Date(now.getTime() - days * 24 * 60 * 60 * 1000).toISOString();
  return { from, to };
}

/** Returns the matching preset for the current from/to, or 'custom'
 * when the span doesn't align with one of the presets. */
export function rangeToPreset(state: FilterState, now = new Date()): DatePreset {
  const { from, to } = state.filters;
  if (!from || !to) return '7d';
  const toDate = new Date(to);
  // Allow 60s slack on the "now" edge — wall clock drift between
  // the preset click and the render.
  if (Math.abs(toDate.getTime() - now.getTime()) > 60_000) return 'custom';
  const fromDate = new Date(from);
  const spanDays = Math.round((toDate.getTime() - fromDate.getTime()) / 86_400_000);
  switch (spanDays) {
    case 1:
      return '1d';
    case 7:
      return '7d';
    case 30:
      return '30d';
    case 90:
      return '90d';
    default:
      return 'custom';
  }
}
