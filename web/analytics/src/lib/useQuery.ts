// useQuery — tiny reactive fetch helper. Re-subscribes on every
// FilterState change, aborts in-flight requests via AbortController.
//
// Rather than pulling in @tanstack/svelte-query (a 30 KB dep just for
// cache-and-retry), we get the 90% we need in ~40 lines. If more
// features (SWR, background refetch, cache) are needed, we can swap
// in the real thing in stage 2.8.

import { writable, type Readable } from 'svelte/store';
import { filters, type FilterState } from './filters';
import { ApiError } from './api/analytics';

export interface QueryState<T> {
  data: T | null;
  loading: boolean;
  error: unknown | null;
}

/** createQuery wraps an async fetcher so it re-runs whenever the
 * FilterState changes. Returns a Readable store for templates and a
 * `retry()` trigger for error-card recovery. */
export function createQuery<T>(
  fetcher: (state: FilterState, signal: AbortSignal) => Promise<T>
): { state: Readable<QueryState<T>>; retry: () => void } {
  const store = writable<QueryState<T>>({ data: null, loading: true, error: null });
  let current: AbortController | null = null;
  let bumper = 0;

  function run(state: FilterState): void {
    // Don't fire without a server + date range.
    if (!state.server_id || !state.filters.from || !state.filters.to) {
      store.set({ data: null, loading: false, error: null });
      return;
    }
    current?.abort();
    current = new AbortController();
    const myToken = ++bumper;
    store.set({ data: null, loading: true, error: null });
    fetcher(state, current.signal)
      .then((data) => {
        if (myToken !== bumper) return; // stale
        store.set({ data, loading: false, error: null });
      })
      .catch((err) => {
        if (myToken !== bumper) return;
        // Aborts are expected on rapid filter changes; don't surface.
        if (err?.name === 'AbortError') return;
        store.set({
          data: null,
          loading: false,
          error: err instanceof ApiError ? err : err ?? new Error('Unknown error'),
        });
      });
  }

  let lastSnapshot: FilterState | null = null;
  const unsub = filters.subscribe((state) => {
    // Avoid retriggering on identical snapshots (the store emits on
    // every mutation; some mutations don't affect the query surface).
    if (lastSnapshot && snapshotEquals(lastSnapshot, state)) return;
    lastSnapshot = structuredClone(state);
    run(state);
  });

  function retry(): void {
    if (lastSnapshot) run(lastSnapshot);
  }

  // The helper is called from onMount; teardown is the caller's
  // responsibility via unsub returned here.
  (store as unknown as { __cleanup: () => void }).__cleanup = unsub;

  return { state: store, retry };
}

function snapshotEquals(a: FilterState, b: FilterState): boolean {
  if (a.server_id !== b.server_id) return false;
  const ak = Object.keys(a.filters);
  const bk = Object.keys(b.filters);
  if (ak.length !== bk.length) return false;
  for (const k of ak) {
    const av = (a.filters as Record<string, unknown>)[k];
    const bv = (b.filters as Record<string, unknown>)[k];
    if (Array.isArray(av) && Array.isArray(bv)) {
      if (av.length !== bv.length) return false;
      for (let i = 0; i < av.length; i++) if (av[i] !== bv[i]) return false;
    } else if (av !== bv) {
      return false;
    }
  }
  return true;
}
