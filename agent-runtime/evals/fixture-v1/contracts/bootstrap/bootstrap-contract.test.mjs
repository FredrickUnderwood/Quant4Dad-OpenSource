import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import Ajv2020 from 'ajv/dist/2020.js'
import { createRunVerifier, runtimeAudience } from '../../../../src/auth/run-capability.mjs'

const root = new URL('../../../../contracts/bootstrap-v1/', import.meta.url)
const schema = JSON.parse(readFileSync(new URL('bootstrap.schema.json', root)))
const fixture = JSON.parse(readFileSync(new URL('fixtures/bootstrap.valid.json', root)))
const validate = new Ajv2020({ strict: true }).compile(schema)

test('bootstrap fixture matches the complete Go response and trusted verifier key format', () => {
  assert.equal(validate(fixture), true)
  const { issuer, public_keys: keys } = fixture.capability
  assert.doesNotThrow(() => createRunVerifier({ issuer, keys, audience: runtimeAudience, authorize: () => false }))
  assert.equal(fixture.revision.split('.')[1], fixture.model_config_revision)
  for (const key of Object.values(keys)) assert.equal(Buffer.from(key, 'base64url').toString('base64url'), key)
})

test('bootstrap wire rejects extra secrets, revoked candidates and incompatible protocol versions', () => {
  const mutations = [
    v => { v.control_token = 'must-never-be-distributed' },
    v => { v.capability.private_key = 'must-never-be-distributed' },
    v => { v.capability.algorithm = 'EdDSA' },
    v => { v.capability.public_keys = {} },
    v => { v.capability.public_keys['key-1'] += '=' },
    v => { v.protocol_version = 'q4d-bootstrap-v2' },
    v => { v.model_config_revision += '\n' },
    v => { v.providers[0].credential_revoked = true },
    v => { v.providers[0].agent.enabled = false },
    v => { v.providers[0].agent.context_window = 0 },
    v => { v.providers[0].api_key = 'bad\nkey' },
    v => { v.providers[0].agent.reasoning_effort = 'bad\nvalue' },
    v => { v.mcp.runtime_token = '' },
  ]
  for (const mutate of mutations) {
    const value = structuredClone(fixture)
    mutate(value)
    assert.equal(validate(value), false)
  }
})

test('bootstrap permits an empty replacement set and explicitly keyless candidates', () => {
  const empty = { ...fixture, providers: [] }
  assert.equal(validate(empty), true)
  const keyless = structuredClone(fixture)
  keyless.providers[0].api_key = ''
  assert.equal(validate(keyless), true)
})
