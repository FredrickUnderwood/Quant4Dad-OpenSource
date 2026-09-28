/** T-00 only. Public Cordis guard/dispatch hooks own the downstream HTTP call. */
import type { Context } from '@deepseek-ai/cordis'
import type { ToolExecution, ToolExecutionResult, ToolExecutionToken } from '@deepseek-ai/dsh-tools'
import { TrustedGateway } from '../src/mcp/trusted-gateway.mjs'
import { queryTool } from '../evals/fixture-v1/helpers/mcp-gateway.mjs'

export function installTrustedMcp(ctx: Context, url: string, runtimeToken: string, options: {
  supervisorIPC?: boolean,
  beforeDispatch?: (sessionId: string) => Promise<void>,
  beforeTool?: (exec: ToolExecution) => Promise<number>,
  observe?: (type: string, call: any, data: any) => void,
} = {}) {
  const gateway = new TrustedGateway({ url, runtimeToken, catalog: [queryTool] })
  const calls = new Map<ToolExecutionToken, ReturnType<typeof gateway.prepare>>()
  const approvals = new Map<string, { call: any, approvalId: string, expiresAt: number, decide: (receipt: string | undefined) => void }>()
  const sources = new Map<ToolExecutionToken, number>()
  const control = {
    beginRun: (sessionId: string, run: any) => gateway.beginRun(sessionId, run),
    endRun: (sessionId: string, runId: string) => gateway.endRun(sessionId, runId),
    approve(toolCallId: string, receipt?: string, runId?: string, approvalId?: string) {
      const approval = approvals.get(toolCallId)
      if (!approval || (runId !== undefined && approval.call.run_id !== runId) ||
          (approvalId !== undefined && approval.approvalId !== approvalId) || Date.now() >= approval.expiresAt) throw new Error('agent_approval_missing')
      approval.decide(receipt)
    },
  }

  // Test-only supervisor IPC: credentials and approval decisions never enter
  // an ACP prompt, Tool schema, tool arguments, or the DSH Session event log.
  if (options.supervisorIPC !== false) process.on('message', (message: any) => {
    try {
      if (message.method === 'beginRun') gateway.beginRun(message.params.dshSessionId, message.params.run)
      else if (message.method === 'endRun') gateway.endRun(message.params.dshSessionId, message.params.runId)
      else if (message.method === 'approvalDecision') {
        control.approve(message.params.toolCallId, message.params.receipt)
      } else throw new Error('fixture_unknown_control')
      process.send?.({ id: message.id, result: {} })
    } catch {
      process.send?.({ id: message.id, error: 'fixture_control_rejected' })
    }
  })

  if (options.beforeTool) ctx.on('tools/pre-execute', async (exec, next) => {
    sources.set(exec.token, await options.beforeTool!(exec))
    return next()
  })

  ctx.tools.guard(exec => {
    try {
      if (exec.name !== 'mcp__q4d__query_kline') return 'agent_tool_forbidden'
      calls.set(exec.token, gateway.prepare(exec.agent?.session.id, 'query_kline', exec.arguments))
      return undefined
    } catch { return 'agent_tool_context_or_arguments_rejected' }
  })

  // Deliberately never delegate to the generic MCP client. In this fixture it
  // provides only per-Session discovery/schema; its executor must see zero calls.
  ctx.on('tools/execute', async (exec): Promise<ToolExecutionResult> => {
    const call = calls.get(exec.token)
    if (!call) return failure('agent_tool_context_missing')
    let started = false
    let settled = false
    const observe = (type: string, data = {}) => options.observe?.(type, call, data)
    const onStarted = options.observe ? () => {
      if (started || settled) return
      started = true
      observe('tool.started')
    } : undefined
    const failed = (code: string) => {
      settled = true
      observe('tool.failed', { code })
      return failure(code)
    }
    // Persist Run admission and logical identity before any outbound attempt.
    await options.beforeDispatch?.(exec.agent!.session.id)
    observe('tool.proposed', { name: call.name, arguments: call.arguments, idempotency_key: call.idempotency_key,
      source_seq: String(sources.get(exec.token)) })
    process.send?.({ event: 'tool_dispatch', call })
    try {
      let result = await gateway.invoke(call, { signal: exec.signal, onStarted })
      if (result.structuredContent?.error?.code === 'agent_approval_required') {
        if (started) return failed('agent_tool_protocol_error')
        const challenge = result.structuredContent.error
        const expiresAt = Date.parse(challenge.expires_at)
        if (typeof challenge.approval_id !== 'string' || !challenge.approval_id || challenge.approval_id.length > 256 ||
            !/^sha256:[0-9a-f]{64}$/.test(challenge.arguments_hash) || !['R2', 'R3'].includes(challenge.risk) ||
            typeof challenge.expires_at !== 'string' || !Number.isFinite(expiresAt) ||
            expiresAt <= Date.now() || expiresAt - Date.now() > 300_000) return failed('agent_tool_protocol_error')
        observe('approval.required', { name: call.name, approval_id: challenge.approval_id,
          arguments_hash: challenge.arguments_hash, risk: challenge.risk, expires_at: new Date(expiresAt).toISOString() })
        const receipt = await new Promise<string | undefined>((resolve, reject) => {
          let timer: ReturnType<typeof setTimeout>
          const cleanup = () => {
            clearTimeout(timer)
            exec.signal.removeEventListener('abort', aborted)
            approvals.delete(call.tool_call_id)
          }
          const aborted = () => { cleanup(); reject(new Error('agent_tool_cancelled')) }
          if (exec.signal.aborted) return aborted()
          exec.signal.addEventListener('abort', aborted, { once: true })
          timer = setTimeout(() => { cleanup(); reject(new Error('agent_approval_timeout')) }, Math.max(0, expiresAt - Date.now()))
          approvals.set(call.tool_call_id, { call, approvalId: challenge.approval_id, expiresAt, decide: receipt => {
            cleanup()
            if (Date.now() >= expiresAt) return reject(new Error('agent_approval_timeout'))
            resolve(receipt)
          } })
          process.send?.({ event: 'approval_required', call })
        })
        if (!receipt) return failed('agent_approval_denied')
        result = await gateway.invoke(call, { receipt, signal: exec.signal, onStarted })
      }
      if (result.isError) return failed('agent_tool_failed')
      settled = true
      observe('tool.completed')
      return { isError: false, value: result, content: result.content }
    } catch (error) { return failed(exec.signal.aborted ? 'agent_tool_cancelled'
      : error instanceof Error && error.message === 'agent_approval_timeout' ? 'agent_approval_timeout' : 'agent_tool_transport_or_context_rejected') }
  })
  ctx.on('tools/result', exec => { calls.delete(exec.token); sources.delete(exec.token); return undefined })
  return control
}

function failure(code: string): ToolExecutionResult {
  return { isError: true, error: { message: code }, content: [{ type: 'text', text: code }] }
}
