// Agent control WebSocket helper for /chats/live.
//
// While connected, the admin is in the per-server "ready agent"
// pool the router picks from. The store surfaces the queue
// snapshot + new session_assigned events so the page can pop a
// banner / auto-route to the thread view.

import { writable, type Readable } from 'svelte/store';

export interface ControlSnapshot {
  status: 'connecting' | 'open' | 'closing' | 'closed' | 'error';
  queued: { session_id: string; queued_for_seconds: number }[];
  assigned: { session_id: string; agent: string }[];
  readyAgents: string[];
  /** Set whenever a new session_assigned arrives. The page
   * subscribes and routes to /chats/[id] on change. */
  incomingAssignment?: {
    session_id: string;
    visitor_email?: string;
    first_message?: string;
    queued_for_seconds: number;
    /** Local clock when the assignment landed; the page's banner
     * uses this to render a "X seconds ago" countdown for the
     * 30-second auto-decline. */
    received_at: number;
  };
  error?: string;
}

const POLL_RECONNECT_BACKOFF = [1000, 2000, 4000, 8000, 16000, 30000];

export interface ControlSocket {
  state: Readable<ControlSnapshot>;
  ack: (sessionId: string) => void;
  decline: (sessionId: string) => void;
  /** Clear the incomingAssignment field — call after the page has
   * routed to /chats/[id]. */
  clearIncoming: () => void;
  disconnect: () => void;
}

export function openControlSocket(serverId: string): ControlSocket {
  const initial: ControlSnapshot = {
    status: 'connecting',
    queued: [],
    assigned: [],
    readyAgents: [],
  };
  const store = writable<ControlSnapshot>(initial);

  let ws: WebSocket | null = null;
  let backoffIdx = 0;
  let pingTimer: ReturnType<typeof setInterval> | null = null;
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  let manualDisconnect = false;

  function url(): string {
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    const tok = localStorage.getItem('hula:token') ?? '';
    return `${proto}//${location.host}/api/v1/chat/admin/agent-control-ws?server_id=${encodeURIComponent(
      serverId,
    )}&token=${encodeURIComponent(tok)}`;
  }

  function clearTimers() {
    if (pingTimer) {
      clearInterval(pingTimer);
      pingTimer = null;
    }
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
    store.update((s) => ({ ...s, status: 'connecting', error: undefined }));
    try {
      ws = new WebSocket(url());
    } catch (e) {
      store.update((s) => ({ ...s, status: 'error', error: String(e) }));
      scheduleReconnect();
      return;
    }
    ws.addEventListener('open', () => {
      backoffIdx = 0;
      store.update((s) => ({ ...s, status: 'open' }));
      pingTimer = setInterval(() => {
        try {
          ws?.send(JSON.stringify({ type: 'ping' }));
        } catch {
          /* ignore */
        }
      }, 25_000);
    });
    ws.addEventListener('close', () => {
      clearTimers();
      store.update((s) => ({ ...s, status: 'closed' }));
      scheduleReconnect();
    });
    ws.addEventListener('error', () => {
      store.update((s) => ({ ...s, status: 'error', error: 'websocket error' }));
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
      case 'queue_snapshot':
        store.update((s) => ({
          ...s,
          queued: Array.isArray(frame.queued) ? frame.queued : [],
          assigned: Array.isArray(frame.assigned) ? frame.assigned : [],
          readyAgents: Array.isArray(frame.ready_agents) ? frame.ready_agents : [],
        }));
        break;
      case 'session_assigned':
        store.update((s) => ({
          ...s,
          incomingAssignment: {
            session_id: String(frame.session_id ?? ''),
            visitor_email: frame.visitor_email,
            first_message: frame.first_message,
            queued_for_seconds: Number(frame.queued_for_seconds ?? 0),
            received_at: Date.now(),
          },
        }));
        break;
      case 'session_released':
        // No incoming for us anymore. Page polls /admin/queue or
        // listens for the next session_assigned.
        break;
      case 'error':
        store.update((s) => ({ ...s, error: `${frame.code}: ${frame.message}` }));
        break;
    }
  }

  function rawSend(payload: unknown) {
    if (!ws || ws.readyState !== WebSocket.OPEN) return;
    try {
      ws.send(JSON.stringify(payload));
    } catch {
      /* ignore */
    }
  }

  connect();

  return {
    state: { subscribe: store.subscribe },
    ack: (sessionId) => rawSend({ type: 'ack', session_id: sessionId }),
    decline: (sessionId) => rawSend({ type: 'decline', session_id: sessionId }),
    clearIncoming: () => store.update((s) => ({ ...s, incomingAssignment: undefined })),
    disconnect: () => {
      manualDisconnect = true;
      clearTimers();
      try {
        ws?.close(1000, 'client disconnect');
      } catch {
        /* ignore */
      }
      store.update((s) => ({ ...s, status: 'closing' }));
    },
  };
}
