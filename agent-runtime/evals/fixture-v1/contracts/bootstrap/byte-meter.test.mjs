import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { measureInput } from '../../../../src/meter/byte-budget.mjs'

const context = { provider: 'fixture', model: 'gpt-4o', protocol: 'openai-completions', modelConfigRevision: 'a'.repeat(32) }
const request = () => ({ provider: 'fixture', model: 'gpt-4o', messages: [{ role: 'user', content: [{ type: 'text', text: '你好 🧑🏽‍💻' }] }] })
test('candidate meter includes UTF-8, escaping, schemas, tool results and private replay state', () => {
  const r = request(), base = measureInput(r, context)
  for (const mutate of [r => r.system = 'x'.repeat(1024), r => r.tools = [{ name: 'f', parameters: { description: '界'.repeat(1024) } }],
    r => r.messages.push({ role: 'user', content: [{ type: 'tool-result', content: [{ type: 'text', text: '\\"'.repeat(1024) }] }] }),
    r => r.messages.push({ role: 'assistant', content: [{ type: 'reasoning', text: 'hi' }], source: { replayState: { signature: 'x'.repeat(1024) } } })]) {
    const changed = request(); mutate(changed)
    assert.ok(measureInput(changed, context) >= base + 1024)
  }
  assert.equal(measureInput(r, { ...context, protocol: 'anthropic-messages' }), base)
  assert.deepEqual(r, request())
})
test('candidate meter rejects unsupported and unbounded input without fallback', () => {
  for (const mutate of [r => r.messages[0].content.push({ type: 'image', data: 'a' }), r => r.ambient = 'key',
    r => r.messages[0].content[0].text = '\ud800', r => r.system = 'x'.repeat(2 * 1024 * 1024 + 1),
    r => r.messages.push(r), r => r.tools = Array(129).fill({}), r => r.messages = Array(4097).fill(r.messages[0])]) {
    const r = request(); mutate(r); assert.throws(() => measureInput(r, context), /agent_input_measurement_unavailable/)
  }
  assert.throws(() => measureInput(request(), { ...context, protocol: 'openai-responses' }))
  assert.throws(() => measureInput(request(), { ...context, provider: 'other' }))
})
test('candidate is a self-contained SHA-loadable module', async () => {
  const bytes = await readFile(new URL('../../../../src/meter/byte-budget.mjs', import.meta.url))
  const loaded = await import('data:text/javascript;base64,' + bytes.toString('base64'))
  assert.equal(loaded.contract, 'q4d-input-meter-v1')
  assert.equal(loaded.qualification, 'requires-route-calibration')
  assert.equal(loaded.measureInput(request(), context), measureInput(request(), context))
})
