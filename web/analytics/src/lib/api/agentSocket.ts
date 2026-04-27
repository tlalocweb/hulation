// Per-session agent WebSocket helper for /chats/[id].
//
// Wraps gorilla/websocket-style frames coming off
// /api/v1/chat/admin/agent-ws into a Svelte store of
// { messages, presence, typing, status }. Reconnects with
// exponential backoff (1, 2, 4, 8 s; cap 30 s). Heartbeats by
// sending a `ping` frame every 25 s.

import { writable, type Readable } from 'svelte/store';
import type { ChatMessage } from './chat';

export interface AgentSocketState {
  status: 'connecting' | 'open' | 'closing' | 'closed' | 'error';
  // Live messages received over the socket. Persisted history is
  // fetched separately via chat.getMessages and merged in the page
  // component.
  messages: ChatMessage[];
  // Presence flags driven by `presence` frames + the
  // initial `presence_snapshot`.
  visitorOnline: boolean;
  agents: string[]; // OTHER agents (not us); admin-username strings
  // typing.visitor: visitor is currently typing; set by `typing`
  // from=visitor frames, auto-cleared after 4 s of silence.
  typingVisitor: boolean;
  typingAgents: string[]; // OTHER agents who are typing
  error?: string;
}

const POLL_RECONNECT_BACKOFF = [1000, 2000, 4000, 8000, 16000, 30000];

export interface AgentSocket {
  state: Readable<AgentSocketState>;
  /** Send a chat message. Returns immediately; the server's
   * `ack` frame eventually arrives via the messages store. */
  sendMessage: (content: string) => void;
  /** Tell the visitor + other agents we're typing. Idempotent;
   * call again with active:false to clear. */
  setTyping: (active: boolean) => void;
  /** Close the session entirely. */
  closeSession: (reason?: string) => void;
  /** Disconnect WITHOUT closing the session (admin navigated away). */
  disconnect: () => void;
}

export function openAgentSocket(opts: { sessionId: string }): AgentSocket {
  const initial: AgentSocketState = {
    status: 'connecting',
    messages: [],
    visitorOnline: false,
    agents: [],
    typingVisitor: false,
    typingAgents: [],
  };
  const store = writable<AgentSocketState>(initial);

  let ws: WebSocket | null = null;
  let backoffIdx = 0;
  let pingTimer: ReturnType<typeof setInterval> | null = null;
  let visitorTypingTimer: ReturnType<typeof setTimeout> | null = null;
  const agentTypingTimers = new Map<string, ReturnType<typeof setTimeout>>();
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  let manualDisconnect = false;

  function url(): string {
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    const tok = localStorage.getItem('hula:token') ?? '';
    return `${proto}//${location.host}/api/v1/chat/admin/agent-ws?session_id=${encodeURIComponent(
      opts.sessionId,
    )}&token=${encodeURIComponent(tok)}`;
  }

  function update(fn: (s: AgentSocketState) => AgentSocketState) {
    store.update(fn);
  }

  function clearTimers() {
    if (pingTimer) {
      clearInterval(pingTimer);
      pingTimer = null;
    }
    if (visitorTypingTimer) {
      clearTimeout(visitorTypingTimer);
      visitorTypingTimer = null;
    }
    for (const t of agentTypingTimers.values()) clearTimeout(t);
    agentTypingTimers.clear();
    if (reconnectTimer) {
      clearTimeout(reconnectTimer);
      reconnectTimer = null;
    }
  }

  function scheduleReconnect() {
    if (manualDisconnect) return;
    const delay = POLL_RECONNECT_BACKOFF[Math.min(backoffIdx, POLL_RECONNECT_BACKOFF.length - 1)];
    backoffIdx = Math.min(backoffIdx + 1, POLL_RECONNECT_BACKOFF.length - 1);
    reconnectTimer = setTimeout(connect, delay);
  }

  function connect() {
    update((s) => ({ ...s, status: 'connecting', error: undefined }));
    try {
      ws = new WebSocket(url());
    } catch (e) {
      update((s) => ({ ...s, status: 'error', error: String(e) }));
      scheduleReconnect();
      return;
    }
    ws.addEventListener('open', () => {
      backoffIdx = 0;
      update((s) => ({ ...s, status: 'open' }));
      pingTimer = setInterval(() => {
        try {
          ws?.send(JSON.stringify({ type: 'ping' }));
        } catch {
          /* socket already closing */
        }
      }, 25_000);
    });
    ws.addEventListener('close', () => {
      clearTimers();
      update((s) => ({ ...s, status: 'closed' }));
      scheduleReconnect();
    });
    ws.addEventListener('error', () => {
      update((s) => ({ ...s, status: 'error', error: 'websocket error' }));
    });
    ws.addEventListener('message', (ev) => {
      let frame: any;
      try {
        frame = JSON.parse(String(ev.data));
      } catch {
        return;
      }
      handleFrame(frame);
    });
  }

  function handleFrame(frame: any) {
    switch (frame.type) {
      case 'presence_snapshot':
        update((s) => ({
          ...s,
          visitorOnline: !!frame.visitor_online,
          agents: Array.isArray(frame.agents) ? frame.agents : [],
        }));
        break;
      case 'presence': {
        const ev = frame.event as string;
        const agent = String(frame.agent ?? '');
        update((s) => {
          const next = { ...s };
          if (ev === 'visitor_connected') next.visitorOnline = true;
          else if (ev === 'visitor_disconnected') next.visitorOnline = false;
          else if (ev === 'agent_joined' && agent) {
            next.agents = next.agents.includes(agent) ? next.agents : [...next.agents, agent];
          } else if (ev === 'agent_left' && agent) {
            next.agents = next.agents.filter((a) => a !== agent);
          }
          return next;
        });
        break;
      }
      case 'msg':
        update((s) => ({
          ...s,
          messages: [
            ...s.messages,
            {
              id: frame.id,
              direction: protoDirection(frame.direction),
              content: frame.content,
              when: frame.ts,
              sender_id: frame.agent ?? '',
            },
          ],
        }));
        break;
      case 'ack':
        // Sender-side echo. Page component handles optimistic
        // dedupe; we don't append the frame on its own.
        break;
      case 'typing': {
        const from = String(frame.from ?? '');
        const active = !!frame.active;
        if (from === 'visitor') {
          update((s) => ({ ...s, typingVisitor: active }));
          if (active) {
            if (visitorTypingTimer) clearTimeout(visitorTypingTimer);
            visitorTypingTimer = setTimeout(() => {
              update((s) => ({ ...s, typingVisitor: false }));
            }, 4_000);
          }
        } else if (from === 'agent') {
          const agent = String(frame.agent ?? '');
          if (!agent) break;
          update((s) => {
            const arr = active
              ? s.typingAgents.includes(agent)
                ? s.typingAgents
                : [...s.typingAgents, agent]
              : s.typingAgents.filter((a) => a !== agent);
            return { ...s, typingAgents: arr };
          });
          if (active) {
            const old = agentTypingTimers.get(agent);
            if (old) clearTimeout(old);
            agentTypingTimers.set(
              agent,
              setTimeout(() => {
                update((s) => ({ ...s, typingAgents: s.typingAgents.filter((a) => a !== agent) }));
              }, 4_000),
            );
          }
        }
        break;
      }
      case 'system':
        update((s) => ({
          ...s,
          messages: [
            ...s.messages,
            {
              direction: 'CHAT_MESSAGE_DIRECTION_SYSTEM',
              content: frame.content,
              when: new Date().toISOString(),
            },
          ],
        }));
        break;
      case 'error':
        update((s) => ({ ...s, error: `${frame.code}: ${frame.message}` }));
        break;
    }
  }

  function protoDirection(d: string): ChatMessage['direction'] {
    switch (d) {
      case 'visitor':
        return 'CHAT_MESSAGE_DIRECTION_VISITOR';
      case 'agent':
        return 'CHAT_MESSAGE_DIRECTION_AGENT';
      case 'system':
        return 'CHAT_MESSAGE_DIRECTION_SYSTEM';
      case 'bot':
        return 'CHAT_MESSAGE_DIRECTION_BOT';
    }
    return 'CHAT_MESSAGE_DIRECTION_UNSPECIFIED';
  }

  function rawSend(payload: unknown) {
    if (!ws || ws.readyState !== WebSocket.OPEN) return;
    try {
      ws.send(JSON.stringify(payload));
    } catch {
      /* socket already closing */
    }
  }

  connect();

  return {
    state: { subscribe: store.subscribe },
    sendMessage: (content) => rawSend({ type: 'msg', content }),
    setTyping: (active) => rawSend({ type: 'typing', active }),
    closeSession: (reason) => rawSend({ type: 'close', reason: reason ?? '' }),
    disconnect: () => {
      manualDisconnect = true;
      clearTimers();
      try {
        ws?.close(1000, 'client disconnect');
      } catch {
        /* ignore */
      }
      update((s) => ({ ...s, status: 'closing' }));
    },
  };
}
