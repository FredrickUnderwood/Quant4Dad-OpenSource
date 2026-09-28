import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { dirname, join } from 'node:path'
import test from 'node:test'
import { fileURLToPath } from 'node:url'
import Ajv2020 from 'ajv/dist/2020.js'
import addFormats from 'ajv-formats'

const here = dirname(fileURLToPath(import.meta.url))
const contractRoot = join(here, '../../../../contracts/bridge-v1')

async function json(path) {
  return JSON.parse(await readFile(path, 'utf8'))
}

async function validator(name) {
  const ajv = new Ajv2020({ allErrors: true, strict: true })
  addFormats(ajv)
  ajv.addSchema(await json(join(contractRoot, 'tool-event.schema.json')))
  return ajv.compile(await json(join(contractRoot, name)))
}

test('TEST-BRIDGE-CONTRACT-01 capabilities are explicit and forward compatible', async () => {
  const validate = await validator('capabilities.schema.json')
  const fixture = await json(join(here, 'fixtures/capabilities.valid.json'))
  assert.equal(validate(fixture), true, JSON.stringify(validate.errors))

  delete fixture.features.event_replay
  assert.equal(validate(fixture), false)
})

test('TEST-BRIDGE-CONTRACT-02 prompt accepts one bounded text block only', async () => {
  const validate = await validator('prompt-request.schema.json')
  const fixture = await json(join(here, 'fixtures/prompt.valid.json'))
  assert.equal(validate(fixture), true, JSON.stringify(validate.errors))

  fixture.content.push({ type: 'image', data: 'AQ==' })
  assert.equal(validate(fixture), false)
})

test('TEST-EVENT-01 canonical event vocabulary contains exactly 16 types', async () => {
  const schema = await json(join(contractRoot, 'event.schema.json'))
  const types = schema.properties.type.enum
  assert.equal(types.length, 16)
  assert.equal(new Set(types).size, 16)

  const validate = await validator('event.schema.json')
  const samples = await json(join(here, 'fixtures/events.valid.json'))
  for (const [index, type] of types.entries()) {
    const event = {
      id: String(index + 1),
      run_id: '01K4DADRUN',
      session_id: '01K4DADSESSION',
      type,
      occurred_at: '2026-09-05T00:00:00Z',
      schema_version: 1,
      data: samples[type],
      future_extension: true,
    }
    assert.equal(validate(event), true, `${type}: ${JSON.stringify(validate.errors)}`)
  }
})
