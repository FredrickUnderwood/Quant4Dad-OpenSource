import type { ArchiveSettingsInput, ArchiveSettingsView, CostInput, IntegrationUpdate, MarketSettingsInput, MarketSettingsView, SecretUpdate } from './types.ts';

export const keepSecret = (): SecretUpdate => ({ action: 'keep' });
export function secretInput(value: SecretUpdate): SecretUpdate {
  if (value.action === 'keep' || value.action === 'clear') return { action: value.action };
  if (value.action !== 'replace' || !value.value?.trim()) throw new Error('替换凭据时必须填写新值。');
  return { action: 'replace', value: value.value };
}
export function marketDraft(value: MarketSettingsView): MarketSettingsInput {
  return { provider: value.provider, initial_years: value.initial_years, concurrency: value.concurrency,
    rate_limit_per_min: value.rate_limit_per_min, include_etf: value.include_etf,
    auto_sync: { ...value.auto_sync }, tushare: { token: keepSecret() }, http: { base_url: value.http.base_url, token: keepSecret() } };
}
export function archiveDraft(value: ArchiveSettingsView): ArchiveSettingsInput {
  return { enabled: value.enabled, daily_time: value.daily_time, retention_days: value.retention_days,
    batch_size: value.batch_size, batch_sleep_ms: value.batch_sleep_ms,
    oss: { endpoint: value.oss.endpoint, region: value.oss.region, bucket: value.oss.bucket, prefix: value.oss.prefix,
      access_key_id: keepSecret(), access_key_secret: keepSecret() } };
}
function integer(value: number, minimum: number, label: string, maximum: number): void {
  if (!Number.isSafeInteger(value) || value < minimum || value > maximum) throw new Error(`${label}必须是 ${minimum}–${maximum} 之间的整数。`);
}
function dailyTime(value: string): void { if (!/^([01]\d|2[0-3]):[0-5]\d$/.test(value)) throw new Error('执行时刻必须为 HH:MM。'); }
export function marketUpdate(revision: number, value: MarketSettingsInput): IntegrationUpdate {
  integer(value.initial_years, 1, '首次回补年数', 50); integer(value.concurrency, 1, '采集并发', 32); integer(value.rate_limit_per_min, 0, '每分钟请求上限', 60000); dailyTime(value.auto_sync.daily_time);
  if (value.provider === 'manual' && value.auto_sync.enabled) throw new Error('手动 CSV 模式不能启用自动采集。');
  const baseURL = value.http.base_url.trim();
  if (value.provider === 'http') {
    let url: URL;
    try { url = new URL(baseURL); } catch { throw new Error('请填写有效的 HTTP 接口地址。'); }
    if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password || url.search || url.hash) throw new Error('接口地址须为 HTTP(S)，不能包含账号、密码、查询参数或片段。');
  }
  return { expected_revision: revision, market: { ...value, auto_sync: { ...value.auto_sync }, tushare: { token: secretInput(value.tushare.token) }, http: { base_url: baseURL, token: secretInput(value.http.token) } } };
}
export function archiveUpdate(revision: number, value: ArchiveSettingsInput): IntegrationUpdate {
  integer(value.retention_days, 1, '本地保留天数', 36500); integer(value.batch_size, 1, '处理批量', 2000); integer(value.batch_sleep_ms, 0, '批次间隔', 60000); dailyTime(value.daily_time);
  const oss = { endpoint: value.oss.endpoint.trim(), region: value.oss.region.trim(), bucket: value.oss.bucket.trim(), prefix: value.oss.prefix.trim(), access_key_id: secretInput(value.oss.access_key_id), access_key_secret: secretInput(value.oss.access_key_secret) };
  const segments = oss.prefix.replace(/\/$/, '').split('/');
  if (new TextEncoder().encode(oss.prefix).length > 256 || oss.prefix.startsWith('/') || oss.prefix.includes('..') || /[\r\n\u0000\\]/.test(oss.prefix) || (oss.prefix !== '' && segments.some(part => part === '' || part === '.'))) throw new Error('对象前缀不能包含重复斜杠、点路径段、控制字符或反斜杠；可使用 quant4dad/。');
  if (value.enabled && (!oss.endpoint || !oss.region || !oss.bucket)) throw new Error('启用归档前请填写 OSS Endpoint、Region 和 Bucket。');
  return { expected_revision: revision, archive: { ...value, oss } };
}
export function costInput(value: CostInput): CostInput {
  const name = value.name.trim();
  if (!name || [...name].length > 64 || /[\u0000-\u001f\u007f]/.test(name)) throw new Error('费用模型名称需为 1–64 个字符，不能包含控制字符。');
  for (const n of [value.commission_rate, value.min_commission, value.stamp_duty_rate, value.slippage_bps]) {
    if (!Number.isFinite(n) || n < 0) throw new Error('费用参数必须为有限的非负数。');
  }
  if (value.commission_rate > 1 || value.stamp_duty_rate > 1 || value.slippage_bps > 10000) throw new Error('费率不能超过 100%，滑点不能超过 10000 bps。');
  return { name, is_default: value.is_default, commission_rate: value.commission_rate, min_commission: value.min_commission, stamp_duty_rate: value.stamp_duty_rate, slippage_bps: value.slippage_bps };
}
