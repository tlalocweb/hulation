<script lang="ts">
  import { onDestroy, onMount } from 'svelte';
  import { base } from '$app/paths';
  import { goto } from '$app/navigation';
  import { page } from '$app/stores';
  import { browser } from '$app/environment';
  import { filters } from '$lib/filters';
  import { chat, type LiveSessionRow } from '$lib/api/chat';
  import { openControlSocket, type ControlSocket } from '$lib/api/agentControlSocket';

  // Live chats — sessions with at least one open WS. The agent
  // control-WS holds presence in the per-server "ready" pool;
  // when a new session arrives, the page auto-routes to
  // /chats/[id] (which acks the assignment via the per-session WS).

  let live: LiveSessionRow[] = [];
  let pollAbort: AbortController | null = null;
  let pollTimer: ReturnType<typeof setInterval> | null = null;
  let control: ControlSocket | null = null;
  let queueDepth = 0;
  let assignedCount = 0;
  let readyAgents: string[] = [];
  let socketStatus: 'connecting' | 'open' | 'closed' | 'closing' | 'error' = 'connecting';

  $: serverID = $filters.server_id;

  async function pollLive() {
    if (!serverID) return;
    pollAbort?.abort();
    pollAbort = new AbortController();
    try {
      const r = await chat.getLiveSessions({ serverId: serverID, signal: pollAbort.signal });
      live = r.sessions ?? [];
    } catch (e) {
      if ((e as { name?: string })?.name === 'AbortError') return;
      // swallow; the WS provides freshness, the poll is belt-and-suspenders
    }
  }

  function openSocket() {
    if (!browser || !serverID) return;
    control?.disconnect();
    control = openControlSocket(serverID);
    control.state.subscribe((s) => {
      socketStatus = s.status;
      queueDepth = s.queued.length;
      assignedCount = s.assigned.length;
      readyAgents = s.readyAgents;
      if (s.incomingAssignment) {
        const id = s.incomingAssignment.session_id;
        // Ack immediately and route to the thread view. The per-
        // session agent-WS opens there and signals "agent took
        // it" implicitly (chat_ws_agent.go calls Router.Ack on
        // upgrade).
        control?.clearIncoming();
        void goto(`${base}/chats/${id}${$page.url.search}`);
      }
    });
  }

  $: if (browser && serverID) {
    openSocket();
  }

  onMount(() => {
    void pollLive();
    pollTimer = setInterval(pollLive, 5_000);
  });
  onDestroy(() => {
    pollAbort?.abort();
    if (pollTimer) clearInterval(pollTimer);
    control?.disconnect();
  });

  function statusDot(): string {
    switch (socketStatus) {
      case 'open':
        return 'bg-emerald-500';
      case 'connecting':
        return 'bg-amber-500';
      case 'error':
      case 'closed':
      case 'closing':
        return 'bg-red-500';
    }
    return 'bg-muted-foreground';
  }
</script>

<section class="space-y-4">
  <header class="flex items-center justify-between gap-4">
    <div>
      <h1 class="text-2xl font-semibold tracking-tight">Live chats</h1>
      <p class="text-sm text-muted-foreground">
        You're {socketStatus === 'open' ? 'in the ready pool' : socketStatus} for <strong>{serverID}</strong>.
        New visitor chats route to you in round-robin order.
      </p>
    </div>
    <div class="flex items-center gap-3 text-xs text-muted-foreground">
      <a href="{base}/chats{$page.url.search}" class="text-primary hover:underline">All chats →</a>
      <span class="flex items-center gap-1">
        <span class="h-2 w-2 rounded-full {statusDot()}"></span>
        {socketStatus}
      </span>
    </div>
  </header>

  <div class="grid grid-cols-1 gap-4 md:grid-cols-3">
    <div class="rounded border bg-card p-3">
      <div class="text-xs text-muted-foreground">Queue depth</div>
      <div class="text-2xl font-semibold">{queueDepth}</div>
    </div>
    <div class="rounded border bg-card p-3">
      <div class="text-xs text-muted-foreground">Assigned (waiting ack)</div>
      <div class="text-2xl font-semibold">{assignedCount}</div>
    </div>
    <div class="rounded border bg-card p-3">
      <div class="text-xs text-muted-foreground">Other ready agents</div>
      <div class="text-2xl font-semibold">{readyAgents.length}</div>
      {#if readyAgents.length > 0}
        <div class="mt-1 text-xs text-muted-foreground truncate">{readyAgents.join(', ')}</div>
      {/if}
    </div>
  </div>

  <div class="overflow-x-auto rounded-lg border bg-card">
    <table class="w-full text-sm">
      <thead class="border-b bg-muted/30 text-left">
        <tr>
          <th class="px-3 py-2 font-medium">Visitor</th>
          <th class="px-3 py-2 font-medium">Online</th>
          <th class="px-3 py-2 font-medium">Agents</th>
          <th class="px-3 py-2 font-medium">Last message</th>
          <th class="px-3 py-2"></th>
        </tr>
      </thead>
      <tbody>
        {#each live as r (r.session_id)}
          <tr class="border-b hover:bg-accent/40">
            <td class="px-3 py-2">
              <div class="font-medium">{r.visitor_email || '—'}</div>
              <div class="text-xs text-muted-foreground">{r.visitor_id}</div>
            </td>
            <td class="px-3 py-2">
              {#if r.visitor_online}
                <span class="text-emerald-600 dark:text-emerald-400">online</span>
              {:else}
                <span class="text-muted-foreground">offline</span>
              {/if}
            </td>
            <td class="px-3 py-2">
              {#if r.agents && r.agents.length > 0}
                {r.agents.join(', ')}
              {:else}
                <span class="text-muted-foreground">—</span>
              {/if}
            </td>
            <td class="px-3 py-2 text-muted-foreground">
              {r.last_message_at ? new Date(r.last_message_at).toLocaleTimeString() : '—'}
            </td>
            <td class="px-3 py-2 text-right">
              <a href="{base}/chats/{r.session_id}{$page.url.search}"
                 class="text-primary hover:underline">Open →</a>
            </td>
          </tr>
        {/each}
        {#if live.length === 0}
          <tr><td colspan="5" class="px-3 py-6 text-center text-muted-foreground">
            No active sessions. New visitor chats will land here automatically.
          </td></tr>
        {/if}
      </tbody>
    </table>
  </div>
</section>
