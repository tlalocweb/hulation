// Typed fetch wrappers for NotifyService. REST gateway uses
// UseProtoNames=true so the JSON shape is snake_case.

import { ApiError } from './analytics';

export interface NotificationPrefs {
  user_id: string;
  email_enabled?: boolean;
  push_enabled?: boolean;
  timezone?: string;
  quiet_hours_start?: string;
  quiet_hours_end?: string;
  updated_at?: string;
}

export interface TestChannelResult {
  channel: string; // "email" | "apns" | "fcm"
  ok?: boolean;
  error?: string;
}

export interface Device {
  id: string;
  user_id: string;
  platform?: 'PLATFORM_UNSPECIFIED' | 'PLATFORM_APNS' | 'PLATFORM_FCM';
  device_fingerprint?: string;
  label?: string;
  registered_at?: string;
  last_seen_at?: string;
  active?: boolean;
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

async function handle<T>(res: Response): Promise<T> {
  if (!res.ok) {
    let body: unknown = null;
    try {
      body = await res.json();
    } catch {
      body = await res.text();
    }
    throw new ApiError(res.status, body);
  }
  return (await res.json()) as T;
}

export const notify = {
  list: async (): Promise<NotificationPrefs[]> => {
    const res = await fetch('/api/v1/notify/prefs', { headers: authHeaders() });
    const data = await handle<{ rows?: NotificationPrefs[] }>(res);
    return data.rows ?? [];
  },

  get: async (userId: string): Promise<NotificationPrefs> => {
    const res = await fetch(`/api/v1/notify/prefs/${encodeURIComponent(userId)}`, {
      headers: authHeaders(),
    });
    return handle<NotificationPrefs>(res);
  },

  set: async (userId: string, prefs: NotificationPrefs): Promise<NotificationPrefs> => {
    const res = await fetch(`/api/v1/notify/prefs/${encodeURIComponent(userId)}`, {
      method: 'PATCH',
      headers: { ...authHeaders(), 'Content-Type': 'application/json' },
      body: JSON.stringify({ prefs }),
    });
    return handle<NotificationPrefs>(res);
  },

  test: async (userId: string, subject?: string, body?: string): Promise<TestChannelResult[]> => {
    const res = await fetch(`/api/v1/notify/test/${encodeURIComponent(userId)}`, {
      method: 'POST',
      headers: { ...authHeaders(), 'Content-Type': 'application/json' },
      body: JSON.stringify({ subject, body }),
    });
    const data = await handle<{ results?: TestChannelResult[] }>(res);
    return data.results ?? [];
  },
};

// Device management reuses the /api/mobile/v1/devices surface. The
// admin UI uses ListMyDevices against the caller identity; admin-as-
// other-user isn't implemented for v1.
export const devices = {
  listMine: async (): Promise<Device[]> => {
    const res = await fetch('/api/mobile/v1/devices', { headers: authHeaders() });
    const data = await handle<{ devices?: Device[] }>(res);
    return data.devices ?? [];
  },

  unregister: async (deviceId: string): Promise<void> => {
    const res = await fetch(`/api/mobile/v1/devices/${encodeURIComponent(deviceId)}`, {
      method: 'DELETE',
      headers: authHeaders(),
    });
    await handle<unknown>(res);
  },
};
