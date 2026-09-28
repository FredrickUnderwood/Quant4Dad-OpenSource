/** T-00 only: explicit public-plugin composition, no base profile or core patches. */
import { Context } from '@deepseek-ai/cordis'
import LlmRuntime from '@deepseek-ai/dsh-llm'
import SessionStore from '@deepseek-ai/dsh-session'
import SystemPrompt from '@deepseek-ai/dsh-system-prompt'
import ToolRuntime from '@deepseek-ai/dsh-tools'
import AgentRegistry from '@deepseek-ai/dsh-agent'
import AgentLoop from '@deepseek-ai/dsh-agent-loop'
import SessionProjectionRegistry from '@deepseek-ai/dsh-session-projection'
import JsonlSessionPersistence from '@deepseek-ai/dsh-session-persistence-jsonl'
import TokenMeter from '@deepseek-ai/dsh-token-meter'
import ApprovalService from '@deepseek-ai/dsh-user-approval'
import * as AcpPlugin from '@deepseek-ai/dsh-acp'
import * as FixtureModel from './control-surface-llm/index.mjs'
import { appendFileSync } from 'node:fs'
import { join } from 'node:path'
import { installTrustedMcp } from './trusted-mcp-plugin.ts'

const root = process.env.Q4D_T00_PERSISTENCE_ROOT
if (!root) throw new Error('Q4D_T00_PERSISTENCE_ROOT is required')
const ctx = new Context()
await ctx.plugin(LlmRuntime)
await ctx.plugin(SessionStore)
await ctx.plugin(SystemPrompt, { persona: '' })
await ctx.plugin(ToolRuntime, {})
await ctx.plugin(AgentRegistry)
await ctx.plugin(SessionProjectionRegistry)
await ctx.plugin(JsonlSessionPersistence, { root, compression: 'none' })
await ctx.plugin(TokenMeter)
await ctx.plugin(AgentLoop, { agents: [] })
await ctx.plugin(FixtureModel)

if (process.env.Q4D_T00_GATEWAY_URL) {
  installTrustedMcp(ctx, process.env.Q4D_T00_GATEWAY_URL, process.env.Q4D_T00_RUNTIME_TOKEN!)
}

// A test evidence sink, not the production Bridge event journal. These public
// hooks distinguish a proposal from actual dispatch (ACP calls both in_progress).
function record(stage: string, exec: { callId: string; agent?: { session: { id: string } } }) {
  appendFileSync(join(root!, '../lifecycle.jsonl'), `${JSON.stringify({
    stage, callId: exec.callId, sessionId: exec.agent?.session.id,
  })}\n`, { mode: 0o600 })
}
ctx.on('tools/execute', async (exec, next) => {
  record('started', exec)
  return next()
})
ctx.on('tools/result', (exec, result) => {
  record(result.isError ? 'failed' : 'completed', exec)
  return undefined
})

// Test-only policy hook exercises the public one-shot approval channel.
// Product Capability/receipt enforcement remains a separate Go/Adapter task.
if (process.env.Q4D_T00_PERMISSION === '1') {
  await ctx.plugin(ApprovalService)
  ctx.on('tools/pre-execute', async exec => {
    record('proposed', exec)
    return { kind: 'ask', reason: 'T-00 approval fixture' }
  })
} else {
  ctx.on('tools/pre-execute', async (exec, next) => {
    record('proposed', exec)
    return next()
  })
}
await ctx.plugin(AcpPlugin, { provider: 'q4d-control-fixture', model: 'alpha' })

let closing = false
async function close() {
  if (closing) return
  closing = true
  await ctx.fiber.dispose()
  process.exit(0)
}
process.on('SIGTERM', () => { void close() })
process.stdin.on('end', () => { void close() })
