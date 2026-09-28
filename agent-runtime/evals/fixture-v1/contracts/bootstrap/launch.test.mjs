import test from 'node:test'
import assert from 'node:assert/strict'
import { chmod, rm, symlink, writeFile, readFile, readdir } from 'node:fs/promises'
import { createHash } from 'node:crypto'
import { join } from 'node:path'
import { loadLaunchConfig, loadInputMeter } from '../../../../src/runtime/launch-config.mjs'
import { startRuntimeServer } from '../../../../src/runtime/server.mjs'
import { measureModelInput } from '../../../../src/runtime/input-meter.ts'
import { launchConfig } from '../../helpers/launch-config.mjs'

async function fixture(t) {
  const f = await launchConfig()
  t.after(() => rm(f.directory, { recursive: true, force: true }))
  return f
}
const invalid = { message: 'agent_runtime_configuration_invalid' }

test('TEST-RUNTIME-LAUNCH-01 strict config, file secrets, private paths and explicit production tmpfs', async t => {
  const f = await fixture(t), original = structuredClone(f.config)
  const loaded = await loadLaunchConfig(f.file, f.env)
  assert.equal(loaded.controlToken, 'fixture-control-token-0123456789-abcdef')
  assert.equal(loaded.bridgeToken, 'fixture-bridge-token-0123456789-abcdef')
  for (const mutate of [c => { c.extra = true }, c => { c.listen.host = '0.0.0.0' },
    c => { c.listen.port = 65536 }, c => { c.bootstrapURL += '?key=private-marker' },
    c => { c.profile.revision += '\n' }, c => { c.manifest.dsh_version = 'next' },
    c => { c.profile.toolCatalogRevision = '' }, c => { c.snapshotDirectory = c.stateDirectory },
    c => { c.stateDirectory += '/.' }, c => { c.mode = 'production'; c.listen.port = 9999 }]) {
    const copy = structuredClone(original); mutate(copy); await f.save(copy)
    await assert.rejects(loadLaunchConfig(f.file, f.env), invalid)
  }
  await f.save()
  await assert.rejects(loadLaunchConfig(f.file, { ...f.env, Q4D_AGENT_CONTROL_TOKEN: '' }), invalid)
  await assert.rejects(loadLaunchConfig(f.file, { ...f.env, Q4D_AGENT_BRIDGE_TOKEN_FILE: f.env.Q4D_AGENT_CONTROL_TOKEN_FILE }), invalid)
  const text = await readFile(f.file, 'utf8')
  await writeFile(f.file, text.replace('"schemaVersion":1', '"schemaVersion":1,"schemaVersion":1'))
  await assert.rejects(loadLaunchConfig(f.file, f.env), invalid)
  await f.save(); await chmod(f.env.Q4D_AGENT_CONTROL_TOKEN_FILE, 0o644)
  await assert.rejects(loadLaunchConfig(f.file, f.env), invalid)
  await chmod(f.env.Q4D_AGENT_CONTROL_TOKEN_FILE, 0o600)
  await symlink(f.file, join(f.directory, 'alias.json'))
  await assert.rejects(loadLaunchConfig(join(f.directory, 'alias.json'), f.env), invalid)
})

test('TEST-RUNTIME-LAUNCH-02 meter must match checked bytes and contract; errors disclose no source', async t => {
  const f = await fixture(t)
  assert.equal(typeof await loadInputMeter(f.config.inputMeter), 'function')
  for (const source of ['throw new Error("private-meter-marker")', 'export const contract="other"',
    'export const contract="q4d-input-meter-v1"; export const measureInput=100', 'import "./ambient.mjs"']) {
    await writeFile(f.config.inputMeter.bundlePath, source)
    await assert.rejects(loadInputMeter(f.config.inputMeter), invalid)
    await assert.rejects(loadInputMeter({ ...f.config.inputMeter,
      sha256: 'sha256:' + createHash('sha256').update(source).digest('hex') }), invalid)
  }
})

test('TEST-RUNTIME-LAUNCH-03 measurement gets detached full history/tools and current real model route', () => {
  const provider = { id: 'configured-route', default_model: 'model', api_key: 'private-key-marker',
    agent: { protocol: 'openai-completions', context_window: 8192, max_output_tokens: 256, reasoning_effort: 'low' } }
  const request = { provider: 'q4d-session-private', model: 'model', system: '中文 🐉', signal: new AbortController().signal,
    messages: [{ role: 'user', content: [{ type: 'text', text: 'history' }] }], tools: [{ name: 'tool', parameters: { type: 'object' } }] }
  const before = JSON.stringify(request)
  let seen
  assert.equal(measureModelInput((input, context) => { seen = { input, context }; return 42 }, request, provider, 'current-revision'), 42)
  assert.equal(seen.input.provider, 'configured-route')
  assert.equal(seen.input.signal, undefined)
  assert.deepEqual(seen.input.messages, request.messages)
  assert.deepEqual(seen.input.tools, request.tools)
  assert.equal(seen.context.modelConfigRevision, 'current-revision')
  assert.equal(seen.context.reasoningEffort, 'low')
  assert.ok(!JSON.stringify(seen).includes('private-key-marker'))
  assert.throws(() => { seen.input.messages[0].content[0].text = 'mutated' }, TypeError)
  assert.equal(JSON.stringify(request), before)
  for (const result of [0, -1, 1.5, NaN, Infinity, 100_000_001, '100', undefined, Promise.resolve(1), Promise.reject(new Error('private-marker'))]) {
    assert.throws(() => measureModelInput(() => result, request, provider, 'rev'), { message: 'agent_input_measurement_unavailable' })
  }
  assert.throws(() => measureModelInput(() => { throw new Error('private-meter-marker') }, request, provider, 'rev'),
    { message: 'agent_input_measurement_unavailable' })
})

test('TEST-RUNTIME-LAUNCH-04 single writer lock, private listener and graceful release permit restart', async t => {
  const f = await fixture(t)
  let starts = 0, closes = 0
  const factory = async options => {
    starts++; assert.equal(typeof options.measureInput, 'function')
    assert.equal(options.authorize, undefined); assert.equal(options.modelReady, undefined)
    return { handler: (_req, res) => res.end('fixture'), close: async () => { closes++ } }
  }
  const host = await startRuntimeServer(f.file, factory, f.env)
  t.after(() => host.close())
  assert.equal(await (await fetch(`http://127.0.0.1:${host.port}`)).text(), 'fixture')
  await assert.rejects(startRuntimeServer(f.file, factory, f.env), { message: 'agent_runtime_startup_failed' })
  assert.equal(starts, 1)
  await Promise.all([host.close(), host.close()]); assert.equal(closes, 1)
  assert.ok(!(await readdir(f.config.stateDirectory)).includes('.runtime-lock'))
  const second = await startRuntimeServer(f.file, factory, f.env); await second.close()
  assert.equal(starts, 2); assert.equal(closes, 2)
})

test('TEST-RUNTIME-LAUNCH-05 startup errors release owned lock; failed shutdown retains it', async t => {
  const f = await fixture(t)
  await assert.rejects(startRuntimeServer(f.file, async () => { throw new Error('private-startup-marker') }, f.env),
    { message: 'agent_runtime_startup_failed' })
  assert.ok(!(await readdir(f.config.stateDirectory)).includes('.runtime-lock'))
  const host = await startRuntimeServer(f.file, async () => ({ handler: (_req, res) => res.end(),
    close: async () => { throw new Error('private-shutdown-marker') } }), f.env)
  await assert.rejects(host.close(), { message: 'agent_runtime_shutdown_failed' })
  assert.ok((await readdir(f.config.stateDirectory)).includes('.runtime-lock'))
})
