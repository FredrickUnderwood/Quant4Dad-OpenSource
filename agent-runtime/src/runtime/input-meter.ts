import type { GenerateOptions } from '@deepseek-ai/dsh-llm'
import type { TokenUsage } from '@deepseek-ai/dsh-llm'

export type InputMeterContext = {
  provider: string; model: string; protocol: string; modelConfigRevision: string;
  contextWindow: number; maxOutputTokens: number; reasoningEffort?: string
}
export type InputMeter = (request: Omit<GenerateOptions, 'signal'>, context: InputMeterContext) => number

// A provider changing its accounting/template must not silently reuse a bad
// bound on the next step. DSH reports cached and uncached input separately.
export function assertMeasuredUsage(usage: TokenUsage, reserved: number): void {
  const counts = [usage.inputTokens, usage.cacheReadTokens ?? 0, usage.cacheWriteTokens ?? 0]
  if (counts.some(n => !Number.isSafeInteger(n) || n < 0) || counts.reduce((a, b) => a + b, 0) > reserved) {
    throw new Error('agent_input_measurement_unavailable')
  }
}

function freeze(value: any): any {
  if (value && typeof value === 'object') {
    for (const child of Object.values(value)) freeze(child)
    Object.freeze(value)
  }
  return value
}

/** The meter is trusted deployment code, not a Tool or a model-selected plugin.
 * It must bound the serialized provider input, including history, tool schemas,
 * replay state and framing, or throw for an unsupported route/request. No
 * character heuristic or provider usage from an earlier request is a fallback. */
export function measureModelInput(meter: InputMeter, request: GenerateOptions, provider: any, revision: string): number {
  try {
    const { signal: _signal, ...input } = request
    const context: InputMeterContext = { provider: provider.id, model: provider.default_model,
      protocol: provider.agent.protocol, modelConfigRevision: revision,
      contextWindow: provider.agent.context_window, maxOutputTokens: provider.agent.max_output_tokens,
      ...(provider.agent.reasoning_effort === undefined ? {} : { reasoningEffort: provider.agent.reasoning_effort }) }
    const value: unknown = meter(freeze(structuredClone({ ...input, provider: context.provider })), freeze(context))
    // A mistakenly async bundle is unsupported; consume its rejection before
    // failing so it cannot later escape through unhandledRejection.
    if (value instanceof Promise) { void value.catch(() => {}); throw new Error() }
    if (typeof value !== 'number' || !Number.isSafeInteger(value) || value < 1 || value > 100_000_000) throw new Error()
    return value
  } catch { throw new Error('agent_input_measurement_unavailable') }
}
