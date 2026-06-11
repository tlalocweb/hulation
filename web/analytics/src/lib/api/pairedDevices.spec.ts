import { describe, it, expect, beforeEach, vi } from 'vitest';
import { pairedDevices } from './pairedDevices';
import { ApiError } from './analytics';

// Minimal localStorage shim so the bearer header is attached in Vitest's node
// environment (mirrors analytics.spec.ts).
beforeEach(() => {
  const store = new Map<string, string>();
  (globalThis as any).localStorage = {
    getItem: (k: string) => (store.has(k) ? store.get(k)! : null),
    setItem: (k: string, v: string) => void store.set(k, v),
    removeItem: (k: string) => void store.delete(k),
  };
});

describe('pairedDevices API client', () => {
  it('listAll() hits ?all=true with the bearer token and unwraps devices', async () => {
    localStorage.setItem('hula:token', 'test-token');
    const calls: { url: string; init: any }[] = [];
    (globalThis as any).fetch = vi.fn(async (url: string, init: any) => {
      calls.push({ url, init });
      return {
        ok: true,
        json: async () => ({ devices: [{ device_id: 'd-1', user_id: 'alice' }] }),
      } as any;
    });

    const out = await pairedDevices.listAll();
    expect(calls).toHaveLength(1);
    expect(calls[0].url).toBe('/api/v1/pair/devices?all=true');
    expect(calls[0].init.headers.Authorization).toBe('Bearer test-token');
    expect(out).toEqual([{ device_id: 'd-1', user_id: 'alice' }]);
  });

  it('listAll() returns [] when the response omits devices', async () => {
    (globalThis as any).fetch = vi.fn(async () => ({ ok: true, json: async () => ({}) }) as any);
    expect(await pairedDevices.listAll()).toEqual([]);
  });

  it('revoke() POSTs the device_id as JSON', async () => {
    localStorage.setItem('hula:token', 'tok');
    let captured: any = null;
    (globalThis as any).fetch = vi.fn(async (url: string, init: any) => {
      captured = { url, init };
      return { ok: true, json: async () => ({ revoked: true, device_id: 'd-9' }) } as any;
    });

    const res = await pairedDevices.revoke('d-9');
    expect(captured.url).toBe('/api/v1/pair/devices/revoke');
    expect(captured.init.method).toBe('POST');
    expect(captured.init.headers['Content-Type']).toBe('application/json');
    expect(JSON.parse(captured.init.body)).toEqual({ device_id: 'd-9' });
    expect(res.revoked).toBe(true);
  });

  it('throws ApiError on a non-2xx response', async () => {
    (globalThis as any).fetch = vi.fn(async () => ({
      ok: false,
      status: 403,
      json: async () => ({ error: { code: 'forbidden', message: 'nope' } }),
    }) as any);
    await expect(pairedDevices.listAll()).rejects.toBeInstanceOf(ApiError);
  });
});
