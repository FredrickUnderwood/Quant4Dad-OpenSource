import { invocationId } from '../mcp/trusted-gateway.mjs'

/** Copy only stable, non-sensitive public token counters. Input/cache counts
 * are disjoint upstream; reasoning is part of output, not another total. */
export function projectUsage(usage) {
  const projected = {}
  for (const [source, target] of Object.entries({ inputTokens: 'input_tokens', outputTokens: 'output_tokens',
    totalTokens: 'total_tokens', cacheReadTokens: 'cache_read_tokens', cacheWriteTokens: 'cache_write_tokens',
    reasoningTokens: 'reasoning_tokens' })) {
    if (usage[source] !== undefined) projected[target] = usage[source]
  }
  return projected
}

export function terminalData(state, reason) {
  if (state === 'cancelled') return { reason: 'user' }
  if (state === 'interrupted') return { code: 'agent_runtime_interrupted', retryable: false }
  if (state === 'failed') return { code: ({ error: 'agent_model_error', 'max-tokens': 'agent_model_limit',
    blocked: 'agent_run_blocked' })[reason?.kind] ?? 'agent_run_failed', retryable: false }
  return {}
}

/** A flushed native result without a dispatch proposal is a pre-dispatch
 * rejection in the explicit trusted composition. Correlate by public source
 * seq, never by a model ID (which may be reused across calls and Turns). */
export function projectRejectedTool(journal, result, events, runId, afterProposed = () => {}) {
  const sources = result.sourceEventSeqs ?? []
  const call = sources.length === 1 ? events.find(event => event.seq === sources[0] && event.type === 'tool/call') : undefined
  if (!call || call.data.turn !== result.data.turn || call.data.step !== result.data.step) throw new Error('agent_tool_source_missing')
  const sourceSeq = String(call.seq)
  let proposed = journal.snapshot().find(event => event.type === 'tool.proposed' && event.data.source_seq === sourceSeq)
  if (proposed && !proposed.data.arguments_omitted) return
  const blocks = result.data.message.content
  const failed = blocks.length === 1 && blocks[0].type === 'tool-result' && blocks[0].isError === true
  if (!failed) throw new Error('agent_unobserved_tool_dispatch')
  if (!proposed) {
    const id = invocationId()
    proposed = journal.append(`rejected-proposal:${call.seq}`, {
      type: 'tool.proposed', occurred_at: new Date(call.time).toISOString(), data: {
        tool_call_id: id, source_seq: sourceSeq, name: call.data.name.slice(0, 256),
        arguments: {}, arguments_omitted: true, idempotency_key: `q4d:${runId}:${id}`,
      },
    })
    afterProposed()
  }
  const text = blocks[0].content.filter(block => block.type === 'text').map(block => block.text).join('')
  const code = ({
    'Error: agent_tool_forbidden': 'agent_tool_forbidden',
    'Error: agent_run_budget_exceeded': 'agent_run_budget_exceeded',
    'Error: agent_capability_expired': 'agent_capability_expired',
    'Error: agent_configuration_unavailable': 'agent_configuration_unavailable',
    'Error: agent_tool_context_or_arguments_rejected': 'agent_tool_context_or_arguments_rejected',
  })[text] ?? (text.startsWith('Error: agent_tool_context_or_arguments_rejected: ') ? 'agent_tool_context_or_arguments_rejected'
    : result.data.error?.code === 'ABORTED_BEFORE_DISPATCH' ? 'agent_tool_cancelled' : 'agent_tool_rejected')
  journal.append(`rejected-result:${result.seq}`, { type: 'tool.failed', occurred_at: new Date(result.time).toISOString(),
    data: { tool_call_id: proposed.data.tool_call_id, code } })
}
