// Mirrors internal/domain/strategy.go shapes (JSON serialized).

export type BarPeriod = '1d' | '1w' | '1mo';
export type FillAt = 'next_open' | 'close';

export interface IndicatorSpec {
  alias: string;
  type: string;
  params: Record<string, number | string>;
}

// SizeSpec on the wire is either "all" or an object with one of these keys.
// Buys use pct_of_cash / fixed_cash / shares; sells use pct_of_position / fixed_cash / shares.
export type SizeSpec =
  | 'all'
  | { pct_of_cash: number }
  | { pct_of_position: number }
  | { shares: number }
  | { fixed_cash: number };

export interface ActionSpec {
  action: 'buy' | 'sell';
  size: SizeSpec;
}

// Visual form of a "when" expression. JSON form is one of the operator objects
// in internal/expr/parser.go; the editor edits this in-place and serializes.
export type CondExpr =
  | { kind: 'all'; children: CondExpr[] }
  | { kind: 'any'; children: CondExpr[] }
  | { kind: 'not'; child: CondExpr }
  | { kind: 'cmp'; op: 'gt' | 'lt' | 'gte' | 'lte' | 'eq'; lhs: Operand; rhs: Operand }
  | { kind: 'cross'; dir: 'cross_up' | 'cross_down'; a: Operand; b: Operand }
  | { kind: 'call'; op: 'has_position' };

// Operand of a leaf comparison: a named ref (indicator alias / OHLCV), a number,
// or a portfolio-state expression (unrealized P&L rate, days held, or price / entry price).
export type Operand =
  | { type: 'ref'; name: string }
  | { type: 'const'; value: number }
  | { type: 'call'; op: 'pnl_pct' | 'days_held' }
  | { type: 'ratio'; field: 'open' | 'high' | 'low' | 'close' };

export interface RuleSpec {
  name: string;
  when: CondExpr;
  then: ActionSpec;
}

export type StrategyMode = 'config' | 'script';

export interface StrategyBody {
  mode?: StrategyMode;   // absent = 'config', for backwards compatibility
  lang?: 'starlark';     // the script mode's language
  code?: string;         // the script mode's source
  indicators: IndicatorSpec[];
  rules: RuleSpec[];
  execution: { fill_at: FillAt };
}

export interface Strategy {
  id: number;
  version: number;
  name: string;
  description?: string;
  universe: string[];
  period: BarPeriod;
  body: StrategyBody | string; // server may return raw JSON string
  created_at?: string;
  updated_at?: string;
}

export interface IndicatorMeta {
  name: string;
  outputs: string[];
}

export interface Instrument {
  code: string;
  name: string;
  industry?: string;
  listed_date?: string | null;
  asset_type?: 'stock' | 'etf';
  status?: string;
}

export interface InstrumentList {
  items: Instrument[];
  total: number;
}

export interface BacktestJob {
  id: number;
  strategy_id: number;
  initial_capital: number;
  start_date: string;
  end_date: string;
  status: 'pending' | 'running' | 'succeed' | 'failed';
  started_at?: string;
  finished_at?: string;
  error_msg?: string;
}

export interface BacktestResult {
  total_return: number;
  annualized_return: number;
  max_drawdown: number;
  sharpe: number;
  win_rate: number;
  trade_count: number;
  rule_stats?: Record<string, { trigger_count: number; win_count: number; total_pnl: number }> | string;
}

export interface EquityPoint { date: string; total_value: number; drawdown: number }

export interface Trade {
  time: string;
  code: string;
  side: 'buy' | 'sell';
  price: number;
  qty: number;
  notional: number;
  commission: number;
  stamp_duty: number;
  realized_pnl?: number;
  triggered_rule?: string;
}

export interface Bar {
  code: string;
  period: BarPeriod;
  date: string;
  open: number;
  high: number;
  low: number;
  close: number;
  volume: number;
  amount: number;
  adj_factor: number;
}

export interface DataSyncJob {
  id: number;
  mode: 'incremental' | 'full';
  period: BarPeriod;
  status: 'pending' | 'running' | 'succeed' | 'failed';
  total: number; done: number; failed: number;
  started_at?: string; finished_at?: string;
}

export interface DataSyncFailure {
  id: number;
  task_id: number;
  code: string;
  reason: string;
  http_status: number;
  response: string;
  created_at?: string;
}

export interface DataCoverageSummary {
  id: number;
  period: BarPeriod;
  total_instruments: number;
  complete_count: number;
  stale_count: number;
  empty_count: number;
  latest_bar_date?: string | null;
  earliest_bar_date?: string | null;
  scanned_at?: string | null;
  scan_duration_ms: number;
  scan_status: 'idle' | 'running';
  last_error?: string;
  updated_at: string;
}

export interface DataCoverageResponse {
  items: DataCoverageSummary[];
  running: boolean;
}

export interface AutoSyncStatus {
  enabled: boolean;
  daily_time: string;
  next_run_at?: string | null;
  last_run_at?: string | null;
  last_task_id: number;
  last_error?: string;
}

// ============ Event pipelines ============
// Mirrors internal/domain/pipeline.go & event.go (JSON serialized).

export type PipelineStatus = 'draft' | 'enabled' | 'disabled';

// A node's config is typed JSON, interpreted by that node's Processor; the UI renders the form
// from config_schema.
export type NodeConfig = Record<string, unknown>;

export interface PipelineNode {
  id?: number;
  pipeline_id?: number;
  node_key: string;   // unique within the canvas; used as the React Flow node id
  type: string;       // keyword_filter / ai_analysis / ...
  name: string;
  config: NodeConfig;
  pos_x: number;
  pos_y: number;
}

// The edge's optional routing condition: one judgement about a field of the upstream node's
// output payload, and the edge activates only when it holds. An empty condition is
// unconditional. op is eq/ne/contains/gt/lt/exists, and exists needs no value.
export interface EdgeCondition {
  field: string;
  op: 'eq' | 'ne' | 'contains' | 'gt' | 'lt' | 'exists';
  value?: string | number;
}

export interface PipelineEdge {
  id?: number;
  pipeline_id?: number;
  from_node_key: string;
  to_node_key: string;
  condition?: EdgeCondition | null;
}

export interface Pipeline {
  id: number;
  name: string;
  description?: string;
  status: PipelineStatus;
  version: number;
  created_by?: string;
  created_at?: string;
  updated_at?: string;
  nodes: PipelineNode[];
  edges: PipelineEdge[];
  sources?: string[]; // the subscribed news sources
}

// PipelineInput is the create/update request body, without id, version or timestamps.
export interface PipelineInput {
  expected_version?: number;
  name: string;
  description: string;
  status: PipelineStatus;
  nodes: Array<{
    node_key: string;
    type: string;
    name: string;
    config: NodeConfig;
    pos_x: number;
    pos_y: number;
  }>;
  edges: Array<{ from_node_key: string; to_node_key: string; condition?: EdgeCondition | null }>;
  sources: string[]; // the subscribed news sources
}

// The subset of JSON Schema a node's ConfigSchema() returns.
export interface JSONSchemaProp {
  type?: 'string' | 'boolean' | 'integer' | 'number' | 'array' | 'object';
  title?: string;
  description?: string;
  default?: unknown;
  enum?: string[];
  // Friendly labels for the enum values, the same length and in the same order; without it the
  // raw enum values are shown.
  enumNames?: string[];
  // Dynamic enum source: the options are not baked into the schema but injected at runtime by
  // the UI under this key (e.g. "llm_providers" for the models configured under Settings). It
  // takes precedence over a static enum.
  x_enum_source?: string;
  // Conditional hiding: hide this field when a sibling field's current value falls in the given
  // set — the email recipient field, for instance, shows only when the channel is email and is
  // hidden for Feishu.
  x_hide_when?: Record<string, string[]>;
  items?: { type: string };
  // Sub-field schemas for a nested object, such as the AI analysis node's retention gate.
  properties?: Record<string, JSONSchemaProp>;
}

export interface NodeConfigSchema {
  type: 'object';
  required?: string[];
  properties: Record<string, JSONSchemaProp>;
}

// One dynamic enum option: either a bare string, where the value is also the label (a provider
// name, say), or an explicit {value, label} pair (the email channel shown as "Email (SMTP)").
export type DynamicEnumOption = string | { value: string; label: string };

export interface NodeTypeMeta {
  type: string;
  name: string;
  category: string;
  config_schema: NodeConfigSchema;
}

// ---- Dry run ----
export interface DryRunTrace {
  delivery_preview?: { channel: string; recipients: string[]; uses_default_recipients: boolean; title: string; body: string };
  node_key: string;
  node_type: string;
  action: string; // pass / drop
  latency_ms: number;
  error: string;
  output?: Record<string, unknown>;
}

export interface DryRunResult {
  status: 'passed' | 'dropped' | 'failed';
  dropped_at_node: string;
  failed_at_node: string;
  traces: DryRunTrace[];
  final_payload: Record<string, unknown>;
}

// ---- Events ----
export type EventStatus = 'processing' | 'passed' | 'dropped' | 'failed';

export interface EventTrace {
  id: number;
  event_id: number;
  node_key: string;
  node_type: string;
  action: string;
  latency_ms: number;
  error: string;
  created_at?: string;
}

export interface AIResult {
  id: number;
  event_id: number;
  node_key: string;
  provider: string;
  model: string;
  prompt_snapshot: string;
  output: unknown;
  tokens_prompt: number;
  tokens_completion: number;
  latency_ms: number;
  created_at?: string;
}

export interface PipelineEvent {
  id: number;
  event_uid: string;
  pipeline_id: number;
  source: string;
  raw_payload: unknown;
  final_payload: unknown;
  status: EventStatus;
  dropped_at_node: string;
  error: string;
  received_at?: string;
  finished_at?: string | null;
  traces?: EventTrace[];
  ai_results?: AIResult[];
}

export interface EventListResponse {
  total: number;
  items: PipelineEvent[];
}

// ---- Settings: LLM providers ----
export type LLMProviderType = 'anthropic' | 'openai';

// The redacted view returned to the UI, without the api_key.
export interface LLMProviderMasked {
  type: string;
  base_url: string;
  default_model: string;
  has_api_key: boolean;
  credential_revoked?: boolean;
  agent?: { enabled?: boolean; protocol?: string; context_window?: number; max_output_tokens?: number; reasoning_effort?: string };
}

// For writes: an empty api_key keeps the stored one.
export interface LLMProviderInput {
  type: string;
  base_url: string;
  api_key: string;
  default_model: string;
}

// ---- Settings: delivery channels (email / Feishu) ----
// The redacted email (SMTP) view, without the password.
export interface EmailSettingMasked {
  host: string;
  port: number;
  username: string;
  from: string;
  use_ssl: boolean;
  default_to: string[];
  has_password: boolean;
}

// For email writes: an empty password keeps the stored one.
export interface EmailSettingInput {
  host: string;
  port: number;
  username: string;
  password: string;
  from: string;
  use_ssl: boolean;
  default_to: string[];
}

// The redacted Feishu view, without the secret.
export interface FeishuSettingMasked {
  webhook_url: string;
  has_secret: boolean;
}

// For Feishu writes: an empty secret keeps the stored one.
export interface FeishuSettingInput {
  webhook_url: string;
  secret: string;
}

// ============ News ============
// Mirrors internal/domain/news.go（JSON serialized）。
// A source name is not a fixed enum: it is the Name() of a datasource.NewsSource registered on
// the backend, and the available list comes from GET /api/v1/news/sources.
export type NewsSourceName = string;

// Source name -> display name. The project ships no collection implementation, so this starts
// empty: add a line here once you plug in your own source. An unlisted source is shown by its
// raw name, since every call site falls back with `?? source`.
export const NEWS_SOURCE_LABEL: Record<string, string> = {};

export interface News {
  id: number;
  source: string;
  external_id: string;
  title: string;
  content: string;
  url: string;
  published_at: string;
  raw?: unknown;
  created_at?: string;
}

export interface NewsListResponse {
  total: number;
  items: News[];
}

// One news source's collection config, for writes.
export interface NewsSourceSetting {
  enabled: boolean;
  interval_seconds: number;
}

// A source's collection status, config and runtime state, as returned by /news/sources.
export interface NewsSourceStatus {
  source: string;
  enabled: boolean;
  interval_seconds: number;
  last_polled_at?: string | null;
  last_fetched: number;
  last_new: number;
  last_error?: string;
}

// The full /news/sources response: the collector's global config plus each source's status.
export interface NewsSourcesResponse {
  enabled: boolean;              // the collector's master switch
  base_interval_seconds: number; // the base poll tick
  sources: NewsSourceStatus[];
}
