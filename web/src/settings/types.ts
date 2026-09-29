export type SecretAction = 'keep' | 'replace' | 'clear';
export interface SecretUpdate { action: SecretAction; value?: string; }
export interface MarketSettingsView {
  provider: string;
  initial_years: number;
  concurrency: number;
  rate_limit_per_min: number;
  include_etf: boolean;
  tushare: { has_token: boolean };
  http: { base_url: string; has_token: boolean };
  auto_sync: { enabled: boolean; daily_time: string };
}
export interface MarketSettingsInput extends Omit<MarketSettingsView, 'tushare' | 'http'> {
  tushare: { token: SecretUpdate };
  http: { base_url: string; token: SecretUpdate };
}
export interface ArchiveSettingsView {
  enabled: boolean;
  daily_time: string;
  retention_days: number;
  batch_size: number;
  batch_sleep_ms: number;
  oss: { endpoint: string; region: string; bucket: string; prefix: string; has_access_key_id: boolean; has_access_key_secret: boolean };
}
export interface ArchiveSettingsInput extends Omit<ArchiveSettingsView, 'oss'> {
  oss: { endpoint: string; region: string; bucket: string; prefix: string; access_key_id: SecretUpdate; access_key_secret: SecretUpdate };
}
export interface IntegrationsView { revision: number; registered_providers: string[]; market: MarketSettingsView; archive: ArchiveSettingsView; background_enabled: boolean; }
export type IntegrationUpdate = { expected_revision: number } & ({ market: MarketSettingsInput; archive?: never } | { archive: ArchiveSettingsInput; market?: never });
export interface ArchiveStatus {
  configured: boolean; running: boolean; enabled: boolean; daily_time: string; retention_days: number;
  next_run_at?: string | null; last_run_at?: string | null; last_days: number; last_events: number; last_error?: string;
}
export interface DeploymentView {
  storage_backend: string; timezone: string; agent_enabled: boolean; mcp_enabled: boolean;
  authentication_enabled: boolean; background_enabled: boolean; backtest_workers: number;
  items: { key: string; label: string; value: string; change_hint: string }[];
}
export interface CostInput { name: string; is_default: boolean; commission_rate: number; min_commission: number; stamp_duty_rate: number; slippage_bps: number; }
export interface CostModel extends CostInput { id: number; created_at?: string; updated_at?: string; }
