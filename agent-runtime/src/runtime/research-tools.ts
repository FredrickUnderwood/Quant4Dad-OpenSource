import type { Context } from '@deepseek-ai/cordis'
import type { ToolExecution, ToolExecutionResult, ToolExecutionToken } from '@deepseek-ai/dsh-tools'
import { assertSupportedJsonSchema } from '@deepseek-ai/dsh-tools'
import Ajv from 'ajv'
import { GatewayError, TrustedGateway } from '../mcp/trusted-gateway.mjs'
import { ApprovalWaiters } from './approval-waiters.mjs'
import { modelToolResult } from './research-evidence.mjs'

export const toolPrefix = 'mcp__q4d__'
const publicFailures = new Set(['agent_approval_timeout', 'agent_approval_invalid', 'agent_approval_rejected', 'agent_approval_denied',
  'agent_tool_start_unconfirmed', 'agent_tool_transport_error', 'agent_tool_result_too_large', 'agent_tool_result_unknown',
  'agent_capability_rejected', 'agent_capability_expired', 'agent_tool_budget_exceeded', 'agent_tool_storage_unavailable',
  'tool_invalid_arguments', 'tool_timeout', 'tool_result_too_large', 'tool_not_found', 'resource_version_conflict', 'tool_unavailable',
  'strategy_validation_required', 'strategy_validation_mismatch', 'strategy_validation_expired'])
export type ResearchCatalog = { profile: string; revision: string; tools: Array<{
  name: string; description: string; inputSchema: any; outputSchema: any;
  risk: 'R0' | 'R1' | 'R2' | 'R3'; timeout_ms: number; max_result_bytes: number;
}> }

/** Public DSH hooks only. Native call IDs locate source events; a separate
 * durable logical ID owns the Gateway attempt. No generic MCP executor exists. */
export function installResearchTools(ctx: Context, catalog: ResearchCatalog, options: {
  selectTools?: (profile: string) => ResearchCatalog['tools'];
  beforeTool: (exec: ToolExecution) => Promise<number>;
  execution: (sessionId: string) => { lease: any; gateway: TrustedGateway };
  observe: (type: string, call: any, data: any) => void;
}) {
  const approvals = new ApprovalWaiters()
  const calls = new Map<ToolExecutionToken, { call: any; execution: ReturnType<typeof options.execution> }>()
  const sources = new Map<ToolExecutionToken, number>()
  const ajv = new Ajv({ strict: true })
  const outputs = new Map(catalog.tools.map(tool => {
    try {
      const schema = dshOutputSchema(tool.outputSchema)
      assertSupportedJsonSchema(schema)
      return [tool.name, { validate: ajv.compile(tool.outputSchema), schema }] as const
    } catch (error) {
      // Static identifiers only: no schema values, provider diagnostics or body.
      console.error(JSON.stringify({ event: 'agent_tool_catalog_invalid', tool_name: tool.name, stage: 'output_schema',
        code: error instanceof Error && (error as { code?: unknown }).code === 'UNSUPPORTED_SCHEMA' ? 'UNSUPPORTED_SCHEMA' : 'schema_invalid' }))
      throw new Error('agent_tool_catalog_invalid')
    }
  }))
  const names = new Set(catalog.tools.map(tool => toolPrefix + tool.name))
  ctx.on('tools/pre-execute', async (exec, next) => {
    sources.set(exec.token, await options.beforeTool(exec))
    return next()
  })
  ctx.tools.guard(exec => {
    try {
      if (!names.has(exec.name)) return 'agent_tool_forbidden'
      const execution = options.execution(exec.agent!.session.id)
      const call = execution.gateway.prepare(exec.agent!.session.id, exec.name.slice(toolPrefix.length), exec.arguments)
      execution.lease.tool()
      calls.set(exec.token, { call, execution })
      return undefined
    } catch (error) {
      const code = error instanceof Error ? error.message : ''
      if (error instanceof GatewayError && code === 'agent_invalid_arguments' && error.validation?.length) {
        return 'agent_tool_context_or_arguments_rejected: ' + JSON.stringify(error.validation)
      }
      return ['agent_run_budget_exceeded', 'agent_capability_expired', 'agent_configuration_unavailable'].includes(code)
        ? code : 'agent_tool_context_or_arguments_rejected'
    }
  })
  ctx.on('tools/execute', async (exec): Promise<ToolExecutionResult> => {
    const owned = calls.get(exec.token)
    if (!owned) return failure('agent_tool_context_missing')
    const { call, execution } = owned
    const observe = (type: string, data = {}) => options.observe(type, call, data)
    // beforeTool flushed the native call and run.started. This synchronous
    // append fsyncs the identity before initialize or tools/call can leave.
    observe('tool.proposed', { name: call.name, arguments: call.arguments, idempotency_key: call.idempotency_key,
      source_seq: String(sources.get(exec.token)) })
    try {
      execution.lease.check()
      const signal = AbortSignal.any([exec.signal, execution.lease.signal])
      let result = await execution.gateway.invoke(call, { signal, onStarted: () => observe('tool.started') })
      const challenge = (result?.structuredContent as { error?: { code?: string } } | undefined)?.error
      if (challenge?.code === 'agent_approval_required') {
        const receipt = await approvals.wait(call, challenge, signal, observe)
        execution.lease.check()
        if (!receipt) { observe('tool.failed', { code: 'agent_approval_denied' }); return failure('agent_approval_denied') }
        result = await execution.gateway.invoke(call, { receipt, signal, onStarted: () => observe('tool.started') })
      }
      execution.lease.check()
      if (result?.isError) {
        const code = Array.isArray(result.content) ? result.content.find(item => item?.type === 'text')?.text : undefined
        throw new Error(typeof code === 'string' && publicFailures.has(code) ? code : 'agent_tool_failed')
      }
      if (!result || !outputs.get(call.name)!.validate(result.structuredContent)) throw new Error('agent_tool_failed')
      const text = JSON.stringify(result.structuredContent)
      if (Buffer.byteLength(text) > catalog.tools.find(tool => tool.name === call.name)!.max_result_bytes) {
        throw new Error('agent_tool_result_too_large')
      }
      observe('tool.completed')
      return { isError: false, value: JSON.parse(text), content: [{ type: 'text', text: JSON.stringify(modelToolResult(call.name, result.structuredContent)) }] }
    } catch (error) {
      const reason = exec.signal.aborted || execution.lease.signal.aborted ? 'agent_tool_cancelled'
        : error instanceof Error && publicFailures.has(error.message) ? error.message : 'agent_tool_failed'
      const code = reason.startsWith('agent_') ? reason : 'agent_' + reason
      observe('tool.failed', { code })
      return failure(code)
    }
  })
  ctx.on('tools/result', exec => { calls.delete(exec.token); sources.delete(exec.token); return undefined })
  const setup = (agentCtx: Context, profile = catalog.profile) => {
    for (const tool of options.selectTools?.(profile) ?? catalog.tools) agentCtx.tools.register({
      name: toolPrefix + tool.name, description: tool.description + '\n审批规则：' +
        (tool.risk === 'R0' || tool.risk === 'R1'
          ? `${tool.risk}，不需要一次性审批；仍须遵守用户授权范围和停止条件。`
          : `${tool.risk}，需要系统审批挑战及绑定到本次调用和参数的批准。聊天中的授权不是审批凭据；实际风险可能由参数升级，以系统挑战为准。`), parameters: tool.inputSchema,
      // DSH's public output-schema subset excludes numeric/array bounds. The
      // complete catalog schema is enforced by Ajv above before returning.
      output: { schema: outputs.get(tool.name)!.schema, render: (_args, value) => [{ type: 'text', text: JSON.stringify(modelToolResult(tool.name, value)) }] },
      // The trusted around-dispatch hook is the sole executor; fail closed if
      // composition is changed and it no longer intercepts this definition.
      async execute() { throw new Error('agent_tool_context_missing') },
    })
  }
  return Object.assign(setup, { decide: (id: string, body: any) => approvals.decide(id, body) })
}
function dshOutputSchema(schema: any): any {
  const value = structuredClone(schema)
  for (const key of ['minimum', 'maximum', 'exclusiveMinimum', 'exclusiveMaximum', 'minItems', 'maxItems', 'maxLength', 'pattern']) delete value[key]
  if (value.properties) for (const key of Object.keys(value.properties)) value.properties[key] = dshOutputSchema(value.properties[key])
  if (value.items) value.items = dshOutputSchema(value.items)
  if (value.oneOf) value.oneOf = value.oneOf.map(dshOutputSchema)
  return value
}
function failure(code: string): ToolExecutionResult {
  // Keep journal/card codes stable while making legacy names unambiguous to
  // the model. Meanings are trusted static text, never raw backend errors.
  const validationMeanings: Record<string, string> = {
    agent_strategy_validation_required: 'No valid strategy validation record is available for this user and session. The strategy was not saved. If continuation is authorized, validate the complete definition and use its validation_id; approval is still required.',
    agent_strategy_validation_mismatch: 'The complete strategy definition or checker revision differs from the validation record. The strategy was not saved. If continuation is authorized, revalidate the complete definition and use the new validation_id. Do not silently change fields after validation.',
    agent_strategy_validation_expired: 'The strategy validation record has expired. The strategy was not saved. If continuation is authorized, validate again and use the new validation_id; approval is still required.',
  }
  const meaning = validationMeanings[code] ?? (code === 'agent_tool_not_found'
    ? 'The requested resource or record was not found. The tool exists and handled the request; this does not mean the tool is unregistered or the numeric ID is invalid.'
    : code === 'agent_tool_unavailable'
      ? 'This tool call failed. Its cause, duration, and availability through another interface are unknown.'
      : undefined)
  const text = meaning ? JSON.stringify({ error: { code, meaning,
    ...(code === 'agent_tool_not_found' ? { category: 'resource_not_found', input_schema_accepted: true } : {}) } }) : code
  return { isError: true, error: { message: text }, content: [{ type: 'text', text }] }
}
