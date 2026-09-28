/** T-00 only: public Cordis message identity, persistence and replay proof. */
import { Context } from '@deepseek-ai/cordis'
import { brandString } from '@deepseek-ai/dsh-brand'
import LlmRuntime, { createUserMessage, freezeMessage, type MessageId } from '@deepseek-ai/dsh-llm'
import SessionStore, { SESSION_FORMAT_VERSION, SessionLogOffset, isAppendSurfaceEvent, deriveEventMessage, type SessionId } from '@deepseek-ai/dsh-session'
import { SessionPersistenceNotFoundError } from '@deepseek-ai/dsh-session-persistence'
import SystemPrompt from '@deepseek-ai/dsh-system-prompt'
import ToolRuntime from '@deepseek-ai/dsh-tools'
import AgentRegistry, { type Agent, type AgentHandle } from '@deepseek-ai/dsh-agent'
import AgentLoop from '@deepseek-ai/dsh-agent-loop'
import SessionProjectionRegistry from '@deepseek-ai/dsh-session-projection'
import JsonlSessionPersistence from '@deepseek-ai/dsh-session-persistence-jsonl'
import TokenMeter from '@deepseek-ai/dsh-token-meter'
import BasicCompaction from '@deepseek-ai/dsh-compaction-basic'
import * as FixtureModel from './control-surface-llm/index.mjs'
import { createHash } from 'node:crypto'
import { createServer } from 'node:http'
import { join } from 'node:path'
import { RequestIndex } from '../src/persistence/request-index.mjs'
import { SessionBindings } from '../src/persistence/session-bindings.mjs'
import { SessionLifecycle, sessionPreset } from '../src/bridge/session-lifecycle.mjs'
import { resolveFixtureSession, fixtureProfileRevision } from './session-config.mjs'
import { EventJournal } from '../src/persistence/event-journal.mjs'
import { queryTool } from '../evals/fixture-v1/helpers/mcp-gateway.mjs'
import { installTrustedMcp } from './trusted-mcp-plugin.ts'
import { createBridgeHandler } from '../src/bridge/http-handler.mjs'
import { projectRun } from '../src/bridge/run-projection.mjs'
import { verifyFixtureCapability } from './verify-capability.mjs'
import { createRunVerifier, canonicalAuthorizationJSON, runtimeAudience } from '../src/auth/run-capability.mjs'
import { DurableLog } from '../src/persistence/durable-log.mjs'
import { TranscriptError } from '../src/bridge/transcript.mjs'
import { projectUsage, terminalData, projectRejectedTool } from '../src/bridge/event-projection.mjs'

const directory = process.env.Q4D_T00_ROOT!
const token = process.env.Q4D_T00_BRIDGE_TOKEN!
if (!directory || !token) throw new Error('fixture configuration missing')
// Explicit T-00 composition: shared v1 verifier + an in-memory stand-in for
// current control-plane policy. No legacy-token fallback in this mode.
const authorization = process.env.Q4D_T00_AUTHORIZATION ? JSON.parse(process.env.Q4D_T00_AUTHORIZATION) : undefined
const runVerifier = authorization ? createRunVerifier({ ...authorization, audience: runtimeAudience,
  authorize: (claims: any) => Boolean(authorization.policies?.[claims.envelope.run_id]) &&
    canonicalAuthorizationJSON(authorization.policies[claims.envelope.run_id]) === canonicalAuthorizationJSON(claims),
}) : undefined
const ctx = new Context()
await ctx.plugin(LlmRuntime)
await ctx.plugin(SessionStore)
await ctx.plugin(SystemPrompt, { persona: '' })
await ctx.plugin(ToolRuntime, {})
await ctx.plugin(AgentRegistry)
await ctx.plugin(SessionProjectionRegistry)
await ctx.plugin(JsonlSessionPersistence, { root: join(directory, 'sessions'), compression: 'none' })
await ctx.plugin(TokenMeter)
await ctx.plugin(AgentLoop, { agents: [] })
await ctx.plugin(FixtureModel)
await ctx.plugin(BasicCompaction, { auto: false, retainTokens: 64, maxTokens: 128 })
const index = new RequestIndex(join(directory, 'request-index.jsonl'))
const sessionBindings = new SessionBindings(join(directory, 'session-bindings.jsonl'))
const handles = new Map<string, AgentHandle>()
const journals = new Map<string, EventJournal>()
const active = new Map<string, string>()
const inflight = new Map<string, Promise<unknown>>()
const running = new Map<string, Promise<void>>()
const cancelLog = new DurableLog(join(directory, 'cancellations.jsonl'))
const cancellations = new Set<string>(cancelLog.records.map((row: any) => row.run_id))
const claimedSources = new Set<string>()
let projection = Promise.resolve()
const trusted = process.env.Q4D_T00_GATEWAY_URL ? installTrustedMcp(ctx, process.env.Q4D_T00_GATEWAY_URL,
  process.env.Q4D_T00_RUNTIME_TOKEN!, {
    supervisorIPC: false,
    beforeTool: async exec => {
      const id = exec.agent!.session.id
      const binding = index.get(active.get(id))
      const agent = handles.get(id)!.agent
      const boundary = inserted(agent, binding.message_id)!.seq
      const source = agent.session.snapshotEvents().find(event => event.seq > boundary && event.type === 'tool/call' &&
        event.data.callId === exec.callId && event.data.name === exec.name && !claimedSources.has(`${id}:${event.seq}`))
      if (!source) throw new Error('agent_tool_source_missing')
      // Claim synchronously before awaiting any flush: parallel calls may reuse
      // the same model ID, but each public call event has its own stable seq.
      claimedSources.add(`${id}:${source.seq}`)
      await flushProjection(agent, binding)
      if (process.env.Q4D_T00_PAUSE_BEFORE_TOOL === '1') {
        process.send?.({ event: 'before_tool', source_seq: source.seq })
        await new Promise<void>(resolve => {
          if (exec.signal.aborted) return resolve()
          exec.signal.addEventListener('abort', () => resolve(), { once: true })
        })
      }
      return source.seq
    },
    beforeDispatch: async sessionId => {
      const binding = index.get(active.get(sessionId))
      await flushProjection(handles.get(sessionId)!.agent, binding)
    },
    observe: (type, call, data) => {
      try {
        journal(index.get(call.run_id)).append(`tool:${call.tool_call_id}:${type}`, {
          type, occurred_at: new Date().toISOString(), data: { tool_call_id: call.tool_call_id, ...data },
        })
      } catch { process.exit(1) } // Cannot continue dispatch after a failed durable observation.
    },
  }) : undefined

ctx.on('session/event', (session, event) => {
  if (!trusted || !['assistant/message', 'tool/result', 'turn/end'].includes(event.type)) return
  const binding = index.get(active.get(session.id))
  const agent = handles.get(session.id)?.agent
  if (!binding || !agent) return
  projection = projection.then(() => flushProjection(agent, binding)).catch(() => { process.exit(1) })
})

function journal(binding: any): EventJournal {
  let value = journals.get(binding.run_id)
  if (!value) {
    value = new EventJournal(join(directory, `events-${binding.run_id}.jsonl`), binding.session_id, binding.run_id)
    journals.set(binding.run_id, value)
  }
  return value
}

function inserted(agent: Agent, messageId: string) {
  return agent.session.snapshotEvents().find(event => event.type === 'agent/inbox/spliced' &&
    event.data.inserted.some(message => message.id === messageId))
}

// Project only public, flushed Session facts. Retrying projection after a crash
// uses the same source seq/time, and therefore produces identical SSE IDs.
function project(agent: Agent, binding: any, fault?: string, live = false, events = agent.session.snapshotEvents(), deferTerminal = false) {
  const insertion = events.find(event => event.type === 'agent/inbox/spliced' &&
    event.data.inserted.some(message => message.id === binding.message_id))
  if (!insertion) throw new Error('fixture_message_not_durable')
  const target = journal(binding)
  if (target.terminal) return target.terminal.slice(4)
  target.append(`inbox:${insertion.seq}`, { type: 'run.started', occurred_at: new Date(insertion.time).toISOString(),
    data: { message_id: binding.message_id, execution_envelope_digest: binding.execution_envelope_digest } })
  crash('after_first_event', fault)
  const userIndex = events.findIndex(event => event.type === 'user/message' && event.data.id === binding.message_id)
  const start = userIndex < 0 ? undefined : events.slice(0, userIndex).findLast(event => event.type === 'turn/start')
  let state = 'running'
  if (start?.type === 'turn/start') {
    for (const event of events) {
      if (!('turn' in event.data) || event.data.turn !== start.data.turn) continue
      const occurred_at = new Date(event.time).toISOString()
      if (event.type === 'assistant/message' && isAppendSurfaceEvent(event)) {
        const text = event.data.message.content.filter(block => block.type === 'text').map(block => block.text).join('')
        if (trusted && text) target.append(`delta:${event.seq}`, { type: 'message.delta', occurred_at,
          data: { message_id: event.data.message.id, text } })
        if (text) target.append(`message:${event.seq}`, { type: 'message.completed', occurred_at,
          data: { message_id: event.data.message.id, text } })
        if (trusted && event.data.usage) target.append(`usage:${event.seq}`, { type: 'usage.updated', occurred_at,
          data: { message_id: event.data.message.id, usage: projectUsage(event.data.usage) } })
      } else if (trusted && event.type === 'tool/result' && isAppendSurfaceEvent(event)) {
        projectRejectedTool(target, event, events, binding.run_id, () => {
          if (process.env.Q4D_T00_CRASH_AFTER_REJECT_PROPOSED === '1') process.kill(process.pid, 'SIGKILL')
        })
      } else if (event.type === 'turn/end') {
        if (deferTerminal) continue
        state = event.data.reason.kind === 'completed' ? 'completed'
          : event.data.reason.kind === 'interrupted' ? 'interrupted'
            : event.data.reason.kind === 'aborted' && event.data.reason.reason.kind === 'user' ? 'cancelled' : 'failed'
        if (state !== 'completed' && cancellations.has(binding.run_id)) state = 'cancelled'
        target.append(`terminal:${event.seq}`, { type: `run.${state}`, occurred_at, data: terminalData(state, event.data.reason) })
      }
    }
  }
  if (!live && !deferTerminal && state === 'running') {
    // A persisted but unclaimed inbox item was admitted, then interrupted.
    // It is not silently re-enqueued and does not acquire a fresh Capability.
    state = cancellations.has(binding.run_id) ? 'cancelled' : 'interrupted'
    target.append(`interrupted:${insertion.seq}`, { type: `run.${state}`, occurred_at: new Date(insertion.time).toISOString(), data: terminalData(state) })
  }
  return state
}

async function flushProjection(agent: Agent, binding: any, recover = false) {
  // Capture BEFORE flush: facts arriving during the flush belong to the next
  // batch and must not be advertised as already durable.
  const events = agent.session.snapshotEvents()
  await ctx.sessions.flush(agent.session)
  const target = journal(binding)
  if (recover && !target.terminal) {
    // Recover durable rejection results before closing remaining unknown Tools;
    // a crash between rejected proposal/result must preserve its known outcome.
    project(agent, binding, undefined, true, events, true)
    const facts = target.snapshot()
    for (const proposed of facts.filter((event: any) => event.type === 'tool.proposed')) {
      const id = proposed.data.tool_call_id
      if (facts.some((event: any) => ['tool.completed', 'tool.failed'].includes(event.type) && event.data.tool_call_id === id)) continue
      target.append(`tool:${id}:tool.failed`, { type: 'tool.failed', occurred_at: new Date().toISOString(),
        data: { tool_call_id: id, code: 'agent_tool_outcome_unknown' } })
    }
  }
  return project(agent, binding, undefined, !recover, events)
}

function crash(point: string, requested?: string) {
  if (point === requested) process.kill(process.pid, 'SIGKILL')
}

async function admit(params: any) {
  const { request, fault } = params
  for (const id of [request.q4d_session_id, request.run_id, request.client_request_id]) {
    if (typeof id !== 'string' || !/^[A-Za-z0-9_-]{1,128}$/.test(id)) throw new Error('agent_invalid_request')
  }
  if (!Array.isArray(request.content) || request.content.length !== 1 || request.content[0].type !== 'text' ||
      typeof request.content[0].text !== 'string' || !request.content[0].text || request.content[0].text.length > 32000) {
    throw new Error('agent_invalid_request')
  }
  // Fixture request-hash domain, intentionally excludes short-lived secrets.
  const hash = 'sha256:' + createHash('sha256').update(JSON.stringify({ session_id: request.q4d_session_id, content: request.content })).digest('hex')
  if (request.request_hash !== hash) throw new Error('agent_request_conflict')
  const agent = handles.get(request.q4d_session_id)?.agent
  if (!agent) throw new Error('agent_session_not_found')
  const fresh = createUserMessage({ content: request.content, source: { kind: 'user' } })
  const candidate = {
    session_id: request.q4d_session_id, dsh_session_id: agent.session.id, run_id: request.run_id,
    client_request_id: request.client_request_id, request_hash: request.request_hash,
    message_id: fresh.id, execution_envelope_digest: request.execution_envelope_digest,
  }
  const previous = index.get(request.run_id)
  // Check retry identity without reserving a rejected new request in the log.
  let binding = previous ? index.prepare(candidate) : candidate
  const existing = inflight.get(binding.run_id)
  if (existing) return existing
  const accepted = { durable: true, message_id: binding.message_id, run_id: binding.run_id, state: 'accepted' }
  if (trusted && previous && inserted(agent, binding.message_id)) return accepted
  const busy = active.get(request.q4d_session_id)
  if (busy && busy !== request.run_id) {
    if (!journal(index.get(busy)).terminal) throw new Error('agent_run_in_progress')
    await running.get(busy)
    return admit(params) // Recheck ownership after yielding to terminal cleanup.
  }
  if (trusted) {
    const run = params.run
    if (run?.sessionId !== binding.session_id || run?.runId !== binding.run_id) throw new Error('agent_invalid_context')
    trusted.beginRun(agent.session.id, run)
  }
  try { if (!previous) binding = index.prepare(candidate) }
  catch (error) { trusted?.endRun(agent.session.id, binding.run_id); throw error }
  active.set(binding.session_id, binding.run_id)
  const operation = (async () => {
    crash('after_intent', fault)
    if (!inserted(agent, binding.message_id)) {
      const message = freezeMessage({ ...fresh, id: brandString<MessageId>(binding.message_id) })
      agent.followup(message)
      await ctx.sessions.flush(agent.session)
      crash('after_inbox_flush', fault)
      if (trusted) {
        await flushProjection(agent, binding)
        const completion = (async () => {
          try {
            await agent.whenIdle()
            await projection
            await flushProjection(agent, binding, true)
          } catch { process.exit(1) }
          finally {
            trusted.endRun(agent.session.id, binding.run_id)
            active.delete(binding.session_id)
            inflight.delete(binding.run_id)
            running.delete(binding.run_id)
          }
        })()
        running.set(binding.run_id, completion)
        return accepted
      }
      await agent.whenIdle()
    }
    await ctx.sessions.flush(agent.session)
    crash('after_turn_flush', fault)
    const state = project(agent, binding, fault)
    crash('after_projection', fault)
    return { durable: true, message_id: binding.message_id, run_id: binding.run_id, state }
  })()
  inflight.set(binding.run_id, operation)
  try { return await operation }
  finally { if (!trusted) { inflight.delete(binding.run_id); active.delete(binding.session_id) } }
}

async function session(method: string, id: string, config = { provider: 'q4d-control-fixture', model: 'alpha', tools: ['query_kline'] }, binding?: any) {
  const options = {
    agentOptions: { provider: config.provider, model: config.model },
    setup: async (agentCtx: Context) => {
      if (!config.tools.includes('query_kline')) return
      agentCtx.tools.register({
        name: 'mcp__q4d__query_kline', description: 'Bounded deterministic fixture.', parameters: queryTool.inputSchema,
        output: { schema: { type: 'object', additionalProperties: true },
          render: (_args, value) => [{ type: 'text', text: JSON.stringify(value) }] },
        async execute(args: any) { return { symbol: args.symbol, bars: [{ date: '2026-09-04', close: 100 }] } },
      })
    },
  }
  const handle = method === 'create'
    ? await ctx.agents.create({ ...options, sessionId: brandString<SessionId>(id),
      meta: { cwd: directory, ...(binding ? { agentPreset: sessionPreset(binding) } : {}) } })
    : await ctx.agents.resume({ ...options, resumeSessionId: brandString<SessionId>(id) })
  handles.set(id, handle)
  if (method === 'resume') {
    handle.agent.cancel({ kind: 'user' })
    await handle.agent.whenIdle()
  }
  // An empty Session has no event to trigger lazy JSONL materialization.
  // Flush alone cannot promise that session/create survives a process crash.
  await ctx.sessionPersistence.ensureMaterialized(handle.agent.session)
  await ctx.sessions.flush(handle.agent.session)
  if (trusted && method === 'resume') {
    for (const binding of index.list().filter((row: any) => row.session_id === id)) {
      if (inserted(handle.agent, binding.message_id)) await flushProjection(handle.agent, binding, true)
    }
  }
  return { session_id: id }
}

async function readTranscript(id: string, signal: AbortSignal) {
  const agent = handles.get(id)?.agent
  if (agent) {
    const events = agent.session.snapshotEvents()
    await ctx.sessions.flush(agent.session)
    return { events, bindings: index.list() }
  }
  try {
    const stored = await ctx.sessionPersistence.readFrom(brandString<SessionId>(id), SessionLogOffset(0), signal)
    return { events: stored.events, bindings: index.list() }
  } catch (error) {
    if (error instanceof SessionPersistenceNotFoundError) throw new TranscriptError('agent_session_not_found', 404)
    throw error
  }
}

function getRun(runId: string) {
  const binding = index.get(runId)
  if (!binding) throw new Error('agent_run_not_found')
  return projectRun(binding, journal(binding), {
    active: active.get(binding.session_id) === binding.run_id, cancelling: cancellations.has(runId),
  })
}

function cancel(runId: string, sessionId?: string) {
  const binding = index.get(runId)
  if (!binding || (sessionId !== undefined && binding.session_id !== sessionId)) throw new Error('agent_run_not_found')
  if (!trusted) throw new Error('agent_runtime_unavailable')
  if (!journal(binding).terminal) {
    if (active.get(binding.session_id) !== binding.run_id) throw new Error('agent_run_recovering')
    if (!cancellations.has(binding.run_id)) {
      cancelLog.append({ run_id: binding.run_id })
      cancellations.add(binding.run_id)
    }
    handles.get(binding.session_id)!.agent.cancel({ kind: 'user' })
  }
  return getRun(runId)
}

const provenance = {
  bridge_protocol: 1, adapter_version: process.env.Q4D_T00_ADAPTER_VERSION ?? '0.0.0-t00', backend: 'cordis',
  dsh_version: '0.1.2-alpha.5', session_format: SESSION_FORMAT_VERSION, event_journal_format: 1, session_binding_format: 1,
}
const lifecycle = new SessionLifecycle({ bindings: sessionBindings, provenance, backend: {
  resolve: resolveFixtureSession,
  liveHeader: (id: string) => handles.get(id)?.agent.session.header,
  readHeader: async (id: string) => {
    try { return (await ctx.sessionPersistence.readFrom(brandString<SessionId>(id), SessionLogOffset(0))).meta }
    catch (error) { if (error instanceof SessionPersistenceNotFoundError) return undefined; throw error }
  },
  materialize: async (binding: any, config: any) => {
    const id = binding.request.q4d_session_id
    if (!handles.has(id)) await session('create', id, config, binding)
    const agent = handles.get(id)!.agent
    await ctx.sessionPersistence.ensureMaterialized(agent.session)
    await ctx.sessions.flush(agent.session)
  },
  resume: (binding: any, config: any) => session('resume', binding.request.q4d_session_id, config),
  isBusy: (id: string) => active.has(id),
  unload: async (id: string) => {
    const handle = handles.get(id)!
    await ctx.sessions.flush(handle.agent.session)
    await handle.dispose()
    handles.delete(id)
    for (const key of claimedSources) if (key.startsWith(`${id}:`)) claimedSources.delete(key)
  },
  checkpoint: (point: string) => crash(point, process.env.Q4D_T00_SESSION_FAULT),
} })

const server = createServer(createBridgeHandler({ token,
  transcriptProjection: { isAppendSurfaceEvent, deriveEventMessage },
  backend: {
    health: () => ({ status: 'ready' }),
    capabilities: () => ({
      ...provenance, model_config_revision: process.env.Q4D_T00_MODEL_REVISION ?? 'fixture-v1',
      features: { session_resume: true, event_replay: true, cancel: Boolean(trusted), approval: Boolean(trusted),
        session_provisioning: true, raw_provider_delta: false, compaction_events: false, fork: false },
    }),
    createSession: (request: any) => lifecycle.provision(request),
    closeSession: (id: string) => lifecycle.close(id),
    readTranscript,
    getRun,
    events: (runId: string) => {
      const binding = index.get(runId)
      if (!binding) throw new Error('agent_run_not_found')
      // A cold unfinished Run has no producer until explicit recovery occurs.
      return { journal: journal(binding), live: Boolean(trusted) && active.get(binding.session_id) === runId }
    },
    admit: async (request: any) => {
      if (!trusted) throw new Error('agent_runtime_unavailable')
      let run: any
      return lifecycle.withActive(request.q4d_session_id, (config: any) => {
        // Validate after waiting for the Session lock, before resume/admission.
        run = runVerifier ? runVerifier.verifyPrompt(request) : verifyFixtureCapability(request, process.env.Q4D_T00_CAPABILITY_PUBLIC_KEY)
        if (runVerifier) {
          const envelope = run.claims.envelope
          const profile = sessionBindings.get(request.q4d_session_id).request.profile
          if (envelope.provider !== config.provider || envelope.model !== config.model || envelope.product_profile !== profile ||
              envelope.profile_revision !== fixtureProfileRevision(profile) ||
              envelope.model_config_revision !== (process.env.Q4D_T00_MODEL_REVISION ?? 'fixture-v1') ||
              envelope.adapter_version !== provenance.adapter_version || envelope.dsh_version !== provenance.dsh_version) throw new Error('agent_capability_rejected')
        }
        if (JSON.stringify([...run.allowedTools].sort()) !== JSON.stringify([...config.tools].sort())) throw new Error('agent_capability_rejected')
      }, () => admit({ request, run, fault: process.env.Q4D_T00_HTTP_FAULT }))
    },
    cancel,
    decide: (approvalId: string, body: any) => {
      if (!trusted) throw new Error('agent_runtime_unavailable')
      trusted.approve(body.tool_call_id, body.approval_receipt, body.run_id, approvalId)
    },
  },
}))
await new Promise<void>(resolve => server.listen(0, '127.0.0.1', resolve))
process.send?.({ event: 'ready', port: (server.address() as any).port })
process.on('message', (message: any) => {
  void (async () => {
    const { method, params = {} } = message
    let result
    if (method === 'create' || method === 'resume') result = await session(method, params.sessionId)
    else if (method === 'prompt') result = await admit(params)
    else if (method === 'approvalDecision' && trusted) {
      trusted.approve(params.toolCallId, params.receipt, params.runId)
      result = {}
    }
    else if (method === 'cancel' && trusted) {
      cancel(params.runId, params.sessionId)
      result = { run_id: params.runId }
    }
    else if (method === 'inspect') result = handles.get(params.sessionId)?.agent.session.snapshotEvents()
    else if (method === 'transcript') result = handles.get(params.sessionId)?.agent.session.deriveMessages()
    else if (method === 'compact') {
      const agent = handles.get(params.sessionId)!.agent
      result = await ctx.compaction.compactNow(agent, new AbortController().signal)
      await ctx.sessions.flush(agent.session)
    }
    else if (method === 'prune') { journal(index.get(params.runId)).pruneThrough(params.through); result = {} }
    else if (method === 'revokeAuthorization' && authorization) {
      delete authorization.policies[params.runId]
      result = {}
    }
    else throw new Error('fixture_unknown_method')
    process.send?.({ id: message.id, result })
  })().catch((error: Error) => process.send?.({ id: message.id, error: error.message }))
})
process.on('disconnect', async () => {
  server.closeAllConnections()
  server.close()
  await ctx.fiber.dispose()
  index.close()
  sessionBindings.close()
  cancelLog.close()
  for (const journal of journals.values()) journal.close()
  process.exit(0)
})
