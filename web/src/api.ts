import type { ArchiveStatus, CostInput, CostModel, DeploymentView, IntegrationsView, IntegrationUpdate } from './settings/types';
import type {
  Strategy, IndicatorMeta, StrategyBody, BarPeriod,
  BacktestJob, BacktestResult, EquityPoint, Trade, Bar,
  DataSyncJob, DataSyncFailure, Instrument, InstrumentList,
  DataCoverageResponse, AutoSyncStatus,
  Pipeline, PipelineInput, PipelineStatus, NodeTypeMeta, DryRunResult,
  PipelineEvent, EventListResponse, EventStatus,
  LLMProviderMasked, LLMProviderInput,
  EmailSettingMasked, EmailSettingInput, FeishuSettingMasked, FeishuSettingInput,
  News, NewsListResponse, NewsSourceSetting, NewsSourcesResponse,
} from './types';

// On a 401, go to the login page with the current path in ?from=, so a successful login returns
// here. The login page's own endpoints (/login and /auth/status) pass skipAuthRedirect
// explicitly, which is what keeps this from looping.
function redirectToLogin() {
  const here = window.location.pathname + window.location.search;
  if (window.location.pathname === '/login') return;
  const from = encodeURIComponent(here);
  window.location.replace(`/login?from=${from}`);
}

async function req<T>(url: string, init?: RequestInit & { skipAuthRedirect?: boolean }): Promise<T> {
  const { skipAuthRedirect, ...rest } = init || {};
  const r = await fetch(url, {
    ...rest,
    credentials: 'include',
    headers: { 'Content-Type': 'application/json', ...(rest.headers || {}) },
  });
  if (r.status === 401 && !skipAuthRedirect) {
    redirectToLogin();
    throw new Error('unauthorized');
  }
  if (!r.ok) {
    const body = await r.text();
    throw new Error(body || `${r.status} ${r.statusText}`);
  }
  if (r.status === 204) return undefined as unknown as T;
  return r.json() as Promise<T>;
}

export interface AuthStatus { authenticated: boolean; auth_required: boolean; }

export interface StrategyInput {
  expected_version?: number;
  name: string;
  description?: string;
  universe: string[];
  period: BarPeriod;
  body: StrategyBody;
}

export const api = {
  login:             (token: string) => req<{ ok: boolean }>('/api/v1/login', {
                       method: 'POST', body: JSON.stringify({ token }), skipAuthRedirect: true,
                     }),
  logout:            () => req<{ ok: boolean }>('/api/v1/logout', { method: 'POST', skipAuthRedirect: true }),
  authStatus:        () => req<AuthStatus>('/api/v1/auth/status', { skipAuthRedirect: true }),

  listStrategies:    () => req<Strategy[]>('/api/v1/strategies'),
  getStrategy:       (id: number) => req<Strategy>(`/api/v1/strategies/${id}`),
  createStrategy:    (s: StrategyInput) => req<Strategy>('/api/v1/strategies', { method: 'POST', body: JSON.stringify(s) }),
  updateStrategy:    (id: number, s: StrategyInput) => req<Strategy>(`/api/v1/strategies/${id}`, { method: 'PUT', body: JSON.stringify(s) }),
  deleteStrategy:    (id: number) => req<void>(`/api/v1/strategies/${id}`, { method: 'DELETE' }),

  listIndicators:    () => req<IndicatorMeta[]>('/api/v1/indicators'),

  searchInstruments: (keyword: string, size = 30) =>
                       req<InstrumentList>(`/api/v1/instruments?keyword=${encodeURIComponent(keyword)}&page=1&size=${size}`),
  getInstrument:     (code: string) => req<Instrument>(`/api/v1/instruments/${encodeURIComponent(code)}`),
  getInstrumentBars: (code: string, period: BarPeriod = '1d', start?: string, end?: string) => {
    const qs = new URLSearchParams({ period });
    if (start) qs.set('start', start);
    if (end) qs.set('end', end);
    return req<Bar[]>(`/api/v1/instruments/${encodeURIComponent(code)}/bars?${qs.toString()}`);
  },

  listBacktests:     (limit = 50) => req<BacktestJob[]>(`/api/v1/backtests?limit=${limit}`),
  getBacktest:       (id: number) => req<BacktestJob>(`/api/v1/backtests/${id}`),
  createBacktest:    (b: { strategy_id: number; initial_capital: number; start_date: string; end_date: string }) =>
                       req<BacktestJob>('/api/v1/backtests', { method: 'POST', body: JSON.stringify(b) }),
  getBacktestResult: (id: number) => req<BacktestResult>(`/api/v1/backtests/${id}/result`),
  getBacktestEquity: (id: number) => req<EquityPoint[]>(`/api/v1/backtests/${id}/equity`),
  getBacktestTrades: (id: number) => req<Trade[]>(`/api/v1/backtests/${id}/trades`),

  listDataSync:      (limit = 20) => req<DataSyncJob[]>(`/api/v1/datasync?limit=${limit}`),
  startDataSync:     (b: { mode: 'incremental' | 'full'; period: BarPeriod; start_date?: string; end_date?: string; codes?: string[] }) =>
                       req<DataSyncJob>('/api/v1/datasync', { method: 'POST', body: JSON.stringify(b) }),
  getDataSyncFailures: (id: number, limit = 5) =>
                       req<DataSyncFailure[]>(`/api/v1/datasync/${id}/failures?limit=${limit}`),

  listCoverage:      () => req<DataCoverageResponse>('/api/v1/datasync/coverage'),
  triggerCoverageScan: () => req<{ running: boolean }>('/api/v1/datasync/coverage/scan', { method: 'POST' }),

  getAutoSyncStatus: () => req<AutoSyncStatus>('/api/v1/datasync/auto'),

  // ============ Event pipelines ============
  listPipelines:     () => req<Pipeline[]>('/api/v1/pipelines'),
  getPipeline:       (id: number) => req<Pipeline>(`/api/v1/pipelines/${id}`),
  createPipeline:    (p: PipelineInput) => req<Pipeline>('/api/v1/pipelines', { method: 'POST', body: JSON.stringify(p) }),
  updatePipeline:    (id: number, p: PipelineInput) => req<Pipeline>(`/api/v1/pipelines/${id}`, { method: 'PUT', body: JSON.stringify(p) }),
  setPipelineStatus: (id: number, status: PipelineStatus, expected_version?: number) =>
                       req<{ ok: boolean }>(`/api/v1/pipelines/${id}/status`, { method: 'PATCH', body: JSON.stringify({ status, expected_version }) }),
  deletePipeline:    (id: number) => req<void>(`/api/v1/pipelines/${id}`, { method: 'DELETE' }),
  previewPipeline:   (pipeline: PipelineInput, sample: Record<string, unknown>) => req<DryRunResult>('/api/v1/pipelines/preview', { method: 'POST', body: JSON.stringify({ pipeline, sample_event: sample }) }),
  dryRunPipeline:    (id: number, sample: Record<string, unknown>) =>
                       req<DryRunResult>(`/api/v1/pipelines/${id}/dry-run`, { method: 'POST', body: JSON.stringify({ sample_event: sample }) }),

  nodeTypes:         () => req<NodeTypeMeta[]>('/api/v1/pipeline/node-types'),

  ingestEvent:       (pipelineId: number, payload: Record<string, unknown>, source = 'manual') =>
                       req<{ event_id: number; event_uid: string; status: EventStatus; dropped_at_node: string; final_payload: unknown }>(
                         `/api/v1/events/ingest/${pipelineId}?source=${encodeURIComponent(source)}`,
                         { method: 'POST', body: JSON.stringify(payload) },
                       ),
  listEvents:        (q: { pipeline_id?: number; status?: string; start?: string; end?: string; limit?: number; offset?: number } = {}) => {
                       const qs = new URLSearchParams();
                       if (q.pipeline_id) qs.set('pipeline_id', String(q.pipeline_id));
                       if (q.status) qs.set('status', q.status);
                       if (q.start) qs.set('start', q.start);
                       if (q.end) qs.set('end', q.end);
                       qs.set('limit', String(q.limit ?? 50));
                       qs.set('offset', String(q.offset ?? 0));
                       return req<EventListResponse>(`/api/v1/events?${qs.toString()}`);
                     },
  getEvent:          (id: number) => req<PipelineEvent>(`/api/v1/events/${id}`),

  // Export events as CSV or Excel. The backend returns a file stream directly, so this fetches a
  // blob to trigger the browser download; req cannot be reused, since it parses JSON.
  exportEvents:      async (q: { pipeline_id?: number; status?: string; start?: string; end?: string; format: 'csv' | 'excel' }) => {
                       const qs = new URLSearchParams();
                       if (q.pipeline_id) qs.set('pipeline_id', String(q.pipeline_id));
                       if (q.status) qs.set('status', q.status);
                       if (q.start) qs.set('start', q.start);
                       if (q.end) qs.set('end', q.end);
                       qs.set('format', q.format);
                       const r = await fetch(`/api/v1/events/export?${qs.toString()}`, { credentials: 'include' });
                       if (r.status === 401) { redirectToLogin(); throw new Error('unauthorized'); }
                       if (!r.ok) { throw new Error((await r.text()) || `${r.status} ${r.statusText}`); }
                       const blob = await r.blob();
                       let filename = q.format === 'excel' ? 'events.xlsx' : 'events.csv';
                       const m = /filename="([^"]+)"/.exec(r.headers.get('Content-Disposition') || '');
                       if (m) filename = m[1];
                       const href = URL.createObjectURL(blob);
                       const a = document.createElement('a');
                       a.href = href;
                       a.download = filename;
                       document.body.appendChild(a);
                       a.click();
                       a.remove();
                       URL.revokeObjectURL(href);
                     },

  // ============ News ============
  listNews:          (q: { source?: string; keyword?: string; limit?: number; offset?: number } = {}) => {
                       const qs = new URLSearchParams();
                       if (q.source) qs.set('source', q.source);
                       if (q.keyword) qs.set('keyword', q.keyword);
                       qs.set('limit', String(q.limit ?? 50));
                       qs.set('offset', String(q.offset ?? 0));
                       return req<NewsListResponse>(`/api/v1/news?${qs.toString()}`);
                     },
  getNews:           (id: number) => req<News>(`/api/v1/news/${id}`),
  getNewsSources:    () => req<NewsSourcesResponse>('/api/v1/news/sources'),
  setNewsSources:    (body: { enabled: boolean; base_interval_seconds: number; sources: Record<string, NewsSourceSetting> }) =>
                       req<NewsSourcesResponse>('/api/v1/news/sources', {
                         method: 'PUT', body: JSON.stringify(body),
                       }),
  pollNewsSource:    (source: string) =>
                       req<{ source: string; new: number }>(`/api/v1/news/sources/${encodeURIComponent(source)}/poll`, { method: 'POST' }),

  // Integration reads are masked; writes submit only the section being edited.
  getIntegrations: () => req<IntegrationsView>('/api/v1/settings/integrations', { cache: 'no-store' }),
  setIntegrations: (body: IntegrationUpdate) => req<IntegrationsView>('/api/v1/settings/integrations', { method: 'PUT', body: JSON.stringify(body) }),
  testMarket: (revision: number) => req<{ ok: boolean; message: string }>('/api/v1/settings/integrations/test-market', { method: 'POST', body: JSON.stringify({ expected_revision: revision }) }),
  testOSS: (revision: number) => req<{ ok: boolean; message: string }>('/api/v1/settings/integrations/test-oss', { method: 'POST', body: JSON.stringify({ expected_revision: revision }) }),
  getDeployment: () => req<DeploymentView>('/api/v1/settings/deployment', { cache: 'no-store' }),
  getArchiveStatus: () => req<ArchiveStatus>('/api/v1/archive/status', { cache: 'no-store' }),
  runArchive: () => req<{ days: number; events: number }>('/api/v1/archive/run', { method: 'POST' }),
  listCosts: () => req<CostModel[]>('/api/v1/costs'),
  createCost: (body: CostInput) => req<CostModel>('/api/v1/costs', { method: 'POST', body: JSON.stringify(body) }),
  updateCost: (id: number, body: CostInput) => req<CostModel>(`/api/v1/costs/${id}`, { method: 'PUT', body: JSON.stringify(body) }),
  deleteCost: (id: number) => req<void>(`/api/v1/costs/${id}`, { method: 'DELETE' }),
  defaultCost: (id: number) => req<void>(`/api/v1/costs/${id}/default`, { method: 'POST' }),

  // ============ Settings: LLM providers ============
  getLLMProviders:   () => req<{ revision: string; providers: Record<string, LLMProviderMasked> }>('/api/v1/settings/llm-providers'),
  setLLMProviders:   (providers: Record<string, LLMProviderInput>) =>
                       req<{ providers: Record<string, LLMProviderMasked> }>('/api/v1/settings/llm-providers', {
                         method: 'PUT', body: JSON.stringify({ providers }),
                       }),

  // ============ Settings: delivery channels (email / Feishu) ============
  getNotifyEmail:    () => req<{ email: EmailSettingMasked }>('/api/v1/settings/notify-email'),
  setNotifyEmail:    (email: EmailSettingInput) =>
                       req<{ email: EmailSettingMasked }>('/api/v1/settings/notify-email', {
                         method: 'PUT', body: JSON.stringify(email),
                       }),
  getNotifyFeishu:   () => req<{ feishu: FeishuSettingMasked }>('/api/v1/settings/notify-feishu'),
  setNotifyFeishu:   (feishu: FeishuSettingInput) =>
                       req<{ feishu: FeishuSettingMasked }>('/api/v1/settings/notify-feishu', {
                         method: 'PUT', body: JSON.stringify(feishu),
                       }),
};
