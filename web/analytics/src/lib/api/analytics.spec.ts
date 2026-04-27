import { describe, it, expect, beforeEach, vi } from 'vitest';
import { analytics, ApiError, setToken, clearToken } from './analytics';

// Minimal localStorage shim so setToken/clearToken work in Vitest's
// node environment. Real browser covers the DOM paths at build time.
beforeEach(() => {
  const store = new Map<string, string>();
  (globalThis as any).localStorage = {
    getItem: (k: string) => (store.has(k) ? store.get(k)! : null),
    setItem: (k: string, v: string) => void store.set(k, v),
    removeItem: (k: string) => void store.delete(k),
  };
});

describe('analytics fetch wrapper', () => {
  it('flattens filters into filters.* query params and includes the bearer token', async () => {
    setToken('test-token');
    const calls: { url: string; headers: any }[] = [];
    (globalThis as any).fetch = vi.fn(async (url: string, init: any) => {
      calls.push({ url, headers: init.headers });
      return {
        ok: true,
        json: async () => ({ visitors: 0, pageviews: 0 }),
      } as any;
    });
    await analytics.summary({
      serverId: 'testsite',
      filters: { from: '2026-01-01T00:00:00Z', to: '2026-01-02T00:00:00Z', country: 'US' },
    });
    expect(calls).toHaveLength(1);
    expect(calls[0].url).toMatch(/^\/api\/v1\/analytics\/summary\?/);
    expect(calls[0].url).toContain('server_id=testsite');
    expect(calls[0].url).toContain('filters.from=2026-01-01T00%3A00%3A00Z');
    expect(calls[0].url).toContain('filters.country=US');
    expect(calls[0].headers.Authorization).toBe('Bearer test-token');
  });

  it('omits empty filter fields', async () => {
    setToken('t');
    const calls: string[] = [];
    (globalThis as any).fetch = vi.fn(async (url: string) => {
      calls.push(url);
      return { ok: true, json: async () => ({ rows: [] }) } as any;
    });
    await analytics.pages({
      serverId: 's',
      filters: { from: '2026-01-01T00:00:00Z', to: '2026-01-02T00:00:00Z', country: '' },
      limit: 25,
    });
    expect(calls[0]).not.toContain('filters.country');
    expect(calls[0]).toContain('limit=25');
  });

  it('throws ApiError on non-2xx', async () => {
    clearToken();
    (globalThis as any).fetch = vi.fn(async () => ({
      ok: false,
      status: 401,
      json: async () => ({ code: 16, message: 'no claims' }),
    }));
    await expect(
      analytics.summary({ serverId: 's', filters: { from: 'x', to: 'y' } })
    ).rejects.toBeInstanceOf(ApiError);
  });
});
