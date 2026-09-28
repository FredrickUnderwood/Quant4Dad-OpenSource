import type { Context } from '@deepseek-ai/cordis'
import { toolPairingBalancedBefore } from '@deepseek-ai/dsh-compaction'
import { CONTEXT_WINDOW_EXCEEDED_CODE } from '@deepseek-ai/dsh-llm'
import { compactionOutputLimit } from './q4d-run-policy.ts'

export const name = 'q4d-context'
export const inject = ['llm', 'tokenMeter', 'compaction', 'toolResultPruner']
// Q4D owns the budget/cancellation gates; all history replacement and
// summarization still use Harness's public compaction services.
export const compactionConfig = { auto: false, thresholdRatio: 0.6, retainRatio: 0.15,
  maxTokens: 8192, compactionRetries: 0, maxOverflowRetries: 0 }

export function apply(ctx: Context, options: {
  execution: (session: string) => any;
  project: (agent: any, binding: any) => Promise<void>;
  progress: (agent: any, execution: any, stage: string) => Promise<void>;
}) {
  async function compact(agent: any, signal: AbortSignal, overflow: boolean) {
    const execution = options.execution(agent.session.id)
    if (!execution || signal.aborted) return false
    execution.lease.check()
    const remaining = execution.lease.snapshot()
    const before = agent.session.surface.replaceGeneration
    let measurement = ctx.tokenMeter.measure(agent.session)
    const model = await ctx.llm.resolveModelInfo(agent.options.provider!, agent.options.model!, signal)
    const threshold = Math.min(Math.max(8000, Math.min(32000, Math.floor(remaining.input_tokens.remaining / 8))),
      Math.floor((model.context?.contextWindow ?? 1000000) * compactionConfig.thresholdRatio))
    if (!overflow && measurement.totalTokens < threshold) return false
    // A repeated failed summary must not burn the rest of a Run on unchanged history.
    const attempt = `${overflow}:${before}:${measurement.nodes.at(-1)?.seq}`
    if (execution.lastCompactAttempt === attempt) return false
    execution.lastCompactAttempt = attempt
    await options.progress(agent, execution, 'compacting')
    execution.compacting = true
    try {
      // Model-free trimming can make progress even when no summary is affordable.
      ctx.toolResultPruner.pruneSession(agent.session)
      measurement = ctx.tokenMeter.measure(agent.session)
      if (!overflow && measurement.totalTokens < threshold) return agent.session.surface.replaceGeneration > before
      if (compactionOutputLimit(remaining, execution.modelOutputLimit) < 1) return agent.session.surface.replaceGeneration > before
      if (overflow) {
        await ctx.compaction.compactIfNeeded(agent, 'context-overflow', signal)
      } else {
        const nodes = measurement.nodes
        if (nodes.length < 3) return agent.session.surface.replaceGeneration > before
        let keep = nodes.length - 1, retained = nodes[keep]!.tokens
        while (keep > 0 && (retained < 4000 || nodes.length - keep < 2)) retained += nodes[--keep]!.tokens
        while (keep > 0 && !toolPairingBalancedBefore(agent.session, nodes[keep]!.seq)) keep--
        if (keep > 0) await ctx.compaction.compactRegion(nodes[0]!.seq, nodes[keep - 1]!.seq, agent, signal)
        else await ctx.compaction.compactIfNeeded(agent, 'pressure', signal)
      }
    } catch {
      // Never expose raw provider text. A failed summary leaves native history
      // intact; cancellation/revocation still terminates the original Run.
      execution.lease.check()
      await options.progress(agent, execution, 'compaction_failed')
    } finally {
      execution.compacting = false
      await options.project(agent, execution.binding)
    }
    return agent.session.surface.replaceGeneration > before
  }

  ctx.on('agent/pre-step', async ({ agent, signal }, next) => {
    await compact(agent, signal, false)
    return next()
  })
  ctx.on('agent/request-error', async ({ agent, failure, signal }, next) => {
    if (failure.code !== CONTEXT_WINDOW_EXCEEDED_CODE || signal.aborted) return next()
    const execution = options.execution(agent.session.id)
    if (!execution) return next()
    // At most one context recovery per Run, and only after a durable reduction.
    if (!execution.contextRecoveryAttempted) {
      execution.contextRecoveryAttempted = true
      if (await compact(agent, signal, true)) {
        execution.lease.check()
        await options.progress(agent, execution, 'context_recovery')
        return { kind: 'retry' }
      }
    }
    const budget = execution.contextFailure
    if (budget) execution.lease.exceed('context_window', budget.used, budget.limit, budget.requested)
    execution.lease.stop('agent_model_request_failed')
    return next()
  })
}
