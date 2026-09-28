import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import Ajv2020 from 'ajv/dist/2020.js'

test('TEST-MCP-CONTRACT-01 logical invocation excludes credentials and model call IDs', async () => {
  const schema = JSON.parse(await readFile(new URL('../../../../contracts/bridge-v1/tool-invocation.schema.json', import.meta.url)))
  const validate = new Ajv2020().compile(schema)
  const invocation = {
    session_id: 'session-1', run_id: 'run-1',
    tool_call_id: '01K4DAD0000000000000000000',
    idempotency_key: 'q4d:run-1:01K4DAD0000000000000000000',
    name: 'query_kline', arguments: { symbol: '000001', limit: 2 },
  }
  assert.equal(validate(invocation), true, JSON.stringify(validate.errors))
  for (const field of ['run_capability', 'approval_receipt', 'runtime_token', 'model_call_id']) {
    assert.equal(validate({ ...invocation, [field]: 'untrusted' }), false)
  }
  assert.equal(validate({ ...invocation, tool_call_id: 'q4d-query' }), false)
})
