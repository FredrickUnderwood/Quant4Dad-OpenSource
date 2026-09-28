import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtemp, realpath, readdir, readFile, chmod, stat, statfs, symlink, rm, writeFile } from 'node:fs/promises'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import { inspect } from 'node:util'
import { deriveDshConfiguration, keylessMarker } from '../../../../src/bootstrap/derive.mjs'
import { BootstrapSnapshotStore } from '../../../../src/bootstrap/snapshot-store.mjs'
import { DshBootstrapApplier } from '../../../../src/bootstrap/applier.mjs'
import { BootstrapSync } from '../../../../src/bootstrap/sync.mjs'
import { clone, revise, host, send } from './helpers.mjs'

async function directory(t) {
  const root = await realpath(await mkdtemp(join(tmpdir(), 'q4d-bootstrap-apply-')))
  t.after(() => rm(root, { recursive: true, force: true }))
  return root
}
async function factory({ settingsPath }) {
  const config = JSON.parse(await readFile(settingsPath))['llm-pi-ai']
  return { async describe() { return Object.entries(config.providers).flatMap(([provider, p]) => p.models.map(m =>
    ({ provider, model: m.id, context_window: m.contextWindow, max_output_tokens: m.maxTokens }))) },
  async *stream() { yield { type: 'finish', reason: { kind: 'stop' } } }, async close() {} }
}
async function setup(t, createGeneration = factory) {
  const root = await directory(t), server = await host(t)
  let sync
  const applier = new DshBootstrapApplier({ store: new BootstrapSnapshotStore({ root, requireTmpfs: false }),
    createGeneration, currentStatus: () => sync.status() })
  sync = new BootstrapSync({ client: server.client, authorize: () => true, applier })
  t.after(() => sync.stop())
  return { root, server, sync, applier }
}

test('TEST-BOOTSTRAP-APPLY-01 derived identities are stable, collision-separated and independent of secrets', () => {
  const value = clone()
  value.providers.push({ ...structuredClone(value.providers[0]), id: 'fixture.other' },
    { ...structuredClone(value.providers[0]), id: 'fixture_other' })
  const first = deriveDshConfiguration(value)
  assert.equal(new Set(Object.values(first.routes)).size, 3)
  for (const name of Object.values(first.routes)) assert.match(name, /^q4d-[a-z0-9-]+$/)
  for (const name of Object.keys(first.credentials.refs)) assert.match(name, /^Q4D_LLM_[A-F0-9]{32}$/)
  value.providers[0].api_key = 'replacement'
  assert.deepEqual(deriveDshConfiguration(revise(value)).routes, first.routes)
  assert.ok(!JSON.stringify(first.settings).includes('fixture-model-private-key'))
  assert.ok(!JSON.stringify(first.settings).includes(value.mcp.runtime_token))
  const special = clone(); special.providers[0].id = '__proto__'
  assert.equal(Object.keys(deriveDshConfiguration(special).routes).length, 1)
})

test('TEST-BOOTSTRAP-APPLY-02 explicit defaults, context/output and reasoning survive derivation; unsupported secrets/efforts fail', () => {
  for (const type of ['openai', 'anthropic']) {
    const value = clone(); const p = value.providers[0]
    p.type = type; p.agent.protocol = type === 'openai' ? 'openai-completions' : 'anthropic-messages'
    p.base_url = ''; p.api_key = ''; p.agent.reasoning_effort = 'high'
    const derived = deriveDshConfiguration(value), profile = Object.values(derived.settings['llm-pi-ai'].providers)[0]
    assert.equal(profile.baseURL, type === 'openai' ? 'https://api.openai.com/v1' : 'https://api.anthropic.com')
    assert.equal(profile.models[0].contextWindow, p.agent.context_window)
    assert.equal(profile.models[0].maxTokens, p.agent.max_output_tokens)
    assert.equal(profile.reasoning, 'high')
    assert.deepEqual(profile.retryPolicy, { mode: 'normal', maxRetries: 0 })
    assert.equal(derived.credentials.refs[profile.apiKeyEnv], keylessMarker)
  }
  for (const change of [p => { p.api_key = 'not header safe' }, p => { p.api_key = '密钥' }, p => { p.agent.reasoning_effort = 'unsupported' }]) {
    const value = clone(); change(value.providers[0])
    assert.throws(() => deriveDshConfiguration(value), { message: 'agent_bootstrap_apply_failed' })
  }
})

test('TEST-BOOTSTRAP-APPLY-03 one immutable file pair has exact permissions and excludes control/MCP secrets', async t => {
  const root = await directory(t), store = new BootstrapSnapshotStore({ root, requireTmpfs: false })
  const value = clone(), first = await store.write(deriveDshConfiguration(value))
  assert.deepEqual(await readdir(root), [first.directory.split('/').at(-1)])
  assert.equal((await stat(first.directory)).mode & 0o777, 0o700)
  assert.equal((await stat(first.settingsPath)).mode & 0o777, 0o640)
  assert.equal((await stat(first.credentialsPath)).mode & 0o777, 0o600)
  const settings = await readFile(first.settingsPath, 'utf8'), credentials = await readFile(first.credentialsPath, 'utf8')
  assert.ok(!settings.includes(value.providers[0].api_key))
  assert.ok(credentials.includes(value.providers[0].api_key))
  assert.ok(!credentials.includes(value.mcp.runtime_token))
  const second = await store.write(deriveDshConfiguration({ ...revise(), providers: [] }))
  assert.deepEqual(JSON.parse(await readFile(second.credentialsPath)).refs, {})
  assert.equal(await readFile(first.credentialsPath, 'utf8'), credentials)
  assert.ok(!inspect(store, { showHidden: true }).includes(root))
  await first.remove(); await first.remove(); await second.remove()
  assert.deepEqual(await readdir(root), [])
})

test('TEST-BOOTSTRAP-APPLY-04 unsafe root, non-tmpfs, pre-abort and excessive files fail before publishing', async t => {
  const root = await directory(t), value = deriveDshConfiguration(clone())
  const store = new BootstrapSnapshotStore({ root, requireTmpfs: false })
  await writeFile(join(root, 'unrelated'), 'keep')
  const controller = new AbortController(); controller.abort(new Error('secret'))
  await assert.rejects(store.write(value, { signal: controller.signal }), { message: 'agent_bootstrap_storage_failed' })
  await assert.rejects(store.write({ settings: {}, credentials: { secret: 'x'.repeat(1024 * 1024) } }), { message: 'agent_bootstrap_storage_failed' })
  await chmod(root, 0o777)
  await assert.rejects(store.write(value), { message: 'agent_bootstrap_storage_failed' })
  await chmod(root, 0o700)
  const parent = await directory(t), alias = join(parent, 'alias'); await symlink(root, alias)
  await assert.rejects(new BootstrapSnapshotStore({ root: alias, requireTmpfs: false }).write(value), { message: 'agent_bootstrap_storage_failed' })
  if (process.platform !== 'linux' || (await statfs(root)).type !== 0x01021994) {
    await assert.rejects(new BootstrapSnapshotStore({ root }).write(value), { message: 'agent_bootstrap_storage_failed' })
  }
  assert.deepEqual(await readdir(root), ['unrelated'])
  assert.equal(await readFile(join(root, 'unrelated'), 'utf8'), 'keep')
})

test('TEST-BOOTSTRAP-APPLY-05 application gate stays closed until real metadata acknowledgement; 304 preserves generation', async t => {
  const entered = Promise.withResolvers(), resume = Promise.withResolvers()
  let builds = 0
  const { sync, server, root } = await setup(t, async files => { builds++; entered.resolve(); await resume.promise; return factory(files) })
  const pending = sync.refresh(); await entered.promise
  assert.deepEqual(sync.status(), { phase: 'applying', configuration_current: false, configuration_applied: false, error: 'agent_configuration_unavailable' })
  assert.throws(() => sync.readConfiguration(), { message: 'agent_configuration_unavailable' })
  resume.resolve(); await pending
  assert.equal(sync.status().configuration_applied, true)
  const generation = await readdir(root)
  server.setHandler((_req, res) => send(res, clone(), 304)); await sync.refresh()
  assert.equal(builds, 1); assert.deepEqual(await readdir(root), generation)
  await sync.stop(); assert.deepEqual(await readdir(root), [])
})

test('TEST-BOOTSTRAP-APPLY-06 partial or rejected DSH application never leaves last-good authority or files', async t => {
  let broken = false, closed = 0
  const { sync, server, root } = await setup(t, async files => {
    const backend = await factory(files)
    return { ...backend, describe: async () => broken ? [] : backend.describe(), close: async () => { closed++ } }
  })
  await sync.refresh(); broken = true
  server.setHandler((_req, res) => send(res, revise()))
  await assert.rejects(sync.refresh(), { message: 'agent_bootstrap_apply_failed' })
  assert.equal(sync.status().configuration_applied, false)
  assert.deepEqual(await readdir(root), [])
  assert.equal(closed, 2)
})

test('TEST-BOOTSTRAP-APPLY-07 stop during preparation discards a late successful backend', async t => {
  const entered = Promise.withResolvers(), resume = Promise.withResolvers()
  let closed = 0
  const { sync, root } = await setup(t, async files => {
    entered.resolve(); await resume.promise
    return { ...await factory(files), close: async () => { closed++ } }
  })
  const pending = assert.rejects(sync.refresh(), { message: 'agent_bootstrap_stopped' })
  await entered.promise
  const stopping = sync.stop()
  assert.equal(sync.status().phase, 'stopped')
  resume.resolve(); await pending; await stopping
  assert.deepEqual(await readdir(root), []); assert.equal(closed, 1)
})

test('TEST-BOOTSTRAP-APPLY-08 every model step requires live authority and errors never expose provider diagnostics', async t => {
  let calls = 0, fail = false
  const { sync, applier } = await setup(t, async files => ({ ...await factory(files), async *stream() {
    calls++
    yield fail ? { type: 'finish', reason: { kind: 'error', failure: { message: 'sensitive-provider-key-and-body' } } }
      : { type: 'finish', reason: { kind: 'stop' } }
  } }))
  await sync.refresh()
  const request = { provider: 'fixture', model: 'fixture-model', messages: [] }
  for (const authorize of [undefined, () => false, async () => true]) {
    await assert.rejects(async () => { for await (const chunk of applier.stream(request, authorize)) void chunk }, { message: 'agent_capability_rejected' })
  }
  await assert.rejects(async () => { for await (const chunk of applier.stream({ ...request, maxTokens: 513 }, () => true)) void chunk }, { message: 'agent_capability_rejected' })
  assert.equal(calls, 0)
  fail = true
  await assert.rejects(async () => { for await (const chunk of applier.stream(request, () => true)) void chunk }, { message: 'agent_model_request_failed' })
  assert.equal(calls, 1)
  await sync.stop()
  await assert.rejects(async () => { for await (const chunk of applier.stream(request, () => true)) void chunk }, { message: 'agent_configuration_unavailable' })
  assert.equal(calls, 1)
})

test('TEST-BOOTSTRAP-APPLY-09 a revoked policy suppresses the next chunk and closes its model iterator', async t => {
  let allowed = true, closed = false
  const { sync, applier } = await setup(t, async files => ({ ...await factory(files), async *stream() {
    try { yield { type: 'fixture', text: 'first' }; yield { type: 'fixture', text: 'must be suppressed' } }
    finally { closed = true }
  } }))
  await sync.refresh()
  const iterator = applier.stream({ provider: 'fixture', model: 'fixture-model', messages: [] }, () => allowed)
  assert.equal((await iterator.next()).value.text, 'first')
  allowed = false
  await assert.rejects(iterator.next(), { message: 'agent_capability_rejected' })
  assert.equal(closed, true)
})

test('TEST-BOOTSTRAP-CONTEXT structured overflow survives sanitization in both delivery styles', async t => {
  let thrown = false
  const { sync, applier } = await setup(t, async files => ({ ...await factory(files), async *stream() {
    if (thrown) throw Object.assign(new Error('private-key-marker'), { code: 'CONTEXT_WINDOW_EXCEEDED' })
    yield { type: 'finish', reason: { kind: 'error', failure: { code: 'CONTEXT_WINDOW_EXCEEDED', message: 'private-key-marker', extra: 'secret' } } }
  } }))
  await sync.refresh()
  for (const mode of [false, true]) {
    thrown = mode
    const chunks = await Array.fromAsync(applier.stream({ provider: 'fixture', model: 'fixture-model', messages: [] }, () => true))
    assert.deepEqual(chunks, [{ type: 'finish', reason: { kind: 'error', failure: { code: 'CONTEXT_WINDOW_EXCEEDED', message: 'agent_context_window_exceeded' } } }])
  }
})
