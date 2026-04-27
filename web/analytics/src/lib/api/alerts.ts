// Typed fetch wrappers for AlertsService. Shapes mirror
// pkg/apispec/v1/alerts/alerts.proto. REST gateway runs with
// UseProtoNames=true so the JSON shape is snake_case.

import { ApiError } from './analytics';

export type AlertKind =
  | 'ALERT_KIND_UNSPECIFIED'
  | 'ALERT_KIND_GOAL_COUNT_ABOVE'
  | 'ALERT_KIND_PAGE_TRAFFIC_DELTA'
  | 'ALERT_KIND_FORM_SUBMISSION_RATE'
  | 'ALERT_KIND_BAD_ACTOR_RATE'
  | 'ALERT_KIND_BUILD_FAILED';

export type DeliveryStatus =
  | 'DELIVERY_STATUS_UNSPECIFIED'
  | 'DELIVERY_STATUS_SUCCESS'
  | 'DELIVERY_STATUS_RETRYING'
  | 'DELIVERY_STATUS_FAILED'
  | 'DELIVERY_STATUS_MAILER_UNCONFIGURED';

export interface Alert {
  id?: string;
  server_id?: string;
  name?: string;
  description?: string;
  kind?: AlertKind;
  threshold?: number;
  window_minutes?: number;
  target_goal_id?: string;
  target_path?: string;
  target_form_id?: string;
  recipients?: string[];
  cooldown_minutes?: number;
  enabled?: boolean;
  created_at?: string;
  updated_at?: string;
  last_fired_at?: string;
}

export interface AlertEvent {
  id?: string;
  alert_id?: string;
  fired_at?: string;
  observed_value?: number;
  threshold?: number;
  recipients?: string[];
  delivery_status?: DeliveryStatus;
  error?: string;
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

export const alerts = {
  list: async (serverId: string): Promise<Alert[]> => {
    const res = await fetch(`/api/v1/alerts/${encodeURIComponent(serverId)}`, {
      headers: authHeaders(),
    });
    const data = await handle<{ alerts?: Alert[] }>(res);
    return data.alerts ?? [];
  },

  create: async (serverId: string, alert: Alert): Promise<Alert> => {
    const res = await fetch(`/api/v1/alerts/${encodeURIComponent(serverId)}`, {
      method: 'POST',
      headers: { ...authHeaders(), 'Content-Type': 'application/json' },
      body: JSON.stringify({ alert }),
    });
    return handle<Alert>(res);
  },

  update: async (serverId: string, alertId: string, alert: Alert): Promise<Alert> => {
    const path = `/api/v1/alerts/${encodeURIComponent(serverId)}/${encodeURIComponent(alertId)}`;
    const res = await fetch(path, {
      method: 'PATCH',
      headers: { ...authHeaders(), 'Content-Type': 'application/json' },
      body: JSON.stringify({ alert }),
    });
    return handle<Alert>(res);
  },

  del: async (serverId: string, alertId: string): Promise<void> => {
    const path = `/api/v1/alerts/${encodeURIComponent(serverId)}/${encodeURIComponent(alertId)}`;
    const res = await fetch(path, { method: 'DELETE', headers: authHeaders() });
    await handle<unknown>(res);
  },

  listEvents: async (serverId: string, alertId: string, limit = 25): Promise<AlertEvent[]> => {
    const path = `/api/v1/alerts/${encodeURIComponent(serverId)}/${encodeURIComponent(alertId)}/events?limit=${limit}`;
    const res = await fetch(path, { headers: authHeaders() });
    const data = await handle<{ events?: AlertEvent[] }>(res);
    return data.events ?? [];
  },
};
