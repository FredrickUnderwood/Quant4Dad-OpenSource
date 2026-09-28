// Self-contained, synchronous candidate meter. Do not label this an exact
// tokenizer: byte-BPE bounds text, while provider framing still needs real
// route calibration. See docs/Agent输入计量与校准-v1.md.
export const contract = 'q4d-input-meter-v1'
export const algorithm = 'utf8-envelope-budget-v1'
export const qualification = 'requires-route-calibration'
const fail = () => { throw new Error('agent_input_measurement_unavailable') }
const encoder = new TextEncoder()
const protocols = new Set(['openai-completions', 'anthropic-messages'])
const fields = new Set(['provider', 'model', 'messages', 'system', 'tools', 'maxTokens', 'temperature', 'reasoningEffort',
  'stop', 'sessionId', 'purpose'])

// Count the entire lossless-JSON envelope, including replay metadata and
// nested schemas/results. JSON encoding includes escaping and property names;
// another escaping pass covers tool arguments serialized inside JSON strings.
// Bounds are checked before serialization and recursion never exceeds 64.
function inspect(value, seen, stats, depth = 0) {
  if (depth > 64 || ++stats.nodes > 200000) fail()
  if (value === undefined || value === null || typeof value === 'boolean') return
  if (typeof value === 'number') { if (!Number.isFinite(value)) fail(); return }
  if (typeof value === 'string') {
    if (!value.isWellFormed() || value.length > 2 * 1024 * 1024) fail()
    stats.bytes += encoder.encode(value).length
    if (stats.bytes > 4 * 1024 * 1024) fail()
    return
  }
  if (typeof value !== 'object' || seen.has(value) ||
      (!Array.isArray(value) && Object.getPrototypeOf(value) !== Object.prototype)) fail()
  seen.add(value)
  for (const [key, child] of Object.entries(value)) { inspect(key, seen, stats, depth + 1); inspect(child, seen, stats, depth + 1) }
  seen.delete(value)
}
function blocks(values, depth = 0) {
  if (!Array.isArray(values) || depth > 32) fail()
  let count = 0
  for (const block of values) {
    if (!block || !['text', 'reasoning', 'tool-call', 'tool-result'].includes(block.type)) fail()
    count++
    if (block.type === 'tool-result') count += blocks(block.content, depth + 1)
  }
  return count
}

export function measureInput(request, context) {
  if (!request || !context || !protocols.has(context.protocol) || request.provider !== context.provider || request.model !== context.model ||
      !/^[A-Za-z0-9_./:-]{1,128}$/.test(context.model) || !/^[0-9a-f]{32}$/.test(context.modelConfigRevision) ||
      Object.keys(request).some(key => !fields.has(key)) || !Array.isArray(request.messages) || request.messages.length > 4096 ||
      (request.tools !== undefined && (!Array.isArray(request.tools) || request.tools.length > 128))) fail()
  const stats = { nodes: 0, bytes: 0 }
  inspect(request, new Set(), stats)
  let blockCount = 0
  for (const message of request.messages) {
    if (!['user', 'assistant', 'system'].includes(message.role)) fail()
    blockCount += blocks(message.content)
  }
  const serialized = JSON.stringify(request)
  if (serialized.length > 8 * 1024 * 1024) fail()
  const bytes = encoder.encode(JSON.stringify(serialized)).length
  // These are explicit framing reserves, not a chars-per-token divisor.
  // Calibration must check every deployed route and invalidate on any drift.
  const budget = bytes + 4096 + 128 * request.messages.length + 64 * blockCount +
    512 * (request.tools?.length ?? 0) + 8 * stats.nodes
  if (!Number.isSafeInteger(budget) || budget > 100000000) fail()
  return budget
}
