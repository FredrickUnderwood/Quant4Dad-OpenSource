import { readFileSync } from 'node:fs'
import { createHash } from 'node:crypto'
import { measureInput, algorithm } from '../src/meter/byte-budget.mjs'

// Offline validation of operator-collected, credential-free request fixtures.
// Output contains only route identities, hashes and counts, never conversation
// text. This checks evidence; it cannot certify a provider's hidden template.
try {
  if (process.argv.length !== 3) throw new Error()
  const source = readFileSync(process.argv[2])
  if (source.length > 32 * 1024 * 1024) throw new Error()
  const rows = JSON.parse(source)
  if (!Array.isArray(rows) || !rows.length || rows.length > 256) throw new Error()
  const cases = rows.map(row => {
    const budget = measureInput(row.request, row.context)
    if (!Number.isSafeInteger(row.actual_input_tokens) || row.actual_input_tokens < 1 || row.actual_input_tokens > budget) throw new Error()
    return { provider: row.context.provider, model: row.context.model, protocol: row.context.protocol,
      model_config_revision: row.context.modelConfigRevision, input_tokens: row.actual_input_tokens, reserved_tokens: budget,
      request_sha256: createHash('sha256').update(JSON.stringify(row.request)).digest('hex') }
  })
  process.stdout.write(JSON.stringify({ algorithm, evidence_sha256: createHash('sha256').update(source).digest('hex'), cases }, null, 2) + '\n')
} catch { process.stderr.write('agent_meter_calibration_failed\n'); process.exitCode = 1 }
