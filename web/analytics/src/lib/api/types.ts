// Proto-generated shape mirrors, kept in sync with
// pkg/apispec/v1/analytics/analytics.proto. Hand-written for Phase 2.1;
// stage 2.8 revisits whether a proper protoc-gen-typescript pipeline
// is worth the toolchain complexity.
//
// Field names are snake_case because the hula gateway is configured
// with UseProtoNames=true — the JSON the browser sees matches proto
// verbatim.

export interface Filters {
  server_ids?: string[];
  from?: string; // RFC 3339
  to?: string;
  granularity?: 'hour' | 'day' | 'week';
  compare?: 'previous' | 'previous_year' | '';
  country?: string;
  device?: 'mobile' | 'tablet' | 'desktop' | string;
  source?: string;
  path?: string;
  event_code?: string;
  goal?: string;
  browser?: string;
  os?: string;
  channel?: 'direct' | 'search' | 'social' | 'referral' | 'email' | string;
  utm_source?: string;
  utm_medium?: string;
  utm_campaign?: string;
  region?: string;
  city?: string;
}

export interface SummaryResponse {
  visitors: number;
  pageviews: number;
  bounce_rate: number;
  avg_session_duration_seconds: number;
  visitors_delta_pct: number;
  pageviews_delta_pct: number;
  bounce_rate_delta_pct: number;
  avg_session_duration_delta_pct: number;
  sparkline: number[];
}

export interface TimeseriesBucket {
  ts: string;
  visitors: number;
  pageviews: number;
}

export interface TimeseriesResponse {
  buckets: TimeseriesBucket[];
}

export interface TableRow {
  key: string;
  visitors: number;
  pageviews: number;
  bounce_rate: number;
  // Pages
  unique_pageviews?: number;
  avg_time_on_page_seconds?: number;
  entrances?: number;
  exits?: number;
  // Sources
  pages_per_visit?: number;
  goal_conv_rate?: number;
  // Geography
  percent?: number;
  // Events
  count?: number;
  unique_visitors?: number;
  first_seen?: string;
  last_seen?: string;
  // FormsReport
  submits?: number;
  conversion_rate?: number;
  avg_time_to_submit_seconds?: number;
}

export interface TableResponse {
  rows: TableRow[];
}

export interface DevicesResponse {
  device_category: TableRow[];
  browser: TableRow[];
  os: TableRow[];
}

export interface VisitorSummary {
  visitor_id: string;
  email?: string;
  first_seen: string;
  last_seen: string;
  sessions: number;
  pageviews: number;
  events: number;
  top_country: string;
  top_device: string;
  top_asn?: string;
  top_isp?: string;
}

export interface VisitorsResponse {
  visitors: VisitorSummary[];
  total: number;
}

export interface VisitorEvent {
  ts: string;
  event_code: string;
  url: string;
  referrer: string;
  country: string;
  device: string;
  ip: string;
  asn?: string;
  isp?: string;
}

export interface VisitorIP {
  ip: string;
  asn?: string;
  isp?: string;
  org?: string;
  country_code?: string;
}

export interface VisitorResponse {
  visitor: VisitorSummary;
  timeline: VisitorEvent[];
  ips?: string[];
  cookies?: string[];
  aliases?: string[];
  visitor_ips?: VisitorIP[];
}

export interface RealtimeResponse {
  active_visitors_5m: number;
  recent: VisitorEvent[];
  top_pages: TableRow[];
  top_sources: TableRow[];
}
