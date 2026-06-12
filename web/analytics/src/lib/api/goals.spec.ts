import { describe, it, expect, beforeEach, vi } from 'vitest';
import { goals } from './goals';
import { ApiError, TOKEN_KEY } from './http';

// localStorage shim for the bearer header (Vitest node env).
beforeEach(() => {
  const store = new Map<string, string>();
  (globalThis as any).localStorage = {
    getItem: (k: string) => (store.has(k) ? store.get(k)! : null),
    setItem: (k: string, v: string) => void store.set(k, v),
    removeItem: (k: string) => void store.delete(k),
  };
});

describe('goals API client (shared http.handle)', () => {
  it('list() sends the bearer token and unwraps goals', async () => {
    localStorage.setItem(TOKEN_KEY, 'tok');
    const calls: { url: string; init: any }[] = [];
    (globalThis as any).fetch = vi.fn(async (url: string, init: any) => {
      calls.push({ url, init });
      return { ok: true, json: async () => ({ goals: [{ id: 'g1', name: 'Signup' }] }) } as any;
    });

    const out = await goals.list('mysite');
    expect(calls[0].url).toBe('/api/v1/goals/mysite');
    expect(calls[0].init.headers.Authorization).toBe('Bearer tok');
    expect(out).toEqual([{ id: 'g1', name: 'Signup' }]);
  });

  it('list() returns [] when the response omits goals', async () => {
    (globalThis as any).fetch = vi.fn(async () => ({ ok: true, json: async () => ({}) }) as any);
    expect(await goals.list('s')).toEqual([]);
  });

  // The double-read regression guard: handle() must read the body once via
  // text() and JSON.parse it — a mock that only exposed json() would have hidden
  // the bug. Here we provide text() (like a real Response) and assert the parsed
  // body is surfaced on the ApiError.
  it('non-2xx throws ApiError with the JSON body parsed from text()', async () => {
    (globalThis as any).fetch = vi.fn(async () => ({
      ok: false,
      status: 404,
      text: async () => JSON.stringify({ error: { code: 'not_found', message: 'no such goal' } }),
    }) as any);

    const err = await goals.list('s').catch((e) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).status).toBe(404);
    expect((err as ApiError).body).toEqual({
      error: { code: 'not_found', message: 'no such goal' },
    });
  });

  it('non-JSON error body is surfaced as raw text', async () => {
    (globalThis as any).fetch = vi.fn(async () => ({
      ok: false,
      status: 502,
      text: async () => 'upstream is down',
    }) as any);
    const err = await goals.list('s').catch((e) => e);
    expect((err as ApiError).body).toBe('upstream is down');
  });
});
