// Typed fetch wrappers for the Phase-4b ChatService. Shapes mirror
// pkg/apispec/v1/chat/chat.proto. REST gateway runs with
// UseProtoNames=true so JSON is snake_case.
//
// The visitor-facing /chat/start + /chat/ws endpoints are NOT
// modelled here — they're reached from the tlalocwebsite chat
// widget, not from the hula admin SPA. This file is admin-only.

import { ApiError } from './analytics';

export type ChatSessionStatus =
  | 'CHAT_SESSION_STATUS_UNSPECIFIED'
  | 'CHAT_SESSION_STATUS_QUEUED'
  | 'CHAT_SESSION_STATUS_ASSIGNED'
  | 'CHAT_SESSION_STATUS_OPEN'
  | 'CHAT_SESSION_STATUS_CLOSED'
  | 'CHAT_SESSION_STATUS_EXPIRED';

export type ChatMessageDirection =
  | 'CHAT_MESSAGE_DIRECTION_UNSPECIFIED'
  | 'CHAT_MESSAGE_DIRECTION_VISITOR'
  | 'CHAT_MESSAGE_DIRECTION_AGENT'
  | 'CHAT_MESSAGE_DIRECTION_SYSTEM'
  | 'CHAT_MESSAGE_DIRECTION_BOT';

export interface ChatSession {
  id?: string;
  server_id?: string;
  visitor_id?: string;
  visitor_email?: string;
  visitor_country?: string;
  visitor_device?: string;
  visitor_ip?: string;
  user_agent?: string;
  started_at?: string;
  closed_at?: string | null;
  last_message_at?: string;
  message_count?: number;
  status?: ChatSessionStatus;
  assigned_agent_id?: string;
  assigned_at?: string | null;
  meta?: string;
}

export interface ChatMessage {
  id?: string;
  session_id?: string;
  server_id?: string;
  visitor_id?: string;
  direction?: ChatMessageDirection;
  sender_id?: string;
  content?: string;
  when?: string;
}

export interface LiveSessionRow {
  session_id?: string;
  visitor_id?: string;
  visitor_email?: string;
  visitor_online?: boolean;
  agents?: string[];
  last_message_at?: string;
}

const TOKEN_KEY = 'hula:token';
function authHeaders(): Record<string, string> {
  const h: Record<string, string> = {};
  if (typeof localStorage !== 'undefined') {
    const t = localStorage.getItem(TOKEN_KEY);
    if (t) h.Authorization = `Bearer ${t}`;
  }
  return h;
}

async function getJSON<T>(url: string, signal?: AbortSignal): Promise<T> {
  const res = await fetch(url, { headers: authHeaders(), signal });
  if (!res.ok) {
    throw new ApiError(res.status, await res.text().catch(() => ''));
  }
  return (await res.json()) as T;
}

async function postJSON<T>(url: string, body: unknown, signal?: AbortSignal): Promise<T> {
  const res = await fetch(url, {
    method: 'POST',
    headers: { ...authHeaders(), 'content-type': 'application/json' },
    body: JSON.stringify(body),
    signal,
  });
  if (!res.ok) {
    throw new ApiError(res.status, await res.text().catch(() => ''));
  }
  return (await res.json()) as T;
}

function qs(params: Record<string, unknown>): string {
  const out = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined || v === null || v === '') continue;
    if (Array.isArray(v)) {
      for (const item of v) out.append(k, String(item));
    } else {
      out.set(k, String(v));
    }
  }
  const s = out.toString();
  return s ? '?' + s : '';
}

const BASE = '/api/v1/chat/admin';

export const chat = {
  listSessions: (opts: {
    serverId: string;
    status?: ChatSessionStatus[];
    from?: string;
    to?: string;
    q?: string;
    limit?: number;
    offset?: number;
    signal?: AbortSignal;
  }) =>
    getJSON<{ sessions: ChatSession[]; total_count: string }>(
      `${BASE}/sessions${qs({
        server_id: opts.serverId,
        status: opts.status,
        from: opts.from,
        to: opts.to,
        q: opts.q,
        limit: opts.limit,
        offset: opts.offset,
      })}`,
      opts.signal,
    ),

  getSession: (opts: { serverId: string; sessionId: string; signal?: AbortSignal }) =>
    getJSON<ChatSession>(
      `${BASE}/sessions/${encodeURIComponent(opts.sessionId)}${qs({ server_id: opts.serverId })}`,
      opts.signal,
    ),

  getMessages: (opts: {
    serverId: string;
    sessionId: string;
    limit?: number;
    offset?: number;
    signal?: AbortSignal;
  }) =>
    getJSON<{ messages: ChatMessage[]; total_count: string }>(
      `${BASE}/sessions/${encodeURIComponent(opts.sessionId)}/messages${qs({
        server_id: opts.serverId,
        limit: opts.limit,
        offset: opts.offset,
      })}`,
      opts.signal,
    ),

  postMessage: (opts: { serverId: string; sessionId: string; content: string; signal?: AbortSignal }) =>
    postJSON<ChatMessage>(
      `${BASE}/sessions/${encodeURIComponent(opts.sessionId)}/messages`,
      { server_id: opts.serverId, content: opts.content },
      opts.signal,
    ),

  closeSession: (opts: { serverId: string; sessionId: string; reason?: string; signal?: AbortSignal }) =>
    postJSON<{ session: ChatSession }>(
      `${BASE}/sessions/${encodeURIComponent(opts.sessionId)}/close`,
      { server_id: opts.serverId, reason: opts.reason ?? '' },
      opts.signal,
    ),

  takeSession: (opts: { serverId: string; sessionId: string; force?: boolean; signal?: AbortSignal }) =>
    postJSON<ChatSession>(
      `${BASE}/sessions/${encodeURIComponent(opts.sessionId)}/take`,
      { server_id: opts.serverId, force: !!opts.force },
      opts.signal,
    ),

  releaseSession: (opts: { serverId: string; sessionId: string; signal?: AbortSignal }) =>
    postJSON<ChatSession>(
      `${BASE}/sessions/${encodeURIComponent(opts.sessionId)}/release`,
      { server_id: opts.serverId },
      opts.signal,
    ),

  getQueue: (opts: { serverId: string; signal?: AbortSignal }) =>
    getJSON<{ queued: ChatSession[]; assigned: ChatSession[] }>(
      `${BASE}/queue${qs({ server_id: opts.serverId })}`,
      opts.signal,
    ),

  getLiveSessions: (opts: { serverId: string; signal?: AbortSignal }) =>
    getJSON<{ sessions: LiveSessionRow[] }>(
      `${BASE}/live-sessions${qs({ server_id: opts.serverId })}`,
      opts.signal,
    ),

  searchMessages: (opts: {
    serverId: string;
    q: string;
    from?: string;
    to?: string;
    limit?: number;
    offset?: number;
    signal?: AbortSignal;
  }) =>
    getJSON<{ messages: ChatMessage[]; total_count: string }>(
      `${BASE}/messages/search${qs({
        server_id: opts.serverId,
        q: opts.q,
        from: opts.from,
        to: opts.to,
        limit: opts.limit,
        offset: opts.offset,
      })}`,
      opts.signal,
    ),
};
