import { readFile, lstat } from 'node:fs/promises'
import { createHash } from 'node:crypto'
import { checkSessionRequest } from '../persistence/session-bindings.mjs'

const fail = () => { throw new Error('agent_inventory_invalid') }
const id = value => typeof value === 'string' && /^[A-Za-z0-9_-]{1,128}$/.test(value)
export async function readJournal(path) {
  const stat = await lstat(path)
  if (!stat.isFile() || stat.isSymbolicLink() || stat.size > 16 * 1024 * 1024) fail()
  const text = await readFile(path, 'utf8')
  if (text && !text.endsWith('\n')) fail()
  return text.split('\n').filter(Boolean).map((line, index) => {
    const row = JSON.parse(line)
    if (row.sequence !== index + 1 || row.checksum !== createHash('sha256').update(JSON.stringify(row.data)).digest('hex')) fail()
    return row.data
  })
}

// IDs and fixed status codes only: never emit titles, prompts or credentials.
// Product rows must be a complete projection captured while admission is stopped.
export function compareSessions(rows, headers, product) {
  if (![rows, headers, product].every(Array.isArray) || [rows, headers, product].some(a => a.length > 20000)) fail()
  const bindings = new Map(), stored = new Map(), products = new Map(), issues = []
  const issue = (session_id, code) => issues.push({ session_id, code })
  for (const row of rows) {
    if (row.kind === 'intent') {
      const request = row.binding?.request
      checkSessionRequest(request)
      if (bindings.has(request.q4d_session_id)) fail()
      bindings.set(request.q4d_session_id, { request, durable: false })
    } else if (row.kind === 'materialized' && bindings.has(row.session_id) && !bindings.get(row.session_id).durable) {
      bindings.get(row.session_id).durable = true
    } else fail()
  }
  for (const header of headers) {
    if (!id(header.id) || stored.has(header.id)) fail()
    stored.set(header.id, header)
    const binding = bindings.get(header.id)
    if (!binding) issue(header.id, 'runtime_unbound_artifact')
    else if (header.agentPreset !== 'q4d-bridge-v1:' + binding.request.provision_request_hash) issue(header.id, 'runtime_identity_mismatch')
  }
  for (const row of product) {
    if (!id(row.id) || products.has(row.id) || !['active', 'archived', 'provisioning', 'provisioning_failed'].includes(row.status) ||
        ![null, row.id].includes(row.dsh_session_id) || Object.keys(row).sort().join() !== 'dsh_session_id,id,status') fail()
    products.set(row.id, row)
    const binding = bindings.get(row.id)
    if (['active', 'archived'].includes(row.status)) {
      if (row.dsh_session_id !== row.id || !binding?.durable || !stored.has(row.id)) issue(row.id, 'product_acknowledged_storage_missing')
    } else issue(row.id, 'product_provisioning_requires_reconcile')
  }
  for (const [sessionID, binding] of bindings) {
    if (!products.has(sessionID)) issue(sessionID, 'runtime_binding_without_product')
    if (binding.durable && !stored.has(sessionID)) issue(sessionID, 'runtime_acknowledged_storage_missing')
    if (!binding.durable) issue(sessionID, 'runtime_intent_requires_reconcile')
  }
  issues.sort((a, b) => a.session_id.localeCompare(b.session_id) || a.code.localeCompare(b.code))
  return { status: issues.length ? 'requires_reconciliation' : 'consistent', product_sessions: products.size,
    runtime_bindings: bindings.size, stored_sessions: stored.size, issues }
}
