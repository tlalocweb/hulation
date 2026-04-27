<script lang="ts">
  import { onDestroy, onMount } from 'svelte';
  import { filters } from '$lib/filters';
  import { analytics, ApiError } from '$lib/api/analytics';
  import type { RealtimeResponse, VisitorEvent, TableRow } from '$lib/api/types';
  import ErrorCard from '$lib/components/ErrorCard.svelte';

  // Realtime page. Polls /api/v1/analytics/realtime every POLL_MS ms.
  // Pauses while the tab is hidden so a background tab doesn't keep
  // hammering the origin. 5s matches the PRD; we can fall back to 15s
  // at runtime if the query turns out to be heavy in production.

  const POLL_MS = 5000;

  let data: RealtimeResponse | null = null;
  let loading = true;
  let error: unknown = null;

  let intervalHandle: ReturnType<typeof setInterval> | null = null;
  let abortCtl: AbortController | null = null;
  let currentServer = '';

  filters.subscribe((s) => {
    currentServer = s.server_id ?? '';
  });

  async function tick(): Promise<void> {
    if (!currentServer) {
      loading = false;
      data = null;
      return;
    }
    abortCtl?.abort();
    abortCtl = new AbortController();
    try {
      const r = await analytics.realtime({
        serverId: currentServer,
        filters: {},
        signal: abortCtl.signal,
      });
      data = r;
      error = null;
      loading = false;
    } catch (e) {
      if ((e as { name?: string })?.name === 'AbortError') return;
      error = e instanceof ApiError ? e : e;
      loading = false;
    }
  }

  function startPolling() {
    if (intervalHandle) return;
    tick();
    intervalHandle = setInterval(tick, POLL_MS);
  }

  function stopPolling() {
    if (intervalHandle) {
      clearInterval(intervalHandle);
      intervalHandle = null;
    }
    abortCtl?.abort();
  }

  function onVisibility() {
    if (document.visibilityState === 'hidden') {
      stopPolling();
    } else {
      startPolling();
    }
  }

  onMount(() => {
    startPolling();
    document.addEventListener('visibilitychange', onVisibility);
  });

  onDestroy(() => {
    stopPolling();
    if (typeof document !== 'undefined') {
      document.removeEventListener('visibilitychange', onVisibility);
    }
  });

  // Re-tick immediately when the selected server changes.
  let lastServer = '';
  $: if (currentServer !== lastServer) {
    lastServer = currentServer;
    if (intervalHandle) {
      tick();
    }
  }

  // Derived views.
  $: recent = (data?.recent ?? []) as VisitorEvent[];
  $: topPages = (data?.top_pages ?? []) as TableRow[];
  $: topSources = (data?.top_sources ?? []) as TableRow[];
  $: topCountry = mostCommonCountry(recent);

  function mostCommonCountry(evs: VisitorEvent[]): string {
    if (evs.length === 0) return '—';
    const counts = new Map<string, number>();
    for (const e of evs) {
      const c = (e.country ?? '').trim();
      if (!c) continue;
      counts.set(c, (counts.get(c) ?? 0) + 1);
    }
    let best = '—';
    let bestCount = 0;
    for (const [c, n] of counts.entries()) {
      if (n > bestCount) {
        best = c;
        bestCount = n;
      }
    }
    return best;
  }

  function formatTime(iso: string): string {
    if (!iso) return '';
    const d = new Date(iso);
    if (Number.isNaN(d.getTime())) return iso;
    const pad = (n: number) => n.toString().padStart(2, '0');
    return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
  }
</script>

<section class="space-y-6">
  <header class="flex items-center justify-between">
    <div>
      <h1 class="text-2xl font-semibold tracking-tight">Realtime</h1>
      <p class="text-sm text-muted-foreground">
        Live activity — refreshes every {Math.round(POLL_MS / 1000)}s while the tab is open.
      </p>
    </div>
    <span
      class="inline-flex items-center gap-2 text-xs text-muted-foreground"
      title={intervalHandle ? 'Polling active' : 'Paused'}
    >
      <span
        class="inline-block size-2 rounded-full {intervalHandle ? 'bg-primary animate-pulse' : 'bg-muted'}"
        aria-hidden="true"
      ></span>
      {intervalHandle ? 'live' : 'paused'}
    </span>
  </header>

  {#if error}
    <ErrorCard {error} onRetry={tick} />
  {/if}

  <div class="grid grid-cols-1 gap-4 md:grid-cols-3">
    <article class="rounded-lg border bg-card p-4">
      <h2 class="text-xs font-medium uppercase tracking-wider text-muted-foreground">
        Active visitors (5m)
      </h2>
      {#if loading && !data}
        <div class="mt-2 h-8 w-16 animate-pulse rounded bg-muted"></div>
      {:else}
        <p class="mt-2 text-3xl font-semibold">{data?.active_visitors_5m ?? 0}</p>
      {/if}
    </article>

    <article class="rounded-lg border bg-card p-4">
      <h2 class="text-xs font-medium uppercase tracking-wider text-muted-foreground">
        Recent events
      </h2>
      {#if loading && !data}
        <div class="mt-2 h-8 w-16 animate-pulse rounded bg-muted"></div>
      {:else}
        <p class="mt-2 text-3xl font-semibold">{recent.length}</p>
      {/if}
    </article>

    <article class="rounded-lg border bg-card p-4">
      <h2 class="text-xs font-medium uppercase tracking-wider text-muted-foreground">
        Top country
      </h2>
      {#if loading && !data}
        <div class="mt-2 h-8 w-16 animate-pulse rounded bg-muted"></div>
      {:else}
        <p class="mt-2 text-3xl font-semibold">{topCountry}</p>
      {/if}
    </article>
  </div>

  <div class="grid grid-cols-1 gap-4 lg:grid-cols-2">
    <article class="rounded-lg border bg-card">
      <header class="flex items-center justify-between border-b px-3 py-2">
        <h2 class="text-sm font-semibold">Top pages right now</h2>
        <span class="text-xs text-muted-foreground">{topPages.length}</span>
      </header>
      <ul class="divide-y text-sm">
        {#each topPages as p, i (i)}
          <li class="flex items-center justify-between gap-3 px-3 py-2">
            <span class="truncate font-mono text-xs">{p.key}</span>
            <span class="tabular-nums text-muted-foreground">{p.visitors ?? 0}</span>
          </li>
        {:else}
          <li class="px-3 py-6 text-center text-xs text-muted-foreground">
            {loading ? 'Loading…' : 'No recent traffic.'}
          </li>
        {/each}
      </ul>
    </article>

    <article class="rounded-lg border bg-card">
      <header class="flex items-center justify-between border-b px-3 py-2">
        <h2 class="text-sm font-semibold">Top sources right now</h2>
        <span class="text-xs text-muted-foreground">{topSources.length}</span>
      </header>
      <ul class="divide-y text-sm">
        {#each topSources as s, i (i)}
          <li class="flex items-center justify-between gap-3 px-3 py-2">
            <span class="truncate font-mono text-xs">{s.key || '(direct)'}</span>
            <span class="tabular-nums text-muted-foreground">{s.visitors ?? 0}</span>
          </li>
        {:else}
          <li class="px-3 py-6 text-center text-xs text-muted-foreground">
            {loading ? 'Loading…' : 'No recent sources.'}
          </li>
        {/each}
      </ul>
    </article>
  </div>

  <article class="rounded-lg border bg-card">
    <header class="flex items-center justify-between border-b px-3 py-2">
      <h2 class="text-sm font-semibold">Recent events</h2>
      <span class="text-xs text-muted-foreground">{recent.length}</span>
    </header>
    <div class="max-h-[28rem] overflow-y-auto">
      <table class="min-w-full text-sm">
        <thead class="sticky top-0 border-b bg-card text-xs text-muted-foreground">
          <tr class="text-left">
            <th class="px-3 py-2 font-medium">Time</th>
            <th class="px-3 py-2 font-medium">Event</th>
            <th class="px-3 py-2 font-medium">Path</th>
            <th class="px-3 py-2 font-medium">Country</th>
            <th class="px-3 py-2 font-medium">Device</th>
          </tr>
        </thead>
        <tbody>
          {#each recent as e, i (i)}
            <tr class="border-t">
              <td class="px-3 py-1.5 font-mono text-xs tabular-nums">{formatTime(e.ts)}</td>
              <td class="px-3 py-1.5 text-xs">{e.event_code}</td>
              <td class="px-3 py-1.5 truncate font-mono text-xs">{e.url}</td>
              <td class="px-3 py-1.5 text-xs text-muted-foreground">{e.country}</td>
              <td class="px-3 py-1.5 text-xs text-muted-foreground">{e.device}</td>
            </tr>
          {:else}
            <tr>
              <td colspan="5" class="px-3 py-10 text-center text-xs text-muted-foreground">
                {loading ? 'Loading…' : 'No events yet — waiting for activity.'}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  </article>
</section>
