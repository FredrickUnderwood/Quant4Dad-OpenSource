import assert from 'node:assert/strict';
import test from 'node:test';
import { archiveDraft, archiveUpdate, costInput, marketDraft, marketUpdate, secretInput } from '../src/settings/models.ts';
import type { ArchiveSettingsView, CostInput, MarketSettingsView } from '../src/settings/types.ts';

const market: MarketSettingsView = { provider: 'tushare', initial_years: 10, concurrency: 8, rate_limit_per_min: 300, include_etf: false, tushare: { has_token: true }, http: { base_url: 'https://example.invalid/data', has_token: true }, auto_sync: { enabled: false, daily_time: '03:00' } };
const archive: ArchiveSettingsView = { enabled: false, daily_time: '03:30', retention_days: 10, batch_size: 2000, batch_sleep_ms: 200, oss: { endpoint: 'oss-cn-test.aliyuncs.com', region: 'cn-test', bucket: 'synthetic-bucket', prefix: 'fixture/', has_access_key_id: true, has_access_key_secret: true } };

test('masked views become credential-free drafts and section writes never submit another section', () => {
  const input = marketUpdate(17, marketDraft(market));
  assert.equal(input.expected_revision, 17); assert.equal('archive' in input, false);
  assert.deepEqual(input.market?.tushare.token, { action: 'keep' });
  assert.deepEqual(input.market?.http.token, { action: 'keep' });
  assert.equal(JSON.stringify(input).includes('has_token'), false);
  const oss = archiveUpdate(18, archiveDraft(archive));
  assert.equal('market' in oss, false); assert.equal(JSON.stringify(oss).includes('has_access_key'), false);
  assert.deepEqual(oss.archive?.oss.access_key_secret, { action: 'keep' });
});
test('credential replacement is explicit and keep/clear discard stale typed values', () => {
  assert.deepEqual(secretInput({ action: 'keep', value: 'synthetic-key' }), { action: 'keep' });
  assert.deepEqual(secretInput({ action: 'clear', value: 'synthetic-key' }), { action: 'clear' });
  assert.throws(() => secretInput({ action: 'replace' }), /必须填写/);
  assert.throws(() => secretInput({ action: 'replace', value: '  ' }), /必须填写/);
  assert.deepEqual(secretInput({ action: 'replace', value: ' synthetic-key ' }), { action: 'replace', value: ' synthetic-key ' });
});
test('a dirty draft cannot mutate the saved view', () => {
  const draft = marketDraft(market); draft.auto_sync.enabled = true; draft.http.base_url = 'https://new.invalid';
  assert.equal(market.auto_sync.enabled, false); assert.equal(market.http.base_url, 'https://example.invalid/data');
});
test('manual automatic collection, endpoint credentials and out-of-range schedules are rejected', () => {
  assert.throws(() => marketUpdate(1, { ...marketDraft(market), provider: 'manual', auto_sync: { enabled: true, daily_time: '03:00' } }));
  for (const base_url of ['file:///tmp/data', 'https://user:password@example.invalid', 'https://example.invalid/?token=secret', 'not-url']) {
    assert.throws(() => marketUpdate(1, { ...marketDraft(market), provider: 'http', http: { base_url, token: { action: 'keep' } } }));
  }
  for (const change of [{ initial_years: 51 }, { concurrency: 33 }, { rate_limit_per_min: 60001 }, { concurrency: NaN }, { auto_sync: { enabled: false, daily_time: '24:00' } }]) assert.throws(() => marketUpdate(1, { ...marketDraft(market), ...change }));
  for (const change of [{ retention_days: 0 }, { retention_days: 36501 }, { batch_size: 2001 }, { batch_sleep_ms: -1 }, { batch_sleep_ms: 60001 }]) assert.throws(() => archiveUpdate(1, { ...archiveDraft(archive), ...change }));
});
test('OSS prefixes reject ambiguous object paths while allowing a trailing slash', () => {
  for (const prefix of ['a//', 'a/./', '../private', '/absolute', 'a\\b']) assert.throws(() => archiveUpdate(1, { ...archiveDraft(archive), oss: { ...archiveDraft(archive).oss, prefix } }));
  assert.equal(archiveUpdate(1, archiveDraft(archive)).archive?.oss.prefix, 'fixture/');
});
test('registered provider identities and inactive provider credentials remain independent', () => {
  const draft = marketDraft(market); draft.provider = 'my-installed-provider'; draft.http.token = { action: 'replace', value: 'custom-fixture-token' };
  const body = marketUpdate(3, draft);
  assert.equal(body.market?.provider, 'my-installed-provider');
  assert.deepEqual(body.market?.tushare.token, { action: 'keep' });
  assert.deepEqual(body.market?.http.token, { action: 'replace', value: 'custom-fixture-token' });
});
test('cost form emits only editable fields and rejects non-finite, negative and excessive rates', () => {
  const valid: CostInput = { name: ' Synthetic cost ', is_default: false, commission_rate: .0003, min_commission: 5, stamp_duty_rate: .0005, slippage_bps: 5 };
  assert.equal(costInput(valid).name, 'Synthetic cost');
  for (const change of [{ name: '' }, { name: 'x\ny' }, { min_commission: NaN }, { commission_rate: Infinity }, { stamp_duty_rate: -1 }, { slippage_bps: 10001 }, { commission_rate: 1.1 }]) assert.throws(() => costInput({ ...valid, ...change }));
  assert.equal(costInput({ ...valid, commission_rate: 0, min_commission: 0, stamp_duty_rate: 0, slippage_bps: 0 }).min_commission, 0);
  assert.equal('id' in costInput({ ...valid, id: 999 } as CostInput), false);
});
