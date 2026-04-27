// Typed fetch wrappers for AuthService admin RPCs — Users CRUD +
// per-server ACL grant/revoke/list. Used by the Phase-3 Admin UI.
//
// The gRPC→REST gateway serves these at /api/v1/auth/*. Shapes
// mirror pkg/apispec/v1/auth/auth.proto (snake_case because the
// gateway runs with UseProtoNames=true).

import { ApiError } from './analytics';

export interface User {
  uuid?: string;
  username?: string;
  email?: string;
  sys_admin?: boolean;
  created_at?: string;
  updated_at?: string;
}

export interface ListUsersResponse {
  status?: string;
  error?: string;
  users?: User[];
}

export type ServerAccessRole =
  | 'SERVER_ACCESS_ROLE_UNSPECIFIED'
  | 'SERVER_ACCESS_ROLE_VIEWER'
  | 'SERVER_ACCESS_ROLE_MANAGER';

export interface ServerAccessEntry {
  user_id: string;
  user_email?: string;
  server_id: string;
  role: ServerAccessRole;
  granted_at?: string;
  granted_by?: string;
}

export interface ListServerAccessResponse {
  status?: string;
  error?: string;
  entries?: ServerAccessEntry[];
}

const TOKEN_KEY = 'hula:token';

function authHeaders(): Record<string, string> {
  const headers: Record<string, string> = {};
  if (typeof localStorage !== 'undefined') {
    const t = localStorage.getItem(TOKEN_KEY);
    if (t) headers.Authorization = `Bearer ${t}`;
  }
  return headers;
}

export function setToken(token: string): void {
  if (typeof localStorage !== 'undefined') localStorage.setItem(TOKEN_KEY, token);
}

export function clearToken(): void {
  if (typeof localStorage !== 'undefined') localStorage.removeItem(TOKEN_KEY);
}

export function getToken(): string | null {
  if (typeof localStorage === 'undefined') return null;
  return localStorage.getItem(TOKEN_KEY);
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

// ----- Login (unauthenticated endpoints) -----

export interface AuthProviderInfo {
  name: string;
  type: string;
  display_name?: string;
  icon_url?: string;
  auth_url?: string;
}

export interface LoginAdminResponse {
  admintoken?: string;
  error?: string;
  totp_required?: boolean;
}

export interface LoginWithCodeResponse {
  token?: string;
  error?: string;
  provider?: string;
  tenantId?: string;
}

export const login = {
  providers: async (): Promise<AuthProviderInfo[]> => {
    const res = await fetch('/api/v1/auth/providers');
    const data = await handle<{ providers?: AuthProviderInfo[] }>(res);
    return data.providers ?? [];
  },

  admin: async (username: string, hash: string): Promise<LoginAdminResponse> => {
    const res = await fetch('/api/v1/auth/admin', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username, hash }),
    });
    return handle<LoginAdminResponse>(res);
  },

  withCode: async (
    code: string,
    onetimetoken: string,
    provider: string,
  ): Promise<LoginWithCodeResponse> => {
    const res = await fetch('/api/v1/auth/code', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ code, onetimetoken, provider }),
    });
    return handle<LoginWithCodeResponse>(res);
  },
};

// ----- Users CRUD -----

export const users = {
  list: async (): Promise<User[]> => {
    const res = await fetch('/api/v1/auth/users/list', {
      method: 'POST',
      headers: { ...authHeaders(), 'Content-Type': 'application/json' },
      body: JSON.stringify({ filter: '' }),
    });
    const data = await handle<ListUsersResponse>(res);
    return data.users ?? [];
  },

  create: async (u: { email: string; username?: string }): Promise<User> => {
    const res = await fetch('/api/v1/auth/users', {
      method: 'POST',
      headers: { ...authHeaders(), 'Content-Type': 'application/json' },
      body: JSON.stringify({ user: u }),
    });
    const data = await handle<{ user?: User }>(res);
    if (!data.user) throw new Error('create: empty response');
    return data.user;
  },

  patch: async (userid: string, patch: Partial<User>): Promise<User> => {
    const res = await fetch('/api/v1/auth/user', {
      method: 'PATCH',
      headers: { ...authHeaders(), 'Content-Type': 'application/json' },
      body: JSON.stringify({ identity: userid, user: patch }),
    });
    const data = await handle<{ user?: User }>(res);
    if (!data.user) throw new Error('patch: empty response');
    return data.user;
  },

  del: async (userid: string): Promise<void> => {
    const res = await fetch('/api/v1/auth/user/delete', {
      method: 'POST',
      headers: { ...authHeaders(), 'Content-Type': 'application/json' },
      body: JSON.stringify({ identity: userid }),
    });
    await handle<unknown>(res);
  },
};

// ----- Per-server ACL -----

export const access = {
  list: async (filter?: { user_id?: string; server_id?: string }): Promise<ServerAccessEntry[]> => {
    const qs = new URLSearchParams();
    if (filter?.user_id) qs.set('user_id', filter.user_id);
    if (filter?.server_id) qs.set('server_id', filter.server_id);
    const res = await fetch(`/api/v1/auth/access?${qs.toString()}`, {
      headers: authHeaders(),
    });
    const data = await handle<ListServerAccessResponse>(res);
    return data.entries ?? [];
  },

  grant: async (user_id: string, server_id: string, role: ServerAccessRole): Promise<void> => {
    const res = await fetch('/api/v1/auth/access', {
      method: 'POST',
      headers: { ...authHeaders(), 'Content-Type': 'application/json' },
      body: JSON.stringify({ user_id, server_id, role }),
    });
    await handle<unknown>(res);
  },

  revoke: async (user_id: string, server_id: string): Promise<void> => {
    const path = `/api/v1/auth/access/${encodeURIComponent(user_id)}/${encodeURIComponent(server_id)}`;
    const res = await fetch(path, {
      method: 'DELETE',
      headers: authHeaders(),
    });
    await handle<unknown>(res);
  },
};
