// 后端管理 API 类型定义与请求封装。

export interface Totals {
  requests: number;
  errors: number;
  input_tokens: number;
  cache_read_tokens: number;
  cache_creation_tokens: number;
  output_tokens: number;
  cost: number;
}

export interface GroupAgg {
  key: string;
  requests: number;
  input_tokens: number;
  cache_read_tokens: number;
  cache_creation_tokens: number;
  output_tokens: number;
  cost: number;
  errors: number;
}

export interface Point {
  bucket: number;
  requests: number;
  input_tokens: number;
  cache_read_tokens: number;
  cache_creation_tokens: number;
  output_tokens: number;
  cost: number;
}

export interface TestResult {
  ok: boolean;
  latency_ms: number;
  errors?: string[];
  tested_at: number;
}

export interface Provider {
  id: string;
  name: string;
  anthropic_url: string;
  openai_url: string;
  timeout_sec: number;
  priority: number;
  enabled: boolean;
  note: string;
  has_key: boolean;
  fetched_models: string[];
  fetched_at: number;
  last_test?: TestResult | null;
  created_at: number;
}

export interface Route {
  alias: string;
  provider_id: string;
  upstream_model: string;
  enabled: boolean;
}

export interface Target {
  provider_id: string;
  model: string;
}

export interface Settings {
  port: number;
  default_provider_id: string;
  retention_days: number;
  inject_openai_usage: boolean;
  unload_on_minimise: boolean;
  lightweight: {
    enabled: boolean;
    targets: Target[];
    aliases: string[];
    heuristic: { enabled: boolean; max_body_bytes: number; require_no_tools: boolean };
  };
  subagent: { enabled: boolean; marker: string; target: Target };
  failover: { enabled: boolean; mode: 'manual' | 'same_name'; targets: Target[]; on_5xx: boolean; max_attempts: number };
  inject: { enabled: boolean; text: string };
  archive: { enabled: boolean; keep_per_session: number; max_snapshot_mb: number; global_cap_gb: number };
}

export interface Tier {
  enabled: boolean;
  start: string;
  end: string;
  mode: 'absolute' | 'multiplier';
  rate?: number;
  input?: number;
  output?: number;
  cache_read?: number;
  cache_creation?: number;
}

export interface Price {
  model: string;
  input: number;
  output: number;
  cache_read: number;
  cache_creation: number;
  peak?: Tier | null;
  valley?: Tier | null;
}

export interface RequestRow {
  id: number;
  ts: number;
  provider_id: string;
  provider_name: string;
  model: string;
  upstream_model: string;
  channel: string;
  format: string;
  stream: boolean;
  status: number;
  ok: boolean;
  input_tokens: number;
  cache_read_tokens: number;
  cache_creation_tokens: number;
  output_tokens: number;
  cost: number;
  duration_ms: number;
  ttfb_ms: number;
  failover: string;
  err: string;
  project: string;
  session_key: string;
}

export interface SessionRow {
  key: string;
  project: string;
  title: string;
  last_model: string;
  msg_count: number;
  size_bytes: number;
  updated_at: number;
}

export interface SnapshotRow {
  id: number;
  session_key: string;
  seq: number;
  ts: number;
  path: string;
  size_bytes: number;
  model: string;
  msg_count: number;
  project: string;
}

export interface SnapshotContent {
  project: string;
  model: string;
  captured_at: number;
  msg_count: number;
  system?: unknown;
  messages: ContentBlock[];
}

export interface ContentBlock {
  role?: string;
  content?: unknown;
  type?: string;
  text?: string;
  [k: string]: unknown;
}

// API 基址: 浏览器模式为同源(''), Wails 桌面模式由启动时注入绝对地址。
let apiBase = '';

export function setApiBase(base: string): void {
  apiBase = (base || '').replace(/\/+$/, '');
}

export function getApiBase(): string {
  return apiBase;
}

async function handle<T>(res: Response): Promise<T> {
  if (!res.ok) {
    let msg = `HTTP ${res.status}`;
    try {
      const j = (await res.json()) as { error?: string };
      if (j.error) msg = j.error;
    } catch { /* 保留默认消息 */ }
    throw new Error(msg);
  }
  return (await res.json()) as T;
}

export function get<T>(path: string): Promise<T> {
  return fetch(apiBase + path).then((r) => handle<T>(r));
}

export function send<T>(method: string, path: string, body?: unknown): Promise<T> {
  return fetch(apiBase + path, {
    method,
    headers: { 'content-type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  }).then((r) => handle<T>(r));
}

export const api = {
  version: () => get<{ version: string }>('/admin/version'),
  connection: () => get<{ base_url: string; router_key: string; port: number }>('/admin/connection'),
  rotateKey: () => send<{ router_key: string }>('POST', '/admin/routerkey/rotate'),
  overview: (range: string) => get<{ totals: Totals; by_provider: GroupAgg[]; by_model: GroupAgg[]; by_channel: GroupAgg[]; stats_dropped: number }>(`/admin/overview?range=${range}`),
  series: (range: string, bucket: string) => get<Point[]>(`/admin/series?range=${range}&bucket=${bucket}`),
  requests: (q: string) => get<RequestRow[]>(`/admin/requests?${q}`),

  providers: () => get<Provider[]>('/admin/providers'),
  createProvider: (p: Partial<Provider> & { api_key?: string }) => send('POST', '/admin/providers', p),
  updateProvider: (id: string, p: Partial<Provider> & { api_key?: string }) => send('PUT', `/admin/providers/${id}`, p),
  deleteProvider: (id: string) => send('DELETE', `/admin/providers/${id}`),
  fetchModels: (id: string) => send<{ models: string[]; added: number; errors: string[] }>('POST', `/admin/providers/${id}/fetch_models`),
  setProviderModels: (id: string, models: string[]) => send('PUT', `/admin/providers/${id}/models`, { models }),
  testProvider: (id: string) => send<{ ok: boolean; latency_ms: number; errors: string[] }>('POST', `/admin/providers/${id}/test`),
  testAllProviders: () => send<{ id: string; name: string; ok: boolean; latency_ms: number; errors: string[] }[]>('POST', '/admin/providers/test_all'),

  routes: () => get<Route[]>('/admin/routes'),
  putRoutes: (routes: Route[]) => send('PUT', '/admin/routes', routes),

  settings: () => get<Settings>('/admin/settings'),
  putSettings: (s: Settings) => send('PUT', '/admin/settings', s),

  pricing: () => get<Price[]>('/admin/pricing'),
  upsertPrice: (p: Price) => send('POST', '/admin/pricing', p),
  deletePrice: (model: string) => send('DELETE', `/admin/pricing/${encodeURIComponent(model)}`),
  importPricing: (json: string) => send<{ imported: number }>('POST', '/admin/pricing/import', { json }),

  sessions: () => get<SessionRow[]>('/admin/sessions'),
  snapshots: (key: string) => get<SnapshotRow[]>(`/admin/sessions/${encodeURIComponent(key)}/snapshots`),
  snapshotContent: (id: number) => get<SnapshotContent>(`/admin/snapshots/${id}`),
  deleteSession: (key: string) => send('DELETE', `/admin/sessions/${encodeURIComponent(key)}`),
};
