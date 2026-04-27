// Typed fetch wrapper for the Phase-1 analytics RPCs.
//
// Design notes:
//  - Every analytics call takes a server_id + Filters. The serialiser
//    flattens Filters into `filters.<field>` query params — matches
//    what grpc-gateway expects.
//  - Auth: the bearer token comes from localStorage (set by the
//    login page in stage 2.8). Until then, hula's admin JWT is
//    pasted manually via setToken() during dev.
//  - Errors: non-2xx responses throw ApiError with the parsed body.

import type {
  DevicesResponse,
  Filters,
  RealtimeResponse,
  SummaryResponse,
  TableResponse,
  TimeseriesResponse,
  VisitorResponse,
  VisitorsResponse,
} from './types';

const TOKEN_KEY = 'hula:token';

export class ApiError extends Error {
  constructor(public status: number, public body: unknown) {
    super(`analytics API ${status}`);
  }
}

export function setToken(token: string): void {
  if (typeof localStorage === 'undefined') return;
  localStorage.setItem(TOKEN_KEY, token);
}

export function clearToken(): void {
  if (typeof localStorage === 'undefined') return;
  localStorage.removeItem(TOKEN_KEY);
}

function getToken(): string | null {
  if (typeof localStorage === 'undefined') return null;
  return localStorage.getItem(TOKEN_KEY);
}

// encodeFilters flattens a Filters object into query params, skipping
// empty values. server_ids becomes repeated `filters.server_ids=…`.
function encodeFilters(f: Filters | undefined, out: URLSearchParams): void {
  if (!f) return;
  for (const [k, v] of Object.entries(f)) {
    if (v === undefined || v === null || v === '') continue;
    if (Array.isArray(v)) {
      for (const item of v) {
        if (item !== '') out.append(`filters.${k}`, String(item));
      }
      continue;
    }
    out.set(`filters.${k}`, String(v));
  }
}

interface RequestOpts {
  serverId: string;
  filters?: Filters;
  extras?: Record<string, string | number | undefined>;
  signal?: AbortSignal;
}

async function get<T>(path: string, opts: RequestOpts): Promise<T> {
  const qs = new URLSearchParams();
  qs.set('server_id', opts.serverId);
  encodeFilters(opts.filters, qs);
  if (opts.extras) {
    for (const [k, v] of Object.entries(opts.extras)) {
      if (v !== undefined && v !== '') qs.set(k, String(v));
    }
  }
  const url = `/api/v1/analytics${path}?${qs.toString()}`;
  const headers: Record<string, string> = {};
  const tok = getToken();
  if (tok) headers.Authorization = `Bearer ${tok}`;
  const res = await fetch(url, { headers, signal: opts.signal });
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

// Typed endpoint wrappers — one per RPC. Each takes the canonical
// (serverId, filters, extras) shape and returns the proto response.

export const analytics = {
  summary: (opts: RequestOpts): Promise<SummaryResponse> => get('/summary', opts),

  timeseries: (opts: RequestOpts): Promise<TimeseriesResponse> =>
    get('/timeseries', opts),

  pages: (opts: RequestOpts & { limit?: number; offset?: number }): Promise<TableResponse> =>
    get('/pages', { ...opts, extras: { ...opts.extras, limit: opts.limit, offset: opts.offset } }),

  sources: (
    opts: RequestOpts & { groupBy?: string; limit?: number; offset?: number }
  ): Promise<TableResponse> =>
    get('/sources', {
      ...opts,
      extras: {
        ...opts.extras,
        group_by: opts.groupBy,
        limit: opts.limit,
        offset: opts.offset,
      },
    }),

  geography: (opts: RequestOpts): Promise<TableResponse> => get('/geography', opts),

  devices: (opts: RequestOpts): Promise<DevicesResponse> => get('/devices', opts),

  events: (opts: RequestOpts): Promise<TableResponse> => get('/events', opts),

  formsReport: (opts: RequestOpts): Promise<TableResponse> => get('/forms', opts),

  visitors: (
    opts: RequestOpts & { limit?: number; offset?: number }
  ): Promise<VisitorsResponse> =>
    get('/visitors', {
      ...opts,
      extras: { ...opts.extras, limit: opts.limit, offset: opts.offset },
    }),

  visitor: (opts: RequestOpts & { visitorId: string }): Promise<VisitorResponse> =>
    get(`/visitor/${encodeURIComponent(opts.visitorId)}`, opts),

  realtime: (opts: RequestOpts): Promise<RealtimeResponse> => get('/realtime', opts),

  // CSV export — returns the raw response so the caller can save a
  // Blob. Every table endpoint honours ?format=csv.
  csv: async (
    endpoint:
      | 'pages'
      | 'sources'
      | 'geography'
      | 'devices'
      | 'events'
      | 'forms'
      | 'visitors',
    opts: RequestOpts
  ): Promise<Blob> => {
    const qs = new URLSearchParams();
    qs.set('server_id', opts.serverId);
    qs.set('format', 'csv');
    encodeFilters(opts.filters, qs);
    const headers: Record<string, string> = {};
    const tok = getToken();
    if (tok) headers.Authorization = `Bearer ${tok}`;
    const res = await fetch(`/api/v1/analytics/${endpoint}?${qs.toString()}`, {
      headers,
      signal: opts.signal,
    });
    if (!res.ok) throw new ApiError(res.status, await res.text());
    return await res.blob();
  },
};
