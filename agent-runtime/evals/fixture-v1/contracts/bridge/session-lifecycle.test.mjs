import assert from 'node:assert/strict'
import { mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'
import { SessionBindings, sessionCreated, sessionProvisionHash } from '../../../../src/persistence/session-bindings.mjs'
import { SessionLifecycle, sessionPreset } from '../../../../src/bridge/session-lifecycle.mjs'
import { DurableLog } from '../../../../src/persistence/durable-log.mjs'
import { sessionRequest } from '../../../../fixtures/session-config.mjs'

const provenance = { bridge_protocol: 1, adapter_version: '0.0.0-t00', backend: 'cordis', dsh_version: '0.1.2-alpha.5',
  session_format: 0, event_journal_format: 1, session_binding_format: 1 }
function setup(t) {
  const directory = mkdtempSync(join(tmpdir(), 'q4d-session-binding-'))
  const path = join(directory, 'sessions.jsonl')
  const bindings = new SessionBindings(path)
  t.after(() => { bindings.close(); rmSync(directory, { recursive: true, force: true }) })
  const stored = new Map()
  const live = new Map()
  const calls = []
  const backend = {
    resolve(request, creating) { calls.push(['resolve', creating]); return { model: request.model } },
    readHeader: async id => structuredClone(stored.get(id)),
    liveHeader: id => live.get(id),
    async materialize(binding) {
      const header = { id: binding.request.q4d_session_id, agentPreset: sessionPreset(binding) }
      stored.set(header.id, header); live.set(header.id, header); calls.push(['create', header.id])
    },
    async resume(binding) { const id = binding.request.q4d_session_id; live.set(id, stored.get(id)); calls.push(['resume', id]) },
    isBusy: () => false,
    async unload(id) { live.delete(id); calls.push(['unload', id]) },
  }
  const lifecycle = new SessionLifecycle({ bindings, backend, provenance })
  return { directory, path, bindings, stored, live, calls, backend, lifecycle }
}

test('TEST-SESSION-01 binding validates the hash domain, immutable identity and provenance through restart', t => {
  const { path, bindings } = setup(t)
  const request = sessionRequest()
  const reordered = Object.fromEntries(Object.entries(request).reverse())
  assert.equal(sessionProvisionHash(reordered), request.provision_request_hash)
  assert.equal(sessionProvisionHash(sessionRequest('another-session')), request.provision_request_hash)
  const prepared = bindings.prepare(request, provenance)
  assert.equal(prepared.durable, false)
  const committed = bindings.materialized(request.q4d_session_id)
  bindings.materialized(request.q4d_session_id)
  assert.equal(readFileSync(path, 'utf8').trim().split('\n').length, 2)
  for (const field of ['provider', 'model', 'profile', 'profile_revision', 'model_config_revision']) {
    const changed = sessionRequest('session-1', { [field]: field === 'profile_revision' ? 'sha256:' + 'f'.repeat(64) : 'changed' })
    assert.throws(() => bindings.prepare(changed, provenance), /agent_session_conflict/)
  }
  assert.throws(() => bindings.prepare({ ...request, run_capability: 'secret' }, provenance), /agent_invalid_request/)
  assert.throws(() => bindings.prepare({ ...request, model: 'changed' }, provenance), /agent_session_conflict/)
  bindings.close()
  const restored = new SessionBindings(path)
  t.after(() => restored.close())
  assert.deepEqual(restored.prepare(reordered, { ...provenance, adapter_version: 'new-version' }), committed)
  assert.deepEqual(sessionCreated(restored.get('session-1')), sessionCreated(committed))
})

test('TEST-SESSION-02 complete invalid binding records fail closed, including forged provenance and duplicate intent', t => {
  for (const mutate of [
    binding => ({ ...binding, runtime_provenance: { ...provenance, run_capability: 'secret' } }),
    binding => ({ ...binding, request: { ...binding.request, model: 'tampered' } }),
    binding => binding,
  ]) {
    const { path, bindings } = setup(t)
    const request = sessionRequest()
    bindings.prepare(request, provenance)
    bindings.close()
    const log = new DurableLog(path)
    log.append({ kind: 'intent', binding: mutate({ request, runtime_provenance: provenance }) })
    log.close()
    assert.throws(() => new SessionBindings(path), /agent_session_index_corrupt/)
  }
})

test('TEST-SESSION-03 concurrent provision is serialized with close and shares one durable creation', async t => {
  const { lifecycle, backend, live, stored, calls, bindings } = setup(t)
  const entered = Promise.withResolvers()
  const release = Promise.withResolvers()
  const materialize = backend.materialize
  backend.materialize = async binding => { entered.resolve(); await release.promise; return materialize(binding) }
  const first = lifecycle.provision(sessionRequest())
  await entered.promise
  assert.equal(bindings.get('session-1').durable, false)
  const repeated = lifecycle.provision(sessionRequest())
  const close = lifecycle.close('session-1')
  release.resolve()
  assert.deepEqual(await repeated, await first)
  assert.deepEqual(await close, { session_id: 'session-1', dsh_session_id: 'session-1', durable: true, loaded: false })
  assert.equal(calls.filter(([type]) => type === 'create').length, 1)
  assert.equal(live.size, 0)
  assert.equal(stored.size, 1)
})

test('TEST-SESSION-04 orphan and missing committed storage are never adopted or recreated', async t => {
  const { lifecycle, stored, bindings, calls } = setup(t)
  stored.set('session-1', { id: 'session-1', agentPreset: 'unowned' })
  await assert.rejects(lifecycle.provision(sessionRequest()), /agent_session_unbound/)
  assert.equal(bindings.get('session-1'), undefined)
  stored.clear()
  await lifecycle.provision(sessionRequest())
  await lifecycle.close('session-1')
  stored.clear()
  await assert.rejects(lifecycle.provision(sessionRequest()), /agent_session_storage_missing/)
  await assert.rejects(lifecycle.withActive('session-1', () => {}, () => {}), /agent_session_storage_missing/)
  await assert.rejects(lifecycle.close('session-1'), /agent_session_storage_missing/)
  assert.equal(calls.filter(([type]) => type === 'create').length, 1)
})

test('TEST-SESSION-05 authorization precedes resume; creation retries stay cold, old provenance stays immutable', async t => {
  const { lifecycle, backend, live, calls } = setup(t)
  const response = await lifecycle.provision(sessionRequest())
  await lifecycle.close('session-1')
  backend.resolve = () => { throw new Error('agent_configuration_unavailable') }
  assert.deepEqual(await lifecycle.provision(sessionRequest()), response, 'creation ack remains queryable after config removal')
  await assert.rejects(lifecycle.withActive('session-1', () => {}, () => {}), /agent_configuration_unavailable/)
  backend.resolve = () => ({ model: 'alpha' })
  await assert.rejects(lifecycle.withActive('session-1', () => { throw new Error('agent_capability_rejected') }, () => {}), /agent_capability_rejected/)
  assert.equal(live.size, 0)
  assert.equal(calls.some(([type]) => type === 'resume'), false)
  await lifecycle.withActive('session-1', () => {}, () => { assert.equal(live.size, 1) })
  assert.equal(calls.filter(([type]) => type === 'resume').length, 1)
})

test('TEST-SESSION-06 close never cancels busy work and admission holds the lifecycle until durable acknowledgement', async t => {
  const { lifecycle, backend, live } = setup(t)
  await lifecycle.provision(sessionRequest())
  backend.isBusy = () => true
  await assert.rejects(lifecycle.close('session-1'), /agent_run_in_progress/)
  assert.equal(live.size, 1)
  backend.isBusy = () => false
  const entered = Promise.withResolvers()
  const release = Promise.withResolvers()
  const admission = lifecycle.withActive('session-1', () => {}, async () => { entered.resolve(); await release.promise })
  await entered.promise
  const close = lifecycle.close('session-1')
  await Promise.resolve()
  assert.equal(live.size, 1)
  release.resolve()
  await admission
  await close
  assert.equal(live.size, 0)
})
