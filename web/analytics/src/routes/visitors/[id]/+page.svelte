<script lang="ts">
  import { onMount } from 'svelte';
  import { page } from '$app/stores';
  import { base } from '$app/paths';
  import { analytics, ApiError } from '$lib/api/analytics';
  import { filters, setFilter } from '$lib/filters';
  import ErrorCard from '$lib/components/ErrorCard.svelte';
  import type { VisitorResponse, VisitorEvent, VisitorIP } from '$lib/api/types';

  // Visitor detail drill-down. Fetches /visitor/{id} on mount. The
  // "Forget visitor (GDPR)" button is admin-gated (hidden for
  // non-admin sessions); the RPC it calls lands in stage 4.9.

  let data: VisitorResponse | null = null;
  let loading = true;
  let error: unknown = null;

  $: visitorId = $page.params.id ?? '';
  $: currentServer = $filters.server_id;

  async function load() {
    if (!currentServer || !visitorId) {
      loading = false;
      data = null;
      return;
    }
    loading = true;
    error = null;
    try {
      data = await analytics.visitor({
        serverId: currentServer,
        visitorId,
        filters: {},
      });
    } catch (e) {
      error = e;
    } finally {
      loading = false;
    }
  }
  $: currentServer, visitorId, load();

  let isAdmin = false;
  onMount(() => {
    isAdmin = Boolean(window.hulaConfig?.isAdmin);
  });

  async function doForget() {
    if (!data || !currentServer) return;
    const conf = confirm(
      `Permanently delete visitor ${visitorId} and every event + aggregate row?\n\nThis is irreversible.`,
    );
    if (!conf) return;
    try {
      const res = await fetch(
        `/api/v1/analytics/visitor/${encodeURIComponent(visitorId)}/forget`,
        {
          method: 'POST',
          headers: {
            Authorization: `Bearer ${localStorage.getItem('hula:token') ?? ''}`,
            'Content-Type': 'application/json',
          },
          body: JSON.stringify({ server_id: currentServer }),
        },
      );
      if (res.status === 501) {
        alert('ForgetVisitor endpoint not yet available on the server (stage 4.9).');
        return;
      }
      if (!res.ok) {
        const body = await res.text();
        throw new Error(`HTTP ${res.status}: ${body}`);
      }
      alert('Visitor forgotten.');
      window.location.href = `${base}/visitors`;
    } catch (e) {
      alert(`Failed: ${String(e)}`);
    }
  }

  function onClickPath(ev: VisitorEvent) {
    setFilter('path', ev.url);
    window.location.href = `${base}/pages`;
  }

  function formatWhen(iso: string): string {
    if (!iso) return '—';
    const d = new Date(iso);
    if (Number.isNaN(d.getTime())) return iso;
    return d.toLocaleString();
  }
</script>

<section class="space-y-6">
  <header class="flex items-start justify-between gap-4">
    <div>
      <nav class="text-xs text-muted-foreground">
        <a href={`${base}/visitors`} class="underline">← Visitors</a>
      </nav>
      <h1 class="mt-1 text-2xl font-semibold tracking-tight">
        Visitor {visitorId}
      </h1>
      {#if data?.visitor}
        <p class="text-sm text-muted-foreground">
          First seen {formatWhen(data.visitor.first_seen)} · Last seen
          {formatWhen(data.visitor.last_seen)} · {data.visitor.sessions} sessions ·
          {data.visitor.pageviews} pageviews
        </p>
      {/if}
    </div>
    {#if isAdmin}
      <button
        type="button"
        class="rounded border border-destructive/50 px-3 py-1.5 text-xs text-destructive hover:bg-destructive hover:text-destructive-foreground"
        on:click={doForget}
      >
        Forget visitor (GDPR)
      </button>
    {/if}
  </header>

  {#if error}
    <ErrorCard {error} onRetry={load} />
  {:else if loading}
    <div class="h-40 animate-pulse rounded-lg border bg-muted/30"></div>
  {:else if data}
    <div class="grid grid-cols-2 gap-4 md:grid-cols-4">
      <article class="rounded-lg border bg-card p-4">
        <h2 class="text-xs font-medium uppercase tracking-wider text-muted-foreground">
          Country
        </h2>
        <p class="mt-2 text-lg font-semibold">{data.visitor.top_country || '—'}</p>
      </article>
      <article class="rounded-lg border bg-card p-4">
        <h2 class="text-xs font-medium uppercase tracking-wider text-muted-foreground">
          Device
        </h2>
        <p class="mt-2 text-lg font-semibold">{data.visitor.top_device || '—'}</p>
      </article>
      <article class="rounded-lg border bg-card p-4">
        <h2 class="text-xs font-medium uppercase tracking-wider text-muted-foreground">
          Network
        </h2>
        <p class="mt-2 text-sm font-semibold">
          {data.visitor.top_asn || '—'}
        </p>
        <p class="text-xs text-muted-foreground" title={data.visitor.top_isp ?? ''}>
          {data.visitor.top_isp || ''}
        </p>
      </article>
      <article class="rounded-lg border bg-card p-4">
        <h2 class="text-xs font-medium uppercase tracking-wider text-muted-foreground">
          Email
        </h2>
        <p class="mt-2 text-lg font-semibold">{data.visitor.email || '—'}</p>
      </article>
    </div>

    <article class="rounded-lg border bg-card">
      <header class="border-b px-3 py-2">
        <h2 class="text-sm font-semibold">Timeline</h2>
      </header>
      {#if data.timeline.length === 0}
        <p class="px-3 py-10 text-center text-xs text-muted-foreground">
          No events recorded for this visitor in the current window.
        </p>
      {:else}
        <ol class="divide-y text-sm">
          {#each data.timeline as e, i (i)}
            <li class="grid grid-cols-[10rem_8rem_1fr_auto] items-center gap-3 px-3 py-2">
              <span class="font-mono text-xs tabular-nums text-muted-foreground">
                {formatWhen(e.ts)}
              </span>
              <span class="text-xs">{e.event_code}</span>
              <button
                type="button"
                class="truncate text-left font-mono text-xs hover:underline"
                on:click={() => onClickPath(e)}
                title="Filter Pages by this URL"
              >
                {e.url}
              </button>
              <span class="text-xs text-muted-foreground">{e.country} · {e.device}</span>
            </li>
          {/each}
        </ol>
      {/if}
    </article>

    {#if data.visitor_ips?.length || data.ips?.length}
      <article class="rounded-lg border bg-card">
        <header class="border-b px-3 py-2">
          <h2 class="text-sm font-semibold">IP addresses</h2>
        </header>
        <table class="min-w-full text-xs">
          <thead class="text-muted-foreground">
            <tr class="text-left">
              <th class="px-3 py-2 font-medium">IP</th>
              <th class="px-3 py-2 font-medium">ASN</th>
              <th class="px-3 py-2 font-medium">ISP</th>
              <th class="px-3 py-2 font-medium">Org</th>
              <th class="px-3 py-2 font-medium">Country</th>
            </tr>
          </thead>
          <tbody>
            {#if data.visitor_ips?.length}
              {#each data.visitor_ips as r (r.ip)}
                <tr class="border-t">
                  <td class="px-3 py-2 font-mono">{r.ip}</td>
                  <td class="px-3 py-2 font-mono">{r.asn || '—'}</td>
                  <td class="px-3 py-2">{r.isp || '—'}</td>
                  <td class="px-3 py-2">{r.org || '—'}</td>
                  <td class="px-3 py-2">{r.country_code || '—'}</td>
                </tr>
              {/each}
            {:else}
              <!-- Fallback for older servers that haven't grown visitor_ips yet. -->
              {#each data.ips ?? [] as ip (ip)}
                <tr class="border-t">
                  <td class="px-3 py-2 font-mono">{ip}</td>
                  <td class="px-3 py-2 text-muted-foreground" colspan="4">
                    Network details not yet resolved
                  </td>
                </tr>
              {/each}
            {/if}
          </tbody>
        </table>
      </article>
    {/if}

    {#if data.aliases?.length || data.cookies?.length}
      <article class="rounded-lg border bg-card p-4 text-xs">
        <h2 class="mb-2 text-sm font-semibold">Related identifiers</h2>
        <dl class="grid grid-cols-[7rem_1fr] gap-x-3 gap-y-1">
          {#if data.aliases?.length}
            <dt class="text-muted-foreground">Aliases</dt>
            <dd class="font-mono">{data.aliases.join(', ')}</dd>
          {/if}
          {#if data.cookies?.length}
            <dt class="text-muted-foreground">Cookies</dt>
            <dd class="font-mono">{data.cookies.join(', ')}</dd>
          {/if}
        </dl>
      </article>
    {/if}
  {/if}
</section>
