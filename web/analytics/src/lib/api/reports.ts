// Typed wrappers for ReportsService — CRUD + Preview + SendNow +
// ListRuns.

import { ApiError } from './analytics';
import type { Filters } from './types';

export type TemplateVariant =
  | 'TEMPLATE_VARIANT_UNSPECIFIED'
  | 'TEMPLATE_VARIANT_SUMMARY'
  | 'TEMPLATE_VARIANT_DETAILED';

export interface ScheduledReport {
  id?: string;
  server_id?: string;
  name?: string;
  cron?: string;
  timezone?: string;
  recipients?: string[];
  template_variant?: TemplateVariant;
  filters?: Filters;
  enabled?: boolean;
  created_at?: string;
  updated_at?: string;
  next_fire_at?: string;
}

export interface ListReportsResponse {
  reports?: ScheduledReport[];
}

export interface PreviewReportResponse {
  html?: string;
  subject?: string;
}

export interface SendNowResponse {
  run_id?: string;
}

export interface ReportRun {
  id?: string;
  report_id?: string;
  started_at?: string;
  finished_at?: string;
  status?: string;
  attempt?: number;
  error?: string;
  recipients?: string[];
}

export interface ListRunsResponse {
  runs?: ReportRun[];
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

export const reports = {
  list: async (server_id: string): Promise<ScheduledReport[]> => {
    const res = await fetch(`/api/v1/reports/${encodeURIComponent(server_id)}`, {
      headers: authHeaders(),
    });
    const data = await handle<ListReportsResponse>(res);
    return data.reports ?? [];
  },

  create: async (server_id: string, report: ScheduledReport): Promise<ScheduledReport> => {
    const res = await fetch(`/api/v1/reports/${encodeURIComponent(server_id)}`, {
      method: 'POST',
      headers: { ...authHeaders(), 'Content-Type': 'application/json' },
      body: JSON.stringify(report),
    });
    return handle<ScheduledReport>(res);
  },

  update: async (
    server_id: string,
    report_id: string,
    report: ScheduledReport
  ): Promise<ScheduledReport> => {
    const res = await fetch(
      `/api/v1/reports/${encodeURIComponent(server_id)}/${encodeURIComponent(report_id)}`,
      {
        method: 'PATCH',
        headers: { ...authHeaders(), 'Content-Type': 'application/json' },
        body: JSON.stringify(report),
      }
    );
    return handle<ScheduledReport>(res);
  },

  del: async (server_id: string, report_id: string): Promise<void> => {
    const res = await fetch(
      `/api/v1/reports/${encodeURIComponent(server_id)}/${encodeURIComponent(report_id)}`,
      { method: 'DELETE', headers: authHeaders() }
    );
    await handle<unknown>(res);
  },

  preview: async (server_id: string, report_id: string): Promise<PreviewReportResponse> => {
    const res = await fetch(
      `/api/v1/reports/${encodeURIComponent(server_id)}/${encodeURIComponent(report_id)}/preview`,
      {
        method: 'POST',
        headers: authHeaders(),
      }
    );
    return handle<PreviewReportResponse>(res);
  },

  sendNow: async (server_id: string, report_id: string): Promise<SendNowResponse> => {
    const res = await fetch(
      `/api/v1/reports/${encodeURIComponent(server_id)}/${encodeURIComponent(report_id)}/send-now`,
      {
        method: 'POST',
        headers: authHeaders(),
      }
    );
    return handle<SendNowResponse>(res);
  },

  listRuns: async (server_id: string, report_id: string, limit = 25): Promise<ReportRun[]> => {
    const qs = new URLSearchParams({ limit: String(limit) });
    const res = await fetch(
      `/api/v1/reports/${encodeURIComponent(server_id)}/${encodeURIComponent(report_id)}/runs?${qs.toString()}`,
      { headers: authHeaders() }
    );
    const data = await handle<ListRunsResponse>(res);
    return data.runs ?? [];
  },
};
