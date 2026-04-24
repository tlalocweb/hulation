// Typed wrappers for GoalsService. Goals are per-server; every call
// takes a server_id in the path.

import { ApiError } from './analytics';

export type GoalKind =
  | 'GOAL_KIND_UNSPECIFIED'
  | 'GOAL_KIND_URL_VISIT'
  | 'GOAL_KIND_EVENT'
  | 'GOAL_KIND_FORM'
  | 'GOAL_KIND_LANDER';

export interface Goal {
  id?: string;
  server_id?: string;
  name?: string;
  description?: string;
  kind?: GoalKind;
  rule_url_regex?: string;
  rule_event_code?: number;
  rule_form_id?: string;
  rule_lander_id?: string;
  enabled?: boolean;
  created_at?: string;
  updated_at?: string;
}

export interface ListGoalsResponse {
  goals?: Goal[];
}

export interface TestGoalResponse {
  would_fire?: number;
  scanned_events?: number;
}

function authHeaders(): Record<string, string> {
  const headers: Record<string, string> = {};
  if (typeof localStorage !== 'undefined') {
    const t = localStorage.getItem('hula:token');
    if (t) headers.Authorization = `Bearer ${t}`;
  }
  return headers;
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

export const goals = {
  list: async (server_id: string): Promise<Goal[]> => {
    const res = await fetch(`/api/v1/goals/${encodeURIComponent(server_id)}`, {
      headers: authHeaders(),
    });
    const data = await handle<ListGoalsResponse>(res);
    return data.goals ?? [];
  },

  create: async (server_id: string, goal: Goal): Promise<Goal> => {
    const res = await fetch(`/api/v1/goals/${encodeURIComponent(server_id)}`, {
      method: 'POST',
      headers: { ...authHeaders(), 'Content-Type': 'application/json' },
      body: JSON.stringify(goal),
    });
    return handle<Goal>(res);
  },

  update: async (server_id: string, goal_id: string, goal: Goal): Promise<Goal> => {
    const res = await fetch(
      `/api/v1/goals/${encodeURIComponent(server_id)}/${encodeURIComponent(goal_id)}`,
      {
        method: 'PATCH',
        headers: { ...authHeaders(), 'Content-Type': 'application/json' },
        body: JSON.stringify(goal),
      }
    );
    return handle<Goal>(res);
  },

  del: async (server_id: string, goal_id: string): Promise<void> => {
    const res = await fetch(
      `/api/v1/goals/${encodeURIComponent(server_id)}/${encodeURIComponent(goal_id)}`,
      { method: 'DELETE', headers: authHeaders() }
    );
    await handle<unknown>(res);
  },

  test: async (server_id: string, goal: Goal, days = 7): Promise<TestGoalResponse> => {
    const res = await fetch(`/api/v1/goals/${encodeURIComponent(server_id)}/test`, {
      method: 'POST',
      headers: { ...authHeaders(), 'Content-Type': 'application/json' },
      body: JSON.stringify({ ...goal, days }),
    });
    return handle<TestGoalResponse>(res);
  },
};
