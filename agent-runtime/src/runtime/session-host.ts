import { Context } from '@deepseek-ai/cordis'
import { brandString } from '@deepseek-ai/dsh-brand'
import LlmRuntime, { LlmAdapter, CONTEXT_WINDOW_EXCEEDED_CODE, createUserMessage, freezeMessage, type MessageId, type GenerateOptions,
  type LlmResolvedModelInfo, type StreamChunk } from '@deepseek-ai/dsh-llm'
import SessionStore, { SESSION_FORMAT_VERSION, SessionLogOffset, isAppendSurfaceEvent, deriveEventMessage,
  type SessionId } from '@deepseek-ai/dsh-session'
import { SessionPersistenceNotFoundError } from '@deepseek-ai/dsh-session-persistence'
import SystemPrompt from '@deepseek-ai/dsh-system-prompt'
import ToolRuntime from '@deepseek-ai/dsh-tools'
import AgentRegistry, { type Agent, type AgentHandle } from '@deepseek-ai/dsh-agent'
import AgentLoop from '@deepseek-ai/dsh-agent-loop'
import SessionProjectionRegistry from '@deepseek-ai/dsh-session-projection'
import JsonlSessionPersistence from '@deepseek-ai/dsh-session-persistence-jsonl'
import TokenMeter from '@deepseek-ai/dsh-token-meter'
import BasicCompaction from '@deepseek-ai/dsh-compaction-basic'
import ToolResultPruner from '@deepseek-ai/dsh-compaction-tool-result-pruner'
import * as Q4dRunPolicy from '../plugins/q4d-run-policy.ts'
import * as Q4dContext from '../plugins/q4d-context.ts'
import * as Q4dDomain from '../plugins/q4d-domain.ts'
import { join } from 'node:path'
import { createHash } from 'node:crypto'
import { lstat, realpath } from 'node:fs/promises'
import { RequestIndex } from '../persistence/request-index.mjs'
import { SessionBindings, validSessionCreate } from '../persistence/session-bindings.mjs'
import { DurableLog } from '../persistence/durable-log.mjs'
import { EventJournal } from '../persistence/event-journal.mjs'
import { SessionLifecycle, sessionPreset } from '../bridge/session-lifecycle.mjs'
import { projectRun } from '../bridge/run-projection.mjs'
import { projectUsage, terminalData, projectRejectedTool } from '../bridge/event-projection.mjs'
import { TranscriptError } from '../bridge/transcript.mjs'
import { createBridgeHandler } from '../bridge/http-handler.mjs'
import { BootstrapSync } from '../bootstrap/sync.mjs'
import { DshBootstrapApplier } from '../bootstrap/applier.mjs'
import { RunSupervisor } from './run-supervisor.mjs'
import { TrustedGateway } from '../mcp/trusted-gateway.mjs'
import { installResearchTools, toolPrefix, type ResearchCatalog } from './research-tools.ts'
import { isDeepStrictEqual } from 'node:util'
import { measureModelInput, assertMeasuredUsage, type InputMeter } from './input-meter.ts'
import { buildAnswerEvidence, evidencePrompt } from './answer-evidence.mjs'
import { strategyReviewSources } from './strategy-evidence.mjs'
import { reviewStrategyAnswer, reviewSystem, streamText } from './strategy-answer.mjs'

import { profilesAgree, profileIdentity } from './profile-agreement.mjs'

type Profile = { id: string; revision: string; systemPrompt: string; promptBundleDigest: string;
  skillsDigest: string; toolCatalogRevision: string }
type Manifest = { q4d_version: string; agent_image_digest: string; agent_runtime_version: string;
  adapter_version: string; dsh_version: string; bridge_protocol: 1 }
type Options = { directory: string; bridgeToken: string; sync: BootstrapSync; applier: DshBootstrapApplier;
  probe?: (request: any, signal?: AbortSignal) => Promise<unknown>;
  prepareRun?: (request: any) => Promise<void>;
  releaseRun?: (runID: string) => void;
  profile?: Profile; profiles?: Profile[]; manifest: Manifest; requireProfileAgreement?: boolean; profileSource?: string;
  // Trusted probe/tokenizer policy. Return true only for a currently usable
  // model; measureInput must provide a conservative bound including framing.
  modelReady: (provider: string, model: string, revision: string) => boolean;
  measureInput: InputMeter }

/** Durable Bridge host. A real public AgentLoop drives the gated pi-ai backend
 * and Profile Gateway. Compaction uses the same Run lease and model route.
 * Callers supply current policy/probe/tokenizer facts, never browser options. */
export async function createSessionHost(options: Options) {
  const { directory, bridgeToken, sync, applier, modelReady, measureInput } = options
  const configuredProfiles = structuredClone(options.profiles ?? [options.profile])
  const manifest = structuredClone(options.manifest)
  const snapshotCatalogs = structuredClone(sync.readConfiguration().tool_catalogs ?? (sync.readConfiguration().tool_catalog ? [sync.readConfiguration().tool_catalog] : []))
  const catalogs = new Map<string, ResearchCatalog>(snapshotCatalogs.map((catalog: ResearchCatalog) => [catalog.profile, catalog]))
  const profiles = new Map<string, Profile>()
  const allTools = new Map<string, ResearchCatalog['tools'][number]>()
  for (const profile of configuredProfiles) {
    if (!profile?.id || profiles.has(profile.id) || typeof profile.systemPrompt !== 'string') throw new Error('agent_runtime_configuration_invalid')
    const scoped = catalogs.get(profile.id)
    if (scoped && !profile.toolCatalogRevision) profile.toolCatalogRevision = scoped.revision
    if (scoped && profile.toolCatalogRevision !== scoped.revision) throw new Error('agent_runtime_configuration_invalid')
    profiles.set(profile.id, profile)
    for (const tool of scoped?.tools ?? []) {
      if (allTools.has(tool.name) && !isDeepStrictEqual(allTools.get(tool.name), tool)) throw new Error('agent_runtime_configuration_invalid')
      allTools.set(tool.name, tool)
    }
  }
  const profileAgreement = () => {
    try { return profilesAgree([...profiles.values()], sync.readConfiguration(), options.requireProfileAgreement) }
    catch { return false }
  }
  const catalog: ResearchCatalog | undefined = allTools.size ? { profile: 'internal', revision: '', tools: [...allTools.values()] } : undefined
  let registerTools: ReturnType<typeof installResearchTools> | undefined
  const manifestKeys = ['q4d_version', 'agent_image_digest', 'agent_runtime_version', 'adapter_version', 'dsh_version', 'bridge_protocol']
  if (typeof modelReady !== 'function' || typeof measureInput !== 'function' || !profiles.size || profiles.size > 128 ||
      manifest?.bridge_protocol !== 1 || manifest.dsh_version !== '0.1.2-alpha.5' ||
      Object.keys(manifest).sort().join(',') !== manifestKeys.sort().join(',') ||
      !/^[!-~]{32,8192}$/.test(bridgeToken)) throw new Error('agent_runtime_configuration_invalid')
  const root = await lstat(directory)
  if (!root.isDirectory() || root.isSymbolicLink() || root.uid !== process.getuid?.() || (root.mode & 0o777) !== 0o700 ||
      await realpath(directory) !== directory) throw new Error('agent_runtime_configuration_invalid')
  const ctx = new Context()
  let index!: RequestIndex, bindings!: SessionBindings, stops!: DurableLog
  try {
    index = new RequestIndex(join(directory, 'request-index.jsonl'))
    bindings = new SessionBindings(join(directory, 'session-bindings.jsonl'))
    stops = new DurableLog(join(directory, 'run-stops.jsonl'))
  } catch { index?.close(); bindings?.close(); throw new Error('agent_runtime_unavailable') }
  const causes = new Map<string, string>(stops.records.map((row: any) => [row.run_id, row.code]))
  const budgetStops = new Map<string, any>(stops.records.filter((row: any) => row.budget).map((row: any) => [row.run_id, row.budget]))
  const handles = new Map<string, AgentHandle>(), journals = new Map<string, EventJournal>()
  const modelRoutes = new Map<string, () => void>()
  type Execution = { binding: any; lease: any; outputLimit: number; modelOutputLimit: number; compacting?: boolean;
    outputRecoveries?: number; pendingOutputRecovery?: boolean; retryOutputLimit?: number;
    contextFailure?: { used: number; limit: number; requested: number }; progressSequence?: number;
    finalizing?: boolean; gateway?: TrustedGateway; completion?: Promise<void> }
  const active = new Map<string, Execution>()
  const claimedSources = new Set<string>()
  const pending = new Set<Promise<unknown>>()
  function track<T>(operation: () => Promise<T>): Promise<T> {
    const promise = Promise.resolve().then(operation).finally(() => pending.delete(promise))
    pending.add(promise)
    return promise
  }
  let closed = false, fatal = false, draining = false
  const supervisor = new RunSupervisor({ currentStatus: () => sync.status() })
  const unsubscribe = sync.subscribeInvalidation(() => supervisor.recheck())
  const provenance = { bridge_protocol: manifest.bridge_protocol, adapter_version: manifest.adapter_version,
    dsh_version: manifest.dsh_version, backend: 'cordis', session_format: SESSION_FORMAT_VERSION,
    event_journal_format: 1, session_binding_format: 1 }
  function available() { if (closed || fatal) throw new Error('agent_runtime_unavailable') }
  function journal(binding: any) {
    let result = journals.get(binding.run_id)
    if (!result) {
      result = new EventJournal(join(directory, `events-${binding.run_id}.jsonl`), binding.session_id, binding.run_id)
      journals.set(binding.run_id, result)
    }
    return result
  }
  function inserted(agent: Agent, messageId: string) {
    return agent.session.snapshotEvents().find(event => event.type === 'agent/inbox/spliced' &&
      event.data.inserted.some(message => message.id === messageId))
  }
  function currentModel(provider: string, model: string) {
    available()
    const snapshot = sync.readConfiguration()
    if (!isDeepStrictEqual(snapshot.tool_catalogs ?? (snapshot.tool_catalog ? [snapshot.tool_catalog] : []), snapshotCatalogs)) throw new Error('agent_configuration_unavailable')
    const found = snapshot.providers.find((p: any) => p.id === provider && p.default_model === model)
    if (!found || modelReady(provider, model, snapshot.model_config_revision) !== true) throw new Error('agent_configuration_unavailable')
    return found
  }
  function resolveSession(request: any, creating: boolean) {
    currentModel(request.provider, request.model)
    const profile = profiles.get(request.profile)
    if (!profile || (creating && (request.profile_revision !== profile.revision ||
        request.model_config_revision !== sync.status().model_config_revision))) throw new Error('agent_configuration_conflict')
    return { provider: request.provider, model: request.model, profile: request.profile }
  }
  function recordStop(runId: string, code: string, budget?: any) {
    if (causes.has(runId)) return
    try { stops.append({ run_id: runId, code, ...(budget ? { budget } : {}) }); causes.set(runId, code); if (budget) budgetStops.set(runId, budget) }
    catch { fatal = true; throw new Error('agent_runtime_unavailable') }
    try { process.stdout.write(JSON.stringify({ event: 'agent_run_stopped', run_id: runId, code,
      ...(budget ? { budget } : {}) }) + '\n') } catch { /* durable stop authority does not depend on log output */ }
  }
  function failedData(cause: string, runId: string) {
    const code = ['agent_run_budget_exceeded', 'agent_model_output_limit'].includes(cause) ? 'agent_model_limit'
      : cause === 'agent_model_request_failed' ? 'agent_model_error' : 'agent_run_blocked'
    return { code, retryable: false, ...(budgetStops.has(runId) ? { budget: budgetStops.get(runId) } : {}) }
  }
  async function project(agent: Agent, binding: any, recover = false) {
    const events = agent.session.snapshotEvents()
    await ctx.sessions.flush(agent.session)
    const target = journal(binding)
    if (target.terminal) return
    const insertion = events.find(event => event.type === 'agent/inbox/spliced' &&
      event.data.inserted.some(message => message.id === binding.message_id))
    if (!insertion) return
    target.append(`inbox:${insertion.seq}`, { type: 'run.started', occurred_at: new Date(insertion.time).toISOString(),
      data: { message_id: binding.message_id, execution_envelope_digest: binding.execution_envelope_digest } })
    const user = events.findIndex(event => event.type === 'user/message' && event.data.id === binding.message_id)
    const start = user < 0 ? undefined : events.slice(0, user).findLast(event => event.type === 'turn/start')
    if (catalog && start) {
      for (const event of events) if (event.type === 'tool/result' && event.data.turn === start.data.turn && isAppendSurfaceEvent(event)) {
        projectRejectedTool(target, event, events, binding.run_id)
      }
      if (recover) {
        const facts = target.snapshot()
        for (const proposal of facts.filter((event: any) => event.type === 'tool.proposed')) {
          const id = proposal.data.tool_call_id
          if (facts.some((event: any) => ['tool.completed', 'tool.failed'].includes(event.type) && event.data.tool_call_id === id)) continue
          target.append(`tool:${id}:tool.failed`, { type: 'tool.failed', occurred_at: new Date().toISOString(),
            data: { tool_call_id: id, code: 'agent_tool_outcome_unknown' } })
        }
      }
    }
    let requestUsage: any
    for (const event of events) {
      if (!start || (event.type === 'compaction/prune' ? event.seq <= start.seq
        : !('turn' in event.data) || event.data.turn !== start.data.turn)) continue
      const occurred_at = new Date(event.time).toISOString()
      if (event.type === 'assistant/chunk') {
        const chunk = event.data.chunk
        if (chunk.type === 'usage') requestUsage = chunk.usage
        if (chunk.type === 'finish') {
          if (chunk.reason.kind === 'error' && requestUsage) {
            target.append(`failed-usage:${event.seq}`, { type: 'usage.updated', occurred_at,
              data: { message_id: 'failed-request-' + event.seq, usage: projectUsage(requestUsage) } })
          }
          // request/header changes only with configuration; it cannot delimit
          // calls. A local preflight failure must not borrow earlier usage.
          requestUsage = undefined
        }
      }
      if (event.type === 'assistant/message' && isAppendSurfaceEvent(event) && !event.data.interrupted) {
        const text = event.data.message.content.filter(block => block.type === 'text').map(block => block.text).join('')
        if (text) {
          target.append(`delta:${event.seq}`, { type: 'message.delta', occurred_at, data: { message_id: event.data.message.id, text } })
          target.append(`message:${event.seq}`, { type: 'message.completed', occurred_at, data: { message_id: event.data.message.id, text } })
        }
        if (event.data.usage) target.append(`usage:${event.seq}`, { type: 'usage.updated', occurred_at,
          data: { message_id: event.data.message.id, usage: projectUsage(event.data.usage) } })
      } else if (event.type === 'compaction/prune') {
        target.append(`prune:${event.seq}`, { type: 'context.compacted', occurred_at, data: { source_seq: String(event.seq) } })
      } else if (event.type === 'compaction/end' && !event.data.error) {
        target.append(`compaction:${event.seq}`, { type: 'context.compacted', occurred_at, data: { source_seq: String(event.seq) } })
        const summary = events.find(e => e.type === 'compaction/summary' && e.data.compactionId === event.data.compactionId)
        if (summary?.type === 'compaction/summary' && summary.data.usage) target.append(`compaction-usage:${summary.seq}`, {
          type: 'usage.updated', occurred_at, data: { message_id: 'compaction-' + summary.seq, usage: projectUsage(summary.data.usage) } })
      } else if (event.type === 'turn/end') {
        const cause = causes.get(binding.run_id)
        const state = cause === 'user' ? 'cancelled' : cause === 'agent_runtime_interrupted' ? 'interrupted'
          : cause ? 'failed' : event.data.reason.kind === 'completed' ? 'completed'
            : event.data.reason.kind === 'interrupted' || (recover && event.data.reason.kind === 'aborted') ? 'interrupted' : 'failed'
        target.append(`terminal:${event.seq}`, { type: `run.${state}`, occurred_at,
          data: cause && state === 'failed' ? failedData(cause, binding.run_id) : terminalData(state, event.data.reason) })
        return
      }
    }
    if (recover) {
      const cause = causes.get(binding.run_id)
      const state = cause === 'user' ? 'cancelled' : cause && cause !== 'agent_runtime_interrupted' ? 'failed' : 'interrupted'
      target.append(`interrupted:${insertion.seq}`, { type: `run.${state}`, occurred_at: new Date(insertion.time).toISOString(),
        data: state === 'failed' ? failedData(cause!, binding.run_id) : terminalData(state) })
    }
  }
  async function progress(agent: Agent, execution: Execution, stage: string) {
    await project(agent, execution.binding)
    execution.progressSequence = (execution.progressSequence ?? 0) + 1
    journal(execution.binding).append(`progress:${execution.progressSequence}`, {
      type: 'run.progress', occurred_at: new Date().toISOString(), data: { stage } })
  }
  async function load(binding: any, config: any, resume: boolean, recoveryOnly = false) {
    const profile = profiles.get(binding.request.profile)
    if (!profile) throw new Error('agent_configuration_conflict')
    const localCatalog = catalogs.get(profile.id)
    const toolNames = localCatalog?.tools.map(tool => tool.name) ?? []
    const id = binding.request.q4d_session_id
    // The public loop resolves LLM on its factory context, not setup(agentCtx).
    // Give each Session a stable route on that registry and verify sessionId
    // again at dispatch. No per-agent replacement of the shared LLM service.
    const route = 'q4d-session-' + createHash('sha256').update(id).digest('hex').slice(0, 32)
    class SessionModel extends LlmAdapter {
      override async listModels() { return [{ provider: route, id: config.model, name: config.model, inputModalities: ['text' as const] }] }
      override async resolveModel(provider: string, model: string): Promise<LlmResolvedModelInfo> {
        if (provider !== route || model !== config.model) throw new Error('agent_capability_rejected')
        if (recoveryOnly) return { provider, id: model, name: model, inputModalities: ['text'] }
        const value = currentModel(config.provider, model)
        return { provider, id: model, name: model, inputModalities: ['text'], context: { contextWindow: value.agent.context_window },
          defaultMaxTokens: Math.min(value.agent.max_output_tokens, active.get(id)?.outputLimit ?? value.agent.max_output_tokens) }
      }
      async *stream(request: GenerateOptions): AsyncIterable<StreamChunk> {
        if (recoveryOnly) throw new Error('agent_runtime_interrupted')
        const execution = active.get(id)
        if (!execution) throw new Error('agent_capability_rejected')
        const { lease } = execution
        const compacting = request.purpose === 'compaction' && execution.compacting === true
        try {
          if (request.provider !== route || request.model !== config.model || request.sessionId !== id || (request.purpose && !compacting) ||
              (request.tools ?? []).some(tool => !toolNames.includes(tool.name.slice(toolPrefix.length)) || !tool.name.startsWith(toolPrefix))) throw new Error('agent_capability_rejected')
          const provider = currentModel(config.provider, config.model)
          // Summaries carry conversation content but never advertise executable
          // Tools. The same exact outgoing request is metered before dispatch.
          const { outgoing, finalizing } = compacting ? { outgoing: { ...request }, finalizing: false }
            : Q4dRunPolicy.prepareBudgetedRequest(request, lease, provider.agent.max_output_tokens, execution.retryOutputLimit)
          let evidence: any, userRequest: any, sources: any[] = []
          const guardAnswer = !compacting && profile!.id === 'strategy_lab'
          if (compacting) {
            delete outgoing.tools
            outgoing.maxTokens = Math.min(request.maxTokens ?? 8192,
              Q4dRunPolicy.compactionOutputLimit(lease.snapshot(), provider.agent.max_output_tokens))
          } else {
            const agent = handles.get(id)!.agent
            const insertedMessage = inserted(agent, execution.binding.message_id)!
            const boundary = insertedMessage.seq
            if (insertedMessage.type === 'agent/inbox/spliced') userRequest = insertedMessage.data.inserted.find(message =>
              message.id === execution.binding.message_id)?.content
            evidence = buildAnswerEvidence(agent.session.snapshotEvents(), boundary, isAppendSurfaceEvent)
            if (guardAnswer) sources = strategyReviewSources(agent.session.snapshotEvents(), boundary, isAppendSurfaceEvent)
            const text = evidencePrompt(evidence)
            if (text) outgoing.system = [outgoing.system, text].filter(Boolean).join('\n\n')
          }
          const inputTokens = measureModelInput(measureInput, outgoing, provider, sync.status().model_config_revision)
          const requested = Math.min(provider.agent.max_output_tokens, outgoing.maxTokens ?? execution.outputLimit)
          if (compacting && (requested < 1 || inputTokens * 2 > lease.snapshot().input_tokens.remaining)) throw new Error('agent_compaction_budget_unavailable')
          if (inputTokens + requested > provider.agent.context_window) {
            if (!compacting) execution.contextFailure = { used: inputTokens, limit: provider.agent.context_window, requested }
            yield { type: 'finish', reason: { kind: 'error', failure: {
              code: CONTEXT_WINDOW_EXCEEDED_CODE, message: 'agent_context_window_exceeded' } } }
            return
          }
          if (finalizing && !execution.finalizing) {
            execution.finalizing = true
            await progress(handles.get(id)!.agent, execution, 'finalizing')
          }
          const reservation = lease.reserveModel(inputTokens, requested)
          const maxTokens = reservation.maxTokens
          process.stdout.write(JSON.stringify({ event: 'agent_model_budget', run_id: execution.binding.run_id,
            purpose: compacting ? 'compaction' : 'agent', input_bound: inputTokens, output_limit: maxTokens,
            output_remaining: lease.snapshot().output_tokens.remaining, finalizing }) + '\n')
          const signal = AbortSignal.any([lease.signal, ...(request.signal ? [request.signal] : [])])
          let usage: any, generated = false, visible = false, toolCall = false
          const buffered: StreamChunk[] = []
          for await (const chunk of applier.stream({ ...outgoing, provider: config.provider, maxTokens, signal }, () => {
            currentModel(config.provider, config.model); return lease.check()
          })) {
            if (chunk.type === 'usage') { assertMeasuredUsage(chunk.usage, inputTokens); usage = chunk.usage }
            if (chunk.type === 'block-start' || chunk.type.endsWith('-delta') || chunk.type === 'block-end') generated = true
            if ((chunk.type === 'text-delta' && chunk.text.trim()) ||
                (chunk.type === 'block-end' && chunk.block.type === 'text' && chunk.block.text.trim())) visible = true
            if (chunk.type === 'block-start' && chunk.blockType === 'tool-call') toolCall = true
            if (chunk.type === 'finish' && chunk.reason.kind === 'error' && chunk.reason.failure.code === CONTEXT_WINDOW_EXCEEDED_CODE) {
              if (!generated && (!usage || usage.outputTokens === 0)) reservation.rejectBeforeOutput()
              if (!compacting) execution.contextFailure = { used: inputTokens, limit: provider.agent.context_window, requested: maxTokens }
            }
            if (chunk.type === 'finish' && ['stop', 'tool-calls', 'max-tokens'].includes(chunk.reason.kind)) {
              if (usage) reservation.settle(usage)
              if (!compacting && chunk.reason.kind === 'stop' && !visible && !toolCall) {
                // A reasoning-only response is not an answer or a Tool action.
                recordStop(execution.binding.run_id, 'agent_model_request_failed')
                yield { type: 'finish', reason: { kind: 'error', failure: {
                  code: 'EMPTY_RESPONSE', message: 'agent_model_empty_response' } } }
                return
              }
              if (compacting && chunk.reason.kind === 'max-tokens' && usage) {
                const agent = handles.get(id)!.agent
                const start = agent.session.snapshotEvents().findLast(e => e.type === 'compaction/start')!
                await project(agent, execution.binding)
                journal(execution.binding).append(`compaction-failed-usage:${start.seq}`, {
                  type: 'usage.updated', occurred_at: new Date(start.time).toISOString(),
                  data: { message_id: 'failed-compaction-' + start.seq, usage: projectUsage(usage) } })
              }
              if (!compacting && chunk.reason.kind === 'max-tokens') {
                if (!finalizing && Q4dRunPolicy.canRecoverOutput(execution, maxTokens)) {
                  execution.outputRecoveries = (execution.outputRecoveries ?? 0) + 1
                  execution.pendingOutputRecovery = true
                  execution.retryOutputLimit = Math.min(provider.agent.max_output_tokens, maxTokens * 2)
                  // Harness retries the request from its last complete surface.
                  // No truncated tool arguments enter history or dispatch.
                  yield { type: 'finish', reason: { kind: 'error', failure: {
                    code: Q4dRunPolicy.OUTPUT_RECOVERY_CODE, message: 'agent_model_output_recovery' } } }
                  return
                }
                recordStop(execution.binding.run_id, 'agent_model_output_limit', { dimension: 'output_per_call', used: usage?.outputTokens ?? maxTokens, limit: maxTokens, requested: maxTokens })
              } else if (!compacting) delete execution.retryOutputLimit
            }
            if ((compacting || finalizing || !localCatalog) && ((chunk.type === 'block-start' && chunk.blockType === 'tool-call') || chunk.type === 'tool-call-delta' ||
                (chunk.type === 'block-end' && chunk.block.type === 'tool-call') ||
                (chunk.type === 'finish' && chunk.reason.kind === 'tool-calls'))) throw new Error('agent_capability_rejected')
            if (guardAnswer) buffered.push(chunk)
            else yield chunk
          }
          if (guardAnswer) {
            const reviewed = await reviewStrategyAnswer(buffered, evidence, async (candidate: string) => {
              const budget = lease.snapshot()
              // Leave a model turn and output room for the next business step
              // when this response carries tool calls. Final answers need none.
              const reserve = toolCall ? 1 : 0
              const output = Math.min(2048, provider.agent.max_output_tokens, budget.output_tokens.remaining - (toolCall ? 512 : 0))
              const payload = JSON.stringify({ user_request: userRequest,
                facts: evidence, current_run_sources: sources, candidate })
              if (budget.model_calls.remaining <= reserve || output < 128 || Buffer.byteLength(payload) > 196608) return ''
              const check: GenerateOptions = { provider: config.provider, model: config.model, sessionId: request.sessionId!,
                system: reviewSystem, messages: [createUserMessage({ content: [{ type: 'text', text: payload }], source: { kind: 'user' } })],
                maxTokens: output, signal: AbortSignal.any([signal, AbortSignal.timeout(30000)]) }
              const measured = measureModelInput(measureInput, check, provider, sync.status().model_config_revision)
              if (measured > budget.input_tokens.remaining || measured + output > provider.agent.context_window) return ''
              const reviewReservation = lease.reserveModel(measured, output)
              const reviewChunks: StreamChunk[] = []
              let reviewUsage: any
              try {
                for await (const chunk of applier.stream({ ...check, maxTokens: reviewReservation.maxTokens }, () => {
                  currentModel(config.provider, config.model); return lease.check()
                })) {
                  if (chunk.type === 'usage') { assertMeasuredUsage(chunk.usage, measured); reviewUsage = chunk.usage }
                  reviewChunks.push(chunk)
                }
              } catch {
                // Revocation/cancellation remains terminal; provider failure
                // otherwise falls back to deterministic facts, never retries.
                lease.check()
                currentModel(config.provider, config.model)
                return ''
              } finally {
                if (reviewUsage) {
                  reviewReservation.settle(reviewUsage)
                  await project(handles.get(id)!.agent, execution.binding)
                  journal(execution.binding).append(`answer-review-usage:${lease.snapshot().model_calls.used}`, {
                    type: 'usage.updated', occurred_at: new Date().toISOString(), data: {
                      message_id: 'answer-review-' + lease.snapshot().model_calls.used, usage: projectUsage(reviewUsage) } })
                }
              }
              const finish = reviewChunks.at(-1)
              if (finish?.type !== 'finish' || finish.reason.kind !== 'stop' || reviewChunks.some(chunk =>
                chunk.type === 'tool-call-delta' || chunk.type === 'block-start' && chunk.blockType === 'tool-call' ||
                chunk.type === 'block-end' && chunk.block.type === 'tool-call')) return ''
              return streamText(reviewChunks)
            }).catch(async (error: unknown) => {
              // Cancellation can discard the buffered candidate before native
              // history sees its usage. Preserve that charge exactly once.
              if (usage) {
                await project(handles.get(id)!.agent, execution.binding)
                journal(execution.binding).append(`answer-candidate-failed-usage:${lease.snapshot().model_calls.used}`, {
                  type: 'usage.updated', occurred_at: new Date().toISOString(), data: {
                    message_id: 'failed-answer-' + lease.snapshot().model_calls.used, usage: projectUsage(usage) } })
              }
              throw error
            })
            lease.check()
            process.stdout.write(JSON.stringify({ event: 'agent_answer_review', run_id: execution.binding.run_id,
              verdict: reviewed.verdict, source_count: sources.length }) + '\n')
            for (const chunk of reviewed.chunks) yield chunk as StreamChunk
          }
        } catch (error) {
          if (compacting && error instanceof Error && ['agent_compaction_budget_unavailable', 'agent_model_request_failed'].includes(error.message)) {
            lease.check()
            throw new Error('agent_compaction_failed')
          }
          lease.stop(error instanceof Error ? error.message : 'agent_model_request_failed')
          throw new Error('agent_model_request_failed')
        }
      }
    }
    const unregister = ctx.llm.registerAdapter([route], new SessionModel())
    const common = { agentOptions: { provider: route, model: config.model }, setup: async (agentCtx: Context) => {
      agentCtx.systemPrompt.section({ name: 'deployment:persona', order: 0, text: profile.systemPrompt })
      registerTools?.(agentCtx, profile.id)
      await agentCtx.plugin(Q4dDomain, { profile: profile.id })
    } }
    let handle: AgentHandle
    try {
      handle = resume ? await ctx.agents.resume({ ...common, resumeSessionId: brandString<SessionId>(id) })
        : await ctx.agents.create({ ...common, sessionId: brandString<SessionId>(id), meta: { cwd: directory, agentPreset: sessionPreset(binding) } })
    } catch (error) { unregister(); throw error }
    modelRoutes.set(id, unregister)
    handles.set(id, handle)
    try {
      if (resume) { handle.agent.cancel({ kind: 'user' }); await handle.agent.whenIdle() }
      await ctx.sessionPersistence.ensureMaterialized(handle.agent.session)
      await ctx.sessions.flush(handle.agent.session)
      if (resume) for (const prior of index.list().filter((row: any) => row.session_id === id)) await project(handle.agent, prior, true)
    } catch {
      fatal = true; supervisor.close()
      await handle.dispose().catch(() => {})
      handles.delete(id); modelRoutes.delete(id); unregister()
      throw new Error('agent_runtime_unavailable')
    }
  }
  async function readStored(id: string, signal?: AbortSignal) {
    try { return await ctx.sessionPersistence.readFrom(brandString<SessionId>(id), SessionLogOffset(0), signal) }
    catch (error) { if (error instanceof SessionPersistenceNotFoundError) return undefined; throw error }
  }
  const lifecycle = new SessionLifecycle({ bindings, provenance, backend: {
    resolve: resolveSession, liveHeader: (id: string) => handles.get(id)?.agent.session.header,
    readHeader: async (id: string) => (await readStored(id))?.meta,
    materialize: (binding: any, config: any) => load(binding, config, false),
    resume: (binding: any, config: any) => load(binding, config, true),
    isBusy: (id: string) => active.has(id),
    unload: async (id: string) => {
      const handle = handles.get(id)!
      await ctx.sessions.flush(handle.agent.session); await handle.dispose(); handles.delete(id)
      modelRoutes.get(id)?.(); modelRoutes.delete(id)
    },
  } })
  function getRun(runId: string) {
    const binding = index.get(runId)
    if (!binding) throw new Error('agent_run_not_found')
    return projectRun(binding, journal(binding), { active: active.get(binding.session_id)?.binding.run_id === runId,
      cancelling: causes.get(runId) === 'user' })
  }
  async function reconcileRun(runId: string) {
    available()
    const request = index.get(runId)
    if (!request) throw new Error('agent_run_not_found')
    return lifecycle.serialized(request.session_id, async () => {
      available()
      const state = getRun(runId)
      if (state.terminal || active.has(request.session_id)) return state
      const binding = bindings.get(request.session_id)
      const stored = await readStored(request.session_id)
      if (!binding?.durable || !stored || stored.meta.id !== request.session_id || stored.meta.agentPreset !== sessionPreset(binding)) {
        throw new Error('agent_session_unbound')
      }
      // Recovery has no execution lease and uses an adapter that cannot stream.
      // Public resume supplies native closers even after provider revoke.
      recordStop(runId, causes.get(runId) ?? 'agent_runtime_interrupted')
      if (!handles.has(request.session_id)) {
        await load(binding, { provider: binding.request.provider, model: binding.request.model }, true, true)
      } else await project(handles.get(request.session_id)!.agent, request, true)
      const handle = handles.get(request.session_id)!
      await ctx.sessions.flush(handle.agent.session)
      await handle.dispose()
      handles.delete(request.session_id); modelRoutes.get(request.session_id)?.(); modelRoutes.delete(request.session_id)
      return getRun(runId)
    })
  }
  async function admit(input: any) {
    available()
    if ((draining || !profileAgreement()) && !index.get(input.run_id)) throw new Error('agent_runtime_unavailable')
    const request = structuredClone(input)
    await options.prepareRun?.(request)
    let run: any
    let selectedCatalog: ResearchCatalog | undefined
    return lifecycle.withActive(request.q4d_session_id, (config: any) => {
      run = sync.verifyPrompt(request)
      const e = run.claims.envelope
      const profile = profiles.get(config.profile)
      if (!profile) throw new Error('agent_configuration_conflict')
      selectedCatalog = catalogs.get(profile.id)
      const toolNames = selectedCatalog?.tools.map(tool => tool.name) ?? []
      if (e.provider !== config.provider || e.model !== config.model || e.product_profile !== profile.id ||
          e.profile_revision !== profile.revision || e.prompt_bundle_digest !== profile.promptBundleDigest ||
          e.skills_digest !== profile.skillsDigest || e.tool_catalog_revision !== profile.toolCatalogRevision ||
          Object.entries(manifest).some(([key, value]) => e[key] !== value) || !isDeepStrictEqual(run.allowedTools, toolNames) ||
          (selectedCatalog ? e.budgets.max_tool_calls < 1 : e.budgets.max_tool_calls !== 0)) {
        throw new Error('agent_capability_rejected')
      }
    }, async () => {
      available(); run.authorize()
      const agent = handles.get(request.q4d_session_id)!.agent
      const fresh = createUserMessage({ content: request.content, source: { kind: 'user' } })
      const candidate = { session_id: request.q4d_session_id, dsh_session_id: agent.session.id, run_id: request.run_id,
        client_request_id: request.client_request_id, request_hash: request.request_hash, message_id: fresh.id,
        execution_envelope_digest: request.execution_envelope_digest }
      const previous = index.get(request.run_id)
      let binding = previous ? index.prepare(candidate) : candidate
      const ack = () => ({ durable: true, message_id: binding.message_id, run_id: binding.run_id, state: 'accepted' })
      if (previous && inserted(agent, binding.message_id)) {
        if (active.get(binding.session_id)?.binding.run_id !== binding.run_id) options.releaseRun?.(binding.run_id)
        return ack()
      }
      if (active.has(binding.session_id)) throw new Error('agent_run_in_progress')
      const lease = supervisor.begin({ ...run, authorize: () => {
        currentModel(run.claims.envelope.provider, run.claims.envelope.model); return run.authorize()
      } }, (code: string, budget: any) => {
        try { recordStop(binding.run_id, code, budget) } finally { agent.cancel({ kind: 'user' }) }
      })
      const execution: Execution = {
        binding, lease, outputLimit: run.claims.envelope.budgets.max_output_tokens,
        modelOutputLimit: currentModel(run.claims.envelope.provider, run.claims.envelope.model).agent.max_output_tokens }
      active.set(binding.session_id, execution)
      try {
        if (selectedCatalog) {
          const mcp = sync.readConfiguration().mcp
          const gateway = new TrustedGateway({ url: mcp.url, runtimeToken: mcp.runtime_token, catalog: selectedCatalog.tools,
            timeoutMs: mcp.tool_timeout_ms, maxResponseBytes: 512 * 1024 })
          gateway.beginRun(agent.session.id, { ...run, authorize: lease.check })
          execution.gateway = gateway
        }
        if (!previous) binding = index.prepare(candidate)
        execution.binding = binding
        agent.followup(freezeMessage({ ...fresh, id: brandString<MessageId>(binding.message_id) }))
        await ctx.sessions.flush(agent.session)
        await project(agent, binding)
        execution.completion = (async () => {
          try { await agent.whenIdle(); await project(agent, binding, true) }
          catch { fatal = true; supervisor.close() }
          finally {
            execution.gateway?.endRun(agent.session.id, binding.run_id)
            lease.finish(); active.delete(binding.session_id); options.releaseRun?.(binding.run_id)
            for (const key of claimedSources) if (key.startsWith(binding.session_id + ':')) claimedSources.delete(key)
          }
        })()
        return ack()
      } catch {
        fatal = true; supervisor.close()
        await agent.whenIdle().catch(() => {})
        execution.gateway?.endRun(agent.session.id, binding.run_id)
        lease.finish(); active.delete(binding.session_id); options.releaseRun?.(binding.run_id)
        throw new Error('agent_runtime_unavailable')
      }
    })
  }
  // A rolling API/profile update may pause new work while the durable runtime
  // remains safe to drain, inspect and upgrade. Deployment health must not
  // depend on the candidate profile already being installed.
  let lastReadinessReason: string | undefined
  function healthState() {
    const configured = sync.status().configuration_applied === true
    const reason = closed ? 'closed' : fatal ? 'fatal' : !configured ? 'configuration_unavailable'
      : draining ? 'draining' : !profileAgreement() ? 'profile_mismatch' : 'ready'
    if (reason !== lastReadinessReason) {
      console.info(JSON.stringify({ event: 'agent_runtime_readiness_changed', reason }))
      lastReadinessReason = reason
    }
    return { runtimeReady: !closed && !fatal && configured, ready: reason === 'ready' }
  }
  const backend = {
    probe: options.probe ? (request: any, signal?: AbortSignal) => {
      available()
      if (draining || !profileAgreement()) throw new Error('agent_runtime_unavailable')
      return track(() => options.probe!(request, signal))
    } : undefined,
    health: () => ({ status: healthState().ready ? 'ready' : 'unavailable' }),
    runtimeHealth: () => ({ status: healthState().runtimeReady ? 'ready' : 'unavailable' }),
    maintenance: (action?: string) => {
      available()
      if (action === 'drain') draining = true
      else if (action === 'resume') draining = false
      return { status: draining ? 'draining' : 'active', active_runs: active.size, pending_operations: pending.size }
    },
    capabilities: () => ({ ...provenance, model_config_revision: sync.status().model_config_revision, runtime_manifest: manifest,
      profile_source: options.profileSource ?? 'config', profiles: [...profiles.values()].map(profileIdentity), profiles_aligned: profileAgreement(),
      ready_models: sync.readConfiguration().providers.filter((p: any) => modelReady(p.id, p.default_model, sync.status().model_config_revision))
        .map((p: any) => ({ provider: p.id, model: p.default_model })),
      features: { session_resume: true, event_replay: true, cancel: true, approval: Boolean(catalog?.tools.some(tool => ['R2', 'R3'].includes(tool.risk))), session_provisioning: true,
        raw_provider_delta: false, compaction_events: true, fork: false } }),
    createSession: (request: any) => {
      available()
      if (draining || !profileAgreement()) throw new Error('agent_runtime_unavailable')
      if (!validSessionCreate(request)) throw new Error('agent_invalid_request')
      return track(() => lifecycle.provision(request))
    },
    closeSession: (id: string) => track(() => lifecycle.close(id)),
    readTranscript: (id: string, signal: AbortSignal) => track(async () => {
      const agent = handles.get(id)?.agent
      if (agent) { const events = agent.session.snapshotEvents(); await ctx.sessions.flush(agent.session); return { events, bindings: index.list() } }
      const stored = await readStored(id, signal)
      if (!stored) throw new TranscriptError('agent_session_not_found', 404)
      return { events: stored.events, bindings: index.list() }
    }),
    admit: (request: any) => track(async () => {
      try { return await admit(request) }
      catch (error) {
        if (active.get(request.q4d_session_id)?.binding.run_id !== request.run_id) options.releaseRun?.(request.run_id)
        throw error
      }
    }), getRun, reconcileRun: (id: string) => track(() => reconcileRun(id)),
    decide: (id: string, body: any) => {
      available()
      if (!registerTools) throw new Error('agent_approval_missing')
      registerTools.decide(id, body)
    },
    events: (runId: string) => {
      const binding = index.get(runId)
      if (!binding) throw new Error('agent_run_not_found')
      return { journal: journal(binding), live: active.get(binding.session_id)?.binding.run_id === runId }
    },
    cancel: (runId: string) => {
      const result = getRun(runId)
      if (!result.terminal) {
        const execution = active.get(result.session_id)
        if (execution?.binding.run_id !== runId) throw new Error('agent_run_recovering')
        execution.lease.stop('user')
      }
      return getRun(runId)
    },
  }
  let closing: Promise<void> | undefined
  function close() {
    if (closing) return closing
    closed = true; unsubscribe(); supervisor.close()
    closing = (async () => {
      await Promise.allSettled([...pending])
      await Promise.all([...active.values()].map(execution => execution.completion))
      try { await ctx.fiber.dispose() }
      finally { index.close(); bindings.close(); stops.close(); for (const value of journals.values()) value.close() }
    })()
    return closing
  }
  try {
    await ctx.plugin(LlmRuntime)
    await ctx.plugin(SessionStore)
    await ctx.plugin(SystemPrompt, {})
    await ctx.plugin(ToolRuntime, {})
    await ctx.plugin(AgentRegistry)
    await ctx.plugin(SessionProjectionRegistry)
    await ctx.plugin(JsonlSessionPersistence, { root: join(directory, 'sessions'), compression: 'none' })
    await ctx.plugin(TokenMeter)
    await ctx.plugin(ToolResultPruner)
    await ctx.plugin(BasicCompaction, Q4dContext.compactionConfig)
    await ctx.plugin(AgentLoop, { agents: [] })
    await ctx.plugin(Q4dRunPolicy, { execution: id => active.get(id), progress })
    await ctx.plugin(Q4dContext, { execution: (id: string) => active.get(id), project, progress })
    if (catalog) registerTools = installResearchTools(ctx, catalog, {
      selectTools: id => catalogs.get(id)?.tools ?? [],
      execution: id => {
        const execution = active.get(id)
        if (!execution?.gateway) throw new Error('agent_tool_context_missing')
        return { lease: execution.lease, gateway: execution.gateway }
      },
      beforeTool: async exec => {
        const agent = exec.agent!, id = agent.session.id
        const binding = active.get(id)?.binding
        if (!binding) throw new Error('agent_tool_context_missing')
        const boundary = inserted(agent, binding.message_id)!.seq
        const source = agent.session.snapshotEvents().find(event => event.seq > boundary && event.type === 'tool/call' &&
          event.data.callId === exec.callId && event.data.name === exec.name && !claimedSources.has(`${id}:${event.seq}`))
        if (!source) throw new Error('agent_tool_source_missing')
        claimedSources.add(`${id}:${source.seq}`)
        await project(agent, binding)
        return source.seq
      },
      observe: (type, call, data) => {
        try {
          journal(index.get(call.run_id)).append(`tool:${call.tool_call_id}:${type}`, {
            type, occurred_at: new Date().toISOString(), data: { tool_call_id: call.tool_call_id, ...data } })
        } catch { fatal = true; supervisor.close(); throw new Error('agent_runtime_unavailable') }
      },
    })
    const handler = createBridgeHandler({ token: bridgeToken, backend, transcriptProjection: { isAppendSurfaceEvent, deriveEventMessage } })
    return Object.freeze({ handler, close })
  } catch { await close(); throw new Error('agent_runtime_configuration_invalid') }
}
