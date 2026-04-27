<script lang="ts">
  import { onDestroy, onMount } from 'svelte';
  import { base } from '$app/paths';
  import { page } from '$app/stores';
  import { filters } from '$lib/filters';
  import { chat, type ChatSession } from '$lib/api/chat';

  // All chats — paginated history table. Search box at the top
  // (debounced 300 ms) hits SearchMessages and merges in matched
  // sessions; empty q falls back to ListSessions filtered by the
  // global from/to date range.

  // ChatSession[] for the list + per-session FTS match snippet
  // when q is non-empty. The snippet renders below the visitor
  // line in each row so the agent can see WHY a session matched.
  type Hit = ChatSession & { snippet?: string };
  let rows: Hit[] = [];
  let totalCount = 0;
  let loading = true;
  let error = '';
  let q = '';
  let qDebounce: ReturnType<typeof setTimeout> | null = null;
  let abort: AbortController | null = null;
  // Generation counter — stale loads (older than the current
  // debounced query) bail before overwriting state.
  let gen = 0;

  $: serverID = $filters.server_id;
  $: from = $filters.filters.from;
  $: to = $filters.filters.to;

  // Refire on filter changes.
  let lastSig = '';
  $: {
    const sig = `${serverID}|${from}|${to}|${q}`;
    if (sig !== lastSig) {
      lastSig = sig;
      void load();
    }
  }

  async function load() {
    if (!serverID) {
      rows = [];
      totalCount = 0;
      loading = false;
      return;
    }
    abort?.abort();
    abort = new AbortController();
    const myGen = ++gen;
    const isStale = () => myGen !== gen;
    // Clear immediately so a query change wipes the previous
    // hits before the (potentially slow, hit-by-hit) FTS hydrate
    // catches up — otherwise the table mixes stale + fresh rows.
    rows = [];
    totalCount = 0;
    loading = true;
    error = '';
    try {
      if (q.trim()) {
        // FTS path — show sessions whose messages match q.
        const r = await chat.searchMessages({
          serverId: serverID,
          q,
          from,
          to,
          limit: 100,
          signal: abort.signal,
        });
        if (isStale()) return;
        // Map session_id → first matching message (the snippet
        // we show under the visitor line in each row). Newest-
        // first ordering preserved from the API response.
        const sids: string[] = [];
        const snippetBySid = new Map<string, string>();
        for (const m of r.messages) {
          if (!m.session_id) continue;
          if (!snippetBySid.has(m.session_id)) {
            sids.push(m.session_id);
            snippetBySid.set(m.session_id, m.content ?? '');
          }
        }
        const fetched: Hit[] = [];
        for (const id of sids.slice(0, 100)) {
          try {
            const s = await chat.getSession({ serverId: serverID, sessionId: id, signal: abort.signal });
            if (isStale()) return;
            fetched.push({ ...s, snippet: snippetBySid.get(id) });
          } catch {
            /* skip */
          }
        }
        if (isStale()) return;
        rows = fetched;
        totalCount = Number(r.total_count ?? 0);
      } else {
        const r = await chat.listSessions({
          serverId: serverID,
          from,
          to,
          limit: 100,
          signal: abort.signal,
        });
        if (isStale()) return;
        rows = r.sessions ?? [];
        totalCount = Number(r.total_count ?? 0);
      }
    } catch (e) {
      if (isStale()) return;
      if ((e as { name?: string })?.name === 'AbortError') return;
      error = (e as Error).message;
    } finally {
      if (!isStale()) loading = false;
    }
  }

  // highlightMatch wraps every (case-insensitive) occurrence of any
  // q-token in the snippet with <mark>. Tokens are whitespace-split
  // server-side, so we mirror that here.
  function highlightMatch(snippet: string, query: string): string {
    if (!snippet || !query.trim()) return escapeHTML(snippet);
    const tokens = query.toLowerCase().trim().split(/\s+/).filter(Boolean);
    if (tokens.length === 0) return escapeHTML(snippet);
    const escaped = tokens.map((t) => t.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'));
    const re = new RegExp('(' + escaped.join('|') + ')', 'gi');
    return escapeHTML(snippet).replace(re, '<mark class="rounded bg-amber-200 px-0.5 dark:bg-amber-700/60 dark:text-amber-50">$1</mark>');
  }
  function escapeHTML(s: string): string {
    return s
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;')
      .replace(/'/g, '&#39;');
  }

  function onSearchInput(ev: Event) {
    const v = (ev.target as HTMLInputElement).value;
    if (qDebounce) clearTimeout(qDebounce);
    qDebounce = setTimeout(() => {
      q = v;
    }, 300);
  }

  function statusBadge(s?: string): string {
    switch (s) {
      case 'CHAT_SESSION_STATUS_QUEUED':
        return 'queued';
      case 'CHAT_SESSION_STATUS_ASSIGNED':
        return 'assigned';
      case 'CHAT_SESSION_STATUS_OPEN':
        return 'open';
      case 'CHAT_SESSION_STATUS_CLOSED':
        return 'closed';
      case 'CHAT_SESSION_STATUS_EXPIRED':
        return 'expired';
    }
    return '—';
  }

  function statusClass(s?: string): string {
    if (s === 'CHAT_SESSION_STATUS_OPEN') return 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-300';
    if (s === 'CHAT_SESSION_STATUS_QUEUED') return 'bg-amber-500/15 text-amber-700 dark:text-amber-300';
    if (s === 'CHAT_SESSION_STATUS_ASSIGNED') return 'bg-sky-500/15 text-sky-700 dark:text-sky-300';
    return 'bg-muted text-muted-foreground';
  }

  function fmtTime(s?: string): string {
    if (!s) return '—';
    try {
      return new Date(s).toLocaleString();
    } catch {
      return s;
    }
  }

  onMount(() => {
    void load();
  });
  onDestroy(() => abort?.abort());
</script>

<section class="space-y-4">
  <header class="flex items-center justify-between gap-4">
    <div>
      <h1 class="text-2xl font-semibold tracking-tight">Chats</h1>
      <p class="text-sm text-muted-foreground">
        All chat sessions and messages on this server. Click a row to open the thread.
      </p>
    </div>
    <a href="{base}/chats/live{$page.url.search}" class="text-sm text-primary hover:underline">
      Live chats →
    </a>
  </header>

  <div class="flex items-center gap-2">
    <input
      type="search"
      placeholder="Search messages…"
      class="flex-1 rounded border bg-background px-3 py-1.5 text-sm focus:outline-none focus:ring-2 focus:ring-ring"
      on:input={onSearchInput}
    />
    {#if loading}
      <span class="text-xs text-muted-foreground">loading…</span>
    {:else}
      <span class="text-xs text-muted-foreground">{totalCount} total</span>
    {/if}
  </div>

  {#if error}
    <div class="rounded border border-destructive bg-destructive/10 p-3 text-sm text-destructive">
      {error}
    </div>
  {/if}

  <div class="overflow-x-auto rounded-lg border bg-card">
    <table class="w-full text-sm">
      <thead class="border-b bg-muted/30 text-left">
        <tr>
          <th class="px-3 py-2 font-medium">Visitor</th>
          <th class="px-3 py-2 font-medium">Started</th>
          <th class="px-3 py-2 font-medium">Last message</th>
          <th class="px-3 py-2 text-right font-medium">Messages</th>
          <th class="px-3 py-2 font-medium">Agent</th>
          <th class="px-3 py-2 font-medium">Status</th>
        </tr>
      </thead>
      <tbody>
        {#each rows as r (r.id)}
          <tr class="border-b hover:bg-accent/40">
            <td class="px-3 py-2">
              <a href="{base}/chats/{r.id}{$page.url.search}" class="block">
                <div class="font-medium">{r.visitor_email || '—'}</div>
                <div class="text-xs text-muted-foreground">{r.visitor_id}</div>
                {#if r.snippet}
                  <div class="mt-1 line-clamp-2 max-w-[40ch] text-xs italic text-muted-foreground">
                    {@html '“' + highlightMatch(r.snippet, q) + '”'}
                  </div>
                {/if}
              </a>
            </td>
            <td class="px-3 py-2 text-muted-foreground">{fmtTime(r.started_at)}</td>
            <td class="px-3 py-2 text-muted-foreground">{fmtTime(r.last_message_at)}</td>
            <td class="px-3 py-2 text-right tabular-nums">{r.message_count ?? 0}</td>
            <td class="px-3 py-2">{r.assigned_agent_id || '—'}</td>
            <td class="px-3 py-2">
              <span class="rounded px-2 py-0.5 text-xs {statusClass(r.status)}">
                {statusBadge(r.status)}
              </span>
            </td>
          </tr>
        {/each}
        {#if !loading && rows.length === 0}
          <tr><td colspan="6" class="px-3 py-6 text-center text-muted-foreground">
            {q ? 'No matching messages.' : 'No chats yet.'}
          </td></tr>
        {/if}
      </tbody>
    </table>
  </div>
</section>
