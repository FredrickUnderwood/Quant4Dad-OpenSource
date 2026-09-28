import { BlockAssembler, createUserMessage, createAssistantMessage, createToolResultMessage, type GenerateOptions,
  type Message } from '@deepseek-ai/dsh-llm'
import { BootstrapSync } from '../bootstrap/sync.mjs'
import { DshBootstrapApplier } from '../bootstrap/applier.mjs'
import { measureModelInput, assertMeasuredUsage, type InputMeter } from '../runtime/input-meter.ts'

export const probeVersion = 'echo-v1'
export const probeTool = 'q4d_agent_echo_probe'
const sentinel = 'Q4D_ECHO_V1_7F2A'
const finalText = 'Q4D_PROBE_OK'
type ProbeRequest = { provider: string; model: string; model_config_revision: string; probe_version: string; force: boolean }
export type ProbeResult = Omit<ProbeRequest, 'force'> & { status: 'ready' | 'incompatible' | 'unavailable'; reason: string;
  checked_at_ms: number; expires_at_ms: number; context_window: number; max_output_tokens: number;
  context_source: 'explicit-config'; model_turns: number; tool_calls: number; first_event_ms: number }
const fail = (code: string): never => { throw new Error(code) }
// Only closed-vocabulary tags and numeric summaries enter logs, never provider
// messages, tool arguments, error details, or provider-specific finish strings.
const finishTag = (kind: string) => ['stop', 'tool-calls', 'max-tokens', 'aborted', 'error'].includes(kind) ? kind : 'other'
const tokenCount = (value: unknown): number | null => Number.isSafeInteger(value) && (value as number) >= 0 ? value as number : null
type TurnDiagnostic = { turn: number; chunks: number; bytes: number; finish: string;
  input_tokens: number | null; output_tokens: number | null; text_bytes: number; tool_blocks: number; reasoning_blocks: number; other_blocks: number }
type Diagnostic = (row: Record<string, unknown>) => void

/** Fixed, memory-only probe: no user messages, business tools, MCP, Session
 * persistence or Run authority. Both requests still use the applied generation
 * gate. Cache entries cannot outlive that generation or the monotonic TTL. */
export class ModelProbe {
  #sync: BootstrapSync
  #applier: DshBootstrapApplier
  #measureInput: InputMeter
  #cache = new Map<string, { result: ProbeResult; until: number }>()
  #pending = new Map<string, Promise<ProbeResult>>()
  #shutdown = new AbortController()
  #unsubscribe: () => void
  #diagnostic: Diagnostic
  constructor(sync: BootstrapSync, applier: DshBootstrapApplier, measureInput: InputMeter,
    diagnostic: Diagnostic = row => { process.stdout.write(JSON.stringify(row) + '\n') }) {
    this.#sync = sync; this.#applier = applier; this.#measureInput = measureInput; this.#diagnostic = diagnostic
    this.#unsubscribe = sync.subscribeInvalidation(() => this.#cache.clear())
  }
  ready = (provider: string, model: string, revision: string) => {
    const state = this.#sync.status()
    const cached = this.#cache.get(JSON.stringify([provider, model, revision, probeVersion]))
    return !this.#shutdown.signal.aborted && state.configuration_applied === true && state.model_config_revision === revision &&
      cached?.result.status === 'ready' && performance.now() < cached.until && Date.now() < cached.result.expires_at_ms
  }
  async probe(request: ProbeRequest, callerSignal?: AbortSignal): Promise<ProbeResult> {
    if (this.#shutdown.signal.aborted) fail('agent_runtime_unavailable')
    callerSignal?.throwIfAborted()
    const { provider, model, model_config_revision, probe_version, force } = request
    if (probe_version !== probeVersion || typeof force !== 'boolean') fail('agent_invalid_request')
    const snapshot = this.#sync.readConfiguration()
    const entry = snapshot.providers.find((p: any) => p.id === provider && p.default_model === model)
    if (snapshot.model_config_revision !== model_config_revision) fail('agent_configuration_conflict')
    if (!entry) fail('agent_configuration_unavailable')
    const key = JSON.stringify([provider, model, model_config_revision, probe_version])
    const running = this.#pending.get(key)
    if (running) return structuredClone(await running)
    const cached = this.#cache.get(key)
    if (!force && cached && performance.now() < cached.until && Date.now() < cached.result.expires_at_ms) return structuredClone(cached.result)
    if (this.#pending.size >= 4) fail('agent_runtime_unavailable')
    this.#cache.delete(key)
    const check = () => {
      if (this.#shutdown.signal.aborted) fail('agent_runtime_unavailable')
      if (this.#sync.readConfiguration().revision !== snapshot.revision) fail('agent_configuration_unavailable')
      callerSignal?.throwIfAborted()
      return true
    }
    const operation = (async (): Promise<ProbeResult> => {
      const started = performance.now(), signal = AbortSignal.any([this.#shutdown.signal, AbortSignal.timeout(15000), ...(callerSignal ? [callerSignal] : [])])
      let turns = 0, calls = 0, first = 0, observed = false, status: ProbeResult['status'] = 'ready'
      const diagnostics: TurnDiagnostic[] = []
      let diagnosticCheck = 'input_measurement'
      const requireContract = (valid: boolean, name: string) => {
        if (!valid) { diagnosticCheck = name; fail('agent_probe_incompatible') }
      }
      // 128 per turn reserves at most 256 output tokens in total.
      const maxTokens = Math.min(128, entry.agent.max_output_tokens)
      const tools = [{ name: probeTool, description: 'Echo the exact sentinel.', parameters: { type: 'object' as const,
        required: ['sentinel'], properties: { sentinel: { type: 'string' as const, const: sentinel } }, additionalProperties: false } }]
      const messages: Message[] = [createUserMessage({ source: { kind: 'user' }, content: [{ type: 'text',
        text: `Call ${probeTool} exactly once with {"sentinel":"${sentinel}"}. After its result, reply exactly ${finalText}.` }] })]
      const collect = async () => {
        turns++
        const assembler = new BlockAssembler()
        const stats: TurnDiagnostic = { turn: turns, chunks: 0, bytes: 0, finish: 'not_observed',
          input_tokens: null, output_tokens: null, text_bytes: 0, tool_blocks: 0, reasoning_blocks: 0, other_blocks: 0 }
        diagnostics.push(stats)
        diagnosticCheck = 'input_measurement'
        const options: GenerateOptions = { provider, model, messages, tools, maxTokens, signal,
          system: 'Follow this fixed compatibility probe exactly. Do not call any other tool.' }
        const input = measureModelInput(this.#measureInput, options, entry, model_config_revision)
        stats.input_tokens = tokenCount(input)
        requireContract(Number.isSafeInteger(input) && input >= 1 && input + maxTokens <= entry.agent.context_window, 'input_budget')
        diagnosticCheck = 'stream'
        for await (const chunk of this.#applier.stream(options, check)) {
          if (chunk.type === 'usage') {
            stats.output_tokens = tokenCount(chunk.usage.outputTokens)
            diagnosticCheck = 'usage_measurement'
            assertMeasuredUsage(chunk.usage, input)
            diagnosticCheck = 'stream'
          }
          if (chunk.type === 'finish') stats.finish = finishTag(chunk.reason.kind)
          signal.throwIfAborted()
          if (!observed) { first = Math.min(15000, Math.max(0, Math.round(performance.now() - started))); observed = true }
          stats.bytes += Buffer.byteLength(JSON.stringify(chunk)); stats.chunks++
          requireContract(stats.bytes <= 65536, 'stream_bytes')
          requireContract(stats.chunks <= 2048, 'stream_chunks')
          if (chunk.type === 'usage') requireContract(!((chunk.usage.outputTokens ?? 0) > maxTokens), 'output_budget')
          assembler.push(chunk)
        }
        signal.throwIfAborted()
        const content = assembler.message().content
        stats.finish = finishTag(assembler.finish.kind)
        stats.text_bytes = content.filter(block => block.type === 'text').reduce((total, block) => total + Buffer.byteLength(block.text), 0)
        stats.tool_blocks = content.filter(block => block.type === 'tool-call').length
        stats.reasoning_blocks = content.filter(block => block.type === 'reasoning').length
        stats.other_blocks = content.filter(block => block.type !== 'text' && block.type !== 'reasoning' && block.type !== 'tool-call').length
        return { content, finish: assembler.finish }
      }
      try {
        const firstTurn = await collect()
        const content = firstTurn.content
        const toolCalls = content.filter(block => block.type === 'tool-call')
        requireContract(firstTurn.finish.kind === 'tool-calls', 'first_finish')
        requireContract(toolCalls.length === 1, 'first_tool_count')
        requireContract(toolCalls[0]!.name === probeTool, 'first_tool_name')
        const call = toolCalls[0]!
        // Validate the public DSH argument shape after provider normalization.
        // No other model-provided argument is executed.
        requireContract(/^\s*\{\s*"sentinel"\s*:\s*"Q4D_ECHO_V1_7F2A"\s*\}\s*$/.test(call.arguments), 'first_tool_arguments')
        calls = 1
        messages.push(createAssistantMessage({ content, source: { provider, model } }),
          createToolResultMessage({ callId: call.id, isError: false, content: [{ type: 'text', text: sentinel }] }))
        const second = await collect(), blocks = second.content
        requireContract(second.finish.kind === 'stop', 'second_finish')
        // pi-ai thinking is normalized to DSH reasoning before assembly.
        // Compare canonical discriminants so TypeScript checks this boundary.
        requireContract(blocks.every(block => block.type === 'text' || block.type === 'reasoning'), 'second_block_types')
        requireContract(blocks.filter(block => block.type === 'text').map(block => block.text).join('').trim() === finalText, 'second_final_text')
        diagnosticCheck = 'passed'
      } catch (error) {
        check() // stale/cancelled work never becomes a cache entry
        status = error instanceof Error && error.message === 'agent_probe_incompatible' ? 'incompatible' : 'unavailable'
      }
      check()
      const checked_at_ms = Date.now(), ttl = status === 'unavailable' ? 300000 : 86400000
      const result: ProbeResult = { provider, model, model_config_revision, probe_version, status,
        reason: status === 'ready' ? 'probe_passed' : status === 'incompatible' ? 'probe_contract_mismatch' : 'probe_request_failed',
        checked_at_ms, expires_at_ms: checked_at_ms + ttl, context_window: entry.agent.context_window,
        max_output_tokens: entry.agent.max_output_tokens, context_source: 'explicit-config', model_turns: turns,
        tool_calls: calls, first_event_ms: first }
      this.#cache.set(key, { result, until: performance.now() + ttl })
      // One row per actual execution; cached results and pending callers do not
      // create duplicate observations. Logging must not change readiness.
      try { this.#diagnostic({ event: 'agent_model_probe_completed', probe_version,
        model_config_revision, checked_at_ms, status, stage: turns === 1 ? 'first_turn' : 'second_turn',
        check: diagnosticCheck, elapsed_ms: Math.max(0, Math.round(performance.now() - started)),
        model_turns: turns, tool_calls: calls, turns: diagnostics }) } catch { /* diagnostics cannot change probe behavior */ }
      return result
    })().finally(() => this.#pending.delete(key))
    this.#pending.set(key, operation)
    return structuredClone(await operation)
  }
  close() { this.#shutdown.abort(); this.#unsubscribe(); this.#cache.clear() }
}
