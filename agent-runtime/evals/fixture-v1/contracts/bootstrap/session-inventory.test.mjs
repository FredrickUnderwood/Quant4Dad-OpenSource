import test from 'node:test'
import assert from 'node:assert/strict'
import { compareSessions } from '../../../../src/operations/session-inventory.mjs'
import { sessionRequest } from '../../../../fixtures/session-config.mjs'

test('session inventory distinguishes orphan, lost acknowledgement and missing storage without adopting state', () => {
  const request = sessionRequest('session-1')
  const intent = { kind: 'intent', binding: { request } }, materialized = { kind: 'materialized', session_id: 'session-1' }
  const header = { id: 'session-1', agentPreset: 'q4d-bridge-v1:' + request.provision_request_hash }
  const product = { id: 'session-1', dsh_session_id: 'session-1', status: 'active' }
  assert.equal(compareSessions([intent, materialized], [header], [product]).status, 'consistent')
  assert.deepEqual(compareSessions([], [header], []).issues, [{ session_id: 'session-1', code: 'runtime_unbound_artifact' }])
  assert.deepEqual(compareSessions([intent], [header], [{ ...product, dsh_session_id: null, status: 'provisioning' }]).issues.map(i => i.code),
    ['product_provisioning_requires_reconcile', 'runtime_intent_requires_reconcile'])
  assert.deepEqual(compareSessions([intent, materialized], [], [product]).issues.map(i => i.code),
    ['product_acknowledged_storage_missing', 'runtime_acknowledged_storage_missing'])
  assert.equal(compareSessions([intent, materialized], [header], []).issues[0].code, 'runtime_binding_without_product')
  assert.equal(compareSessions([intent, materialized], [{ ...header, agentPreset: 'unowned' }], [product]).issues[0].code, 'runtime_identity_mismatch')
  assert.throws(() => compareSessions([intent, materialized, materialized], [header], [product]))
  assert.throws(() => compareSessions([], [header, header], []))
  assert.throws(() => compareSessions([], [], [{ ...product, dsh_session_id: 'another-session' }]))
})
