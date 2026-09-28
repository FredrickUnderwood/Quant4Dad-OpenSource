import type { Context } from '@deepseek-ai/cordis'
import type { GenerateOptions } from '@deepseek-ai/dsh-llm'

export const name = 'q4d-run-policy'
export const OUTPUT_RECOVERY_CODE = 'Q4D_OUTPUT_RECOVERY'
export const MAX_OUTPUT_RECOVERIES = 2

export function apply(ctx: Context, options: { execution: (session: string) => any;
  progress: (agent: any, execution: any, stage: string) => Promise<void> }) {
  ctx.on('agent/pre-step', ({ agent }, next) => {
    options.execution(agent.session.id)?.lease.check()
    return next()
  })
  ctx.on('agent/request-error', async ({ agent, failure, signal }, next) => {
    const execution = options.execution(agent.session.id)
    if (failure.code !== OUTPUT_RECOVERY_CODE || !execution?.pendingOutputRecovery || signal.aborted) return next()
    execution.lease.check()
    execution.pendingOutputRecovery = false
    await options.progress(agent, execution, 'output_recovery')
    return { kind: 'retry' }
  })
}

/** A final answer has a small independent reserve, not the provider's ceiling. */
export function finalOutputReserve(remaining: any, outputLimit: number) {
  return Math.min(outputLimit, 8192, Math.max(1, Math.floor(remaining.output_tokens.limit / 4)))
}

export function compactionOutputLimit(remaining: any, outputLimit: number) {
  if (remaining.model_calls.remaining < 3) return 0
  const reserve = finalOutputReserve(remaining, outputLimit)
  // Leave a working response and the final answer after the summary.
  return Math.max(0, Math.min(8192, outputLimit, remaining.output_tokens.remaining - reserve * 2))
}

export function canRecoverOutput(execution: any, maxTokens: number) {
  const remaining = execution.lease.snapshot()
  const reserve = finalOutputReserve(remaining, execution.modelOutputLimit)
  return (execution.outputRecoveries ?? 0) < MAX_OUTPUT_RECOVERIES &&
    remaining.model_calls.remaining >= 2 && remaining.input_tokens.remaining >= remaining.last_input * 2 &&
    remaining.output_tokens.remaining - reserve >= Math.min(maxTokens, 1024)
}

/** The same supervisor owns admission, tools, compaction and provider calls. */
export function prepareBudgetedRequest(request: GenerateOptions, lease: any, outputLimit: number, retryOutputLimit?: number) {
  const remaining = lease.snapshot()
  const reserve = finalOutputReserve(remaining, outputLimit)
  const finalizing = (remaining.last_input > 0 && remaining.input_tokens.remaining < remaining.last_input * 2) ||
    remaining.model_calls.remaining <= 1 || remaining.output_tokens.remaining <= reserve ||
    ((request.tools?.length ?? 0) > 0 && remaining.tool_calls.remaining === 0)
  const outgoing = { ...request }
  outgoing.maxTokens = Math.max(1, Math.min(outputLimit, request.maxTokens ?? outputLimit,
    retryOutputLimit ?? 16384, remaining.output_tokens.remaining - (finalizing ? 0 : reserve)))
  if (finalizing) delete outgoing.tools
  outgoing.system = [request.system, `本轮剩余预算：模型调用 ${remaining.model_calls.remaining} 次；工具调用 ${remaining.tool_calls.remaining} 次；输出 ${remaining.output_tokens.remaining} tokens。本次生成最多 ${outgoing.maxTokens} tokens（包含思考）；最终回答预留 ${reserve} tokens。`,
    retryOutputLimit ? '上一次生成因输出额度耗尽被截断，其中的工具调用均未执行。请缩短思考和说明，只完成当前下一步；参考已有工具结果，避免重复已完成的操作。' : '',
    finalizing ? '本次为收尾回答。根据已有证据说明已完成内容、明确未完成事项和下一步；不要发起或用正文模拟工具调用，不要声称尚未保存或回测的策略已完成。' :
      '合理安排调用，为最终回答保留预算。工具失败或业务校验 valid=false 时，用户的遇错停止要求优先；仅在未触发停止条件且用户允许修正时依据具体错误修正。不要盲目重复或把预算限制解释为并行工具故障。'].filter(Boolean).join('\n\n')
  return { outgoing, finalizing }
}
