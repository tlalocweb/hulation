<script lang="ts">
  import { onDestroy, onMount, tick } from 'svelte';
  import { base } from '$app/paths';
  import { page } from '$app/stores';
  import { browser } from '$app/environment';
  import { filters } from '$lib/filters';
  import { chat, type ChatMessage, type ChatSession } from '$lib/api/chat';
  import { openAgentSocket, type AgentSocket, type AgentSocketState } from '$lib/api/agentSocket';

  // Live thread view + admin compose. Opens the per-session
  // agent-WS on mount; persists history is fetched once via
  // chat.getMessages and merged with the live stream.

  let sessionID: string;
  let serverID: string;
  let session: ChatSession | null = null;
  let history: ChatMessage[] = [];
  let composing = '';
  let composing_was_typing = false;
  let typingTimer: ReturnType<typeof setTimeout> | null = null;
  let listEl: HTMLDivElement | null = null;
  let socket: AgentSocket | null = null;
  let socketState: AgentSocketState | null = null;
  let pendingClose = false;
  let error = '';

  $: sessionID = $page.params.id;
  $: serverID = $filters.server_id;

  onMount(async () => {
    if (!serverID || !sessionID) return;
    try {
      session = await chat.getSession({ serverId: serverID, sessionId: sessionID });
      const r = await chat.getMessages({ serverId: serverID, sessionId: sessionID, limit: 500 });
      history = r.messages ?? [];
      await tick();
      scrollToBottom();
    } catch (e) {
      error = (e as Error).message;
    }
    if (browser) {
      socket = openAgentSocket({ sessionId: sessionID });
      socket.state.subscribe(async (s) => {
        socketState = s;
        await tick();
        scrollToBottom();
      });
    }
  });
  onDestroy(() => socket?.disconnect());

  // Combined timeline: history + live socket messages, deduped by id.
  $: combined = mergeMessages(history, socketState?.messages ?? []);

  function mergeMessages(prev: ChatMessage[], live: ChatMessage[]): ChatMessage[] {
    const seen = new Set<string>();
    const out: ChatMessage[] = [];
    for (const m of [...prev, ...live]) {
      const key = m.id ?? `${m.when}|${m.content}|${m.direction}`;
      if (seen.has(key)) continue;
      seen.add(key);
      out.push(m);
    }
    return out.sort((a, b) => (a.when ?? '').localeCompare(b.when ?? ''));
  }

  function scrollToBottom() {
    if (!listEl) return;
    listEl.scrollTop = listEl.scrollHeight;
  }

  async function send() {
    const content = composing.trim();
    if (!content) return;
    composing = '';
    if (typingTimer) clearTimeout(typingTimer);
    if (composing_was_typing) {
      socket?.setTyping(false);
      composing_was_typing = false;
    }
    // Optimistic append; the server's `msg` echo arrives via the
    // socket and merges by id.
    const optimistic: ChatMessage = {
      id: `optimistic-${Date.now()}`,
      direction: 'CHAT_MESSAGE_DIRECTION_AGENT',
      content,
      when: new Date().toISOString(),
      sender_id: 'me',
    };
    history = [...history, optimistic];
    await tick();
    scrollToBottom();
    if (socketState?.status === 'open') {
      socket?.sendMessage(content);
    } else {
      // WS closed — fall back to REST.
      try {
        await chat.postMessage({ serverId: serverID, sessionId: sessionID, content });
      } catch (e) {
        error = `send failed: ${(e as Error).message}`;
      }
    }
  }

  function onComposeKey(ev: KeyboardEvent) {
    if (ev.key === 'Enter' && !ev.shiftKey) {
      ev.preventDefault();
      void send();
      return;
    }
    if (!composing_was_typing) {
      socket?.setTyping(true);
      composing_was_typing = true;
    }
    if (typingTimer) clearTimeout(typingTimer);
    typingTimer = setTimeout(() => {
      socket?.setTyping(false);
      composing_was_typing = false;
    }, 3_000);
  }

  async function closeSession() {
    pendingClose = true;
    try {
      socket?.closeSession('resolved');
    } catch {
      /* ignore */
    }
    try {
      await chat.closeSession({ serverId: serverID, sessionId: sessionID, reason: 'resolved' });
    } catch (e) {
      error = `close failed: ${(e as Error).message}`;
    } finally {
      pendingClose = false;
    }
  }

  function dirClass(d?: ChatMessage['direction']): string {
    switch (d) {
      case 'CHAT_MESSAGE_DIRECTION_VISITOR':
        return 'self-start bg-muted text-foreground';
      case 'CHAT_MESSAGE_DIRECTION_AGENT':
        return 'self-end bg-primary text-primary-foreground';
      case 'CHAT_MESSAGE_DIRECTION_SYSTEM':
        return 'self-center bg-amber-500/10 text-amber-700 dark:text-amber-300 italic';
      case 'CHAT_MESSAGE_DIRECTION_BOT':
        return 'self-start bg-sky-500/15 text-sky-700 dark:text-sky-300';
    }
    return 'self-start bg-muted';
  }

  function fmtTime(s?: string): string {
    if (!s) return '';
    try {
      return new Date(s).toLocaleTimeString();
    } catch {
      return s;
    }
  }
</script>

<section class="flex h-full flex-col gap-3">
  <header class="flex items-start justify-between gap-4 rounded border bg-card p-3">
    <div>
      <a href="{base}/chats{$page.url.search}" class="text-xs text-primary hover:underline">← All chats</a>
      <h1 class="mt-1 text-lg font-semibold">
        {session?.visitor_email || 'Chat'}
      </h1>
      <div class="text-xs text-muted-foreground">
        {session?.visitor_id ?? ''}
        {#if session?.visitor_country}· {session.visitor_country}{/if}
        {#if session?.visitor_device}· {session.visitor_device}{/if}
      </div>
    </div>
    <div class="flex flex-col items-end gap-1 text-xs">
      <span class="rounded px-2 py-0.5 {socketState?.visitorOnline ? 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-300' : 'bg-muted text-muted-foreground'}">
        Visitor {socketState?.visitorOnline ? 'online' : 'offline'}
      </span>
      {#if socketState && socketState.agents.length > 0}
        <span class="text-muted-foreground">Other agents: {socketState.agents.join(', ')}</span>
      {/if}
      <button
        class="mt-1 rounded border border-destructive bg-destructive/10 px-2 py-1 text-destructive hover:bg-destructive hover:text-destructive-foreground disabled:opacity-50"
        on:click={closeSession}
        disabled={pendingClose || session?.status === 'CHAT_SESSION_STATUS_CLOSED'}
      >
        {session?.status === 'CHAT_SESSION_STATUS_CLOSED' ? 'Closed' : 'Close session'}
      </button>
    </div>
  </header>

  {#if error}
    <div class="rounded border border-destructive bg-destructive/10 p-2 text-xs text-destructive">
      {error}
    </div>
  {/if}

  <div bind:this={listEl} class="flex-1 overflow-y-auto rounded border bg-card p-3">
    <div class="flex flex-col gap-2">
      {#each combined as m (m.id ?? `${m.when}|${m.content}`)}
        <div class="max-w-[75%] rounded px-3 py-2 text-sm {dirClass(m.direction)}">
          {#if m.direction === 'CHAT_MESSAGE_DIRECTION_AGENT' && m.sender_id && m.sender_id !== 'me'}
            <div class="text-[10px] font-semibold uppercase opacity-70">{m.sender_id}</div>
          {/if}
          <div class="whitespace-pre-wrap">{m.content}</div>
          <div class="mt-1 text-[10px] opacity-70 tabular-nums">{fmtTime(m.when)}</div>
        </div>
      {/each}
      {#if socketState?.typingVisitor}
        <div class="self-start rounded bg-muted px-3 py-2 text-xs italic text-muted-foreground">
          visitor is typing…
        </div>
      {/if}
      {#if socketState?.typingAgents && socketState.typingAgents.length > 0}
        <div class="self-end rounded bg-primary/10 px-3 py-2 text-xs italic text-primary">
          {socketState.typingAgents.join(', ')} typing…
        </div>
      {/if}
    </div>
  </div>

  <div class="rounded border bg-card p-2">
    <textarea
      class="w-full resize-none border-0 bg-transparent p-2 text-sm focus:outline-none"
      placeholder="Type a reply… (Enter to send, Shift+Enter for newline)"
      rows="2"
      bind:value={composing}
      on:keydown={onComposeKey}
    ></textarea>
    <div class="flex items-center justify-between">
      <div class="text-[10px] text-muted-foreground">
        {socketState?.status === 'open' ? 'Live (WS)' : `Socket ${socketState?.status ?? 'connecting'}`}
      </div>
      <button
        class="rounded bg-primary px-3 py-1 text-sm text-primary-foreground hover:opacity-90 disabled:opacity-50"
        on:click={send}
        disabled={!composing.trim()}
      >
        Send
      </button>
    </div>
  </div>
</section>
