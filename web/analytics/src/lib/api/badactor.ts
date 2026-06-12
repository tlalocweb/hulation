// Typed fetch wrappers for the badactor admin endpoints.
//
// The hula server exposes plain net/http handlers under /api/badactor/*
// (see server/h2handler.go and server/unified_fallback.go). The admin
// middleware on the server side rejects non-admin callers, so the UI
// only needs to hide the entry point — the browser still gets a 401/403
// if a non-admin user crafts the request manually.

import { ApiError, authHeaders, handle } from './http';

export interface BadActorEntry {
  ip: string;
  score: number;
  detected_at: string;
  expires_at: string;
  last_reason: string;
  blocked: boolean;
}

export interface BadActorStats {
  enabled: boolean;
  dry_run: boolean;
  block_threshold: number;
  ttl: string;
  blocked_ips: number;
  allowlisted_ips: number;
  signatures: number;
}

export interface SignatureInfo {
  name: string;
  type: string;
  score: number;
  reason: string;
  category: string;
}

export const badactor = {
  list: async (): Promise<BadActorEntry[]> => {
    const res = await fetch('/api/badactor/list', { headers: authHeaders() });
    return handle<BadActorEntry[]>(res);
  },

  evict: async (ip: string): Promise<void> => {
    const res = await fetch(`/api/badactor/block/${encodeURIComponent(ip)}`, {
      method: 'DELETE',
      headers: authHeaders(),
    });
    await handle<unknown>(res);
  },

  manualBlock: async (ip: string, reason: string): Promise<void> => {
    const res = await fetch('/api/badactor/block', {
      method: 'POST',
      headers: { ...authHeaders(), 'Content-Type': 'application/json' },
      body: JSON.stringify({ ip, reason }),
    });
    await handle<unknown>(res);
  },

  stats: async (): Promise<BadActorStats> => {
    const res = await fetch('/api/badactor/stats', { headers: authHeaders() });
    return handle<BadActorStats>(res);
  },

  signatures: async (): Promise<SignatureInfo[]> => {
    const res = await fetch('/api/badactor/signatures', { headers: authHeaders() });
    return handle<SignatureInfo[]>(res);
  },

  allowlist: {
    list: async (): Promise<string[]> => {
      const res = await fetch('/api/badactor/allowlist', { headers: authHeaders() });
      return handle<string[]>(res);
    },

    add: async (ip: string, reason?: string): Promise<void> => {
      const res = await fetch('/api/badactor/allowlist', {
        method: 'POST',
        headers: { ...authHeaders(), 'Content-Type': 'application/json' },
        body: JSON.stringify({ ip, reason: reason ?? '' }),
      });
      await handle<unknown>(res);
    },

    remove: async (ip: string): Promise<void> => {
      const res = await fetch(`/api/badactor/allowlist/${encodeURIComponent(ip)}`, {
        method: 'DELETE',
        headers: authHeaders(),
      });
      await handle<unknown>(res);
    },
  },
};
