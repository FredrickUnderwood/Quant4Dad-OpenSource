// Local-only UI fixture: built frontend + synthetic APIs, never a production server.
// Build web first, then run `node tests/assistant-fixture.mjs` and open the printed URL.
import { createServer } from 'node:http'
import { readFile } from 'node:fs/promises'
import { resolve, extname } from 'node:path'
import { klineAnalysisFixture, klineAnalysisWindowsFixture } from './kline-analysis-fixture.mjs'
const root = resolve(import.meta.dirname, '../dist'), sessions = [], runs = new Map(), transcript = new Map(), subscribers = new Map()
const p0 = process.env.Q4D_ASSISTANT_P0 === '1', research = p0 || process.env.Q4D_ASSISTANT_RESEARCH === '1'
const approvals = new Map(), results = new Map(), hash = 'sha256:' + 'a'.repeat(64)
const chartWindows = process.env.Q4D_ASSISTANT_KLINE_WINDOWS === '1' ? klineAnalysisWindowsFixture() : null
const chartResult = chartWindows?.[0] ?? klineAnalysisFixture()
let sequence = 1, sends = 0, cancels = 0, decisions = 0, modelWrites = 0, ready = false, revision = hash
const provider = { type: 'openai', base_url: 'https://fixture.invalid/v1', default_model: '本地验证模型', has_api_key: true, agent: { enabled: true, context_window: 65536, max_output_tokens: 2048 } }
const markdownAnswer = [
  '## 研究思路', '', '已收到你的问题 🐉。这是本地界面验证的合成回答。', '',
  '先明确**研究目标**，再验证假设。使用 `close` 作为收盘价字段。', '',
  '- 整理行情数据', '- 记录策略假设', '', '1. 检查数据覆盖', '2. 比较回测结果', '',
  '> 先把问题拆小，再逐步验证。', '',
  '| 检查项 | 状态 |', '| --- | --- |', '| 行情数据 | 已就绪 |', '| 策略假设 | 待验证 |', '',
  '- [x] 整理问题', '- [ ] 完成回测', '',
  '```python', 'print("hello, Quant4Dad")', '```', '',
  '[项目说明](https://example.com) · ~~过时假设~~', '',
  '<script>保持为文本显示。</script>', '', '[不安全链接](javascript:alert%281%29)',
].join('\n')
const id = () => '01K00000000000000000' + String(sequence++).padStart(6, '0')
const emit = (run, type, data = {}) => {
  const event = { schema_version: 1, id: String(run.events.length + 1), session_id: run.session_id, run_id: run.run_id, type, data }
  run.events.push(event); run.last_event_id = event.id
  const frame = `id: ${event.id}\nevent: ${type}\ndata: ${JSON.stringify(event)}\n\n`
  for (const res of subscribers.get(run.run_id) ?? []) { res.write(frame); if (run.terminal) res.end() }
}
const finish = (run, cancelled = false) => {
  if (run.terminal) return
  if (run.toolID) {
    emit(run, cancelled || run.rejected ? 'tool.failed' : 'tool.completed', { tool_call_id: run.toolID, ...(cancelled || run.rejected ? { code: run.rejected ? 'agent_approval_denied' : 'agent_tool_cancelled' } : {}) })
    results.set(run.toolID, { run_id: run.run_id, tool_call_id: run.toolID, tool_name: run.toolName ?? 'query_kline', status: cancelled || run.rejected ? 'failed' : 'succeeded', risk: run.risk ?? 'R0', result: { untrusted_data: true, data: run.toolName === 'analyze_kline' ? chartResult : { id: 7, name: '<script>保持为文本</script>', version: 2, status: run.risk === 'R3' ? 'enabled' : 'draft' } } })
  }
  if (!cancelled && run.toolName === 'analyze_kline' && chartWindows) {
    for (const data of chartWindows.slice(1)) {
      const call = id()
      emit(run, 'tool.proposed', { tool_call_id: call, name: 'analyze_kline', arguments: { file_id: data.file_id, start_date: data.requested_start, end_date: data.requested_end } })
      emit(run, 'tool.started', { tool_call_id: call })
      results.set(call, { run_id: run.run_id, tool_call_id: call, tool_name: 'analyze_kline', status: 'succeeded', risk: 'R0', result: { untrusted_data: true, data } })
      emit(run, 'tool.completed', { tool_call_id: call })
    }
  }
  if (!cancelled || run.partial) {
    const message = { message_id: 'answer-' + run.run_id, role: 'assistant', content: [{ type: 'text', text: cancelled ? run.partial : markdownAnswer }], run_id: run.run_id, omitted_content: false }
    transcript.get(run.session_id).push(message)
    emit(run, 'message.completed', { message_id: message.message_id, text: message.content[0].text })
  }
  run.terminal = true; run.state = cancelled ? 'cancelled' : 'completed'; emit(run, 'run.' + run.state)
}
const server = createServer(async (req, res) => {
  const url = new URL(req.url, 'http://127.0.0.1'), path = url.pathname
  const json = (body, code = 200) => { res.writeHead(code, { 'content-type': 'application/json', 'cache-control': 'no-store' }); res.end(JSON.stringify(body)) }
  if (!path.startsWith('/api/')) {
    const file = path.startsWith('/assets/') ? resolve(root, '.' + path) : resolve(root, 'index.html')
    if (!file.startsWith(root + '/')) return json({}, 404)
    try { const bytes = await readFile(file); res.writeHead(200, { 'content-type': ({ '.js': 'text/javascript', '.css': 'text/css' })[extname(file)] ?? 'text/html' }); res.end(bytes) } catch { json({}, 404) }
    return
  }
  let text = ''; for await (const chunk of req) text += chunk
  const body = text ? JSON.parse(text) : {}
  if (path === '/api/v1/auth/status') return json({ auth_required: true, authenticated: true })
  if (path === '/api/v1/agent/options') return json({ profiles: p0 ? ['research', 'strategy_lab', 'pipeline_builder', 'text_only'] : [research ? 'research' : 'text_only'], text_only: !research })
  if (path === '/api/v1/agent/status') return json({ status: ready ? 'ready' : 'model_check_required', runtime: { version: 'local-fixture', adapter_version: 'fixture-v1', dsh_version: '0.1.2-alpha.5', bridge_protocol: 1, image_digest: hash }, profiles: ['research', 'strategy_lab', 'pipeline_builder'].map(id => ({ id, tools: (id === 'research' ? ['query_kline'] : id === 'strategy_lab' ? ['create_strategy', 'run_backtest'] : ['create_pipeline', 'set_pipeline_status']).map(name => ({ name, available: true })) })) })
  if (path === '/api/v1/agent/models') return json({ revision, models: [{ provider: 'fixture', model: '本地验证模型', status: ready ? 'ready' : 'unverified', reason: ready ? 'ready' : 'probe_required', limits_source: 'pinned_catalog', context_window: 65536, max_output_tokens: 2048 }] })
  if (path === '/api/v1/agent/models/fixture/probe') { ready = true; return json({ status: 'ready' }) }
  if (path === '/api/v1/settings/llm-providers') return json({ revision, providers: { fixture: provider } })
  if (path === '/api/v1/settings/llm-providers/fixture' && req.method === 'PATCH') {
    if (req.headers['if-match']?.replaceAll('"', '') !== revision) return json({ error: 'model_revision_stale' }, 412)
    modelWrites++; Object.assign(provider.agent, body.agent); ready = false; revision = 'sha256:' + String(modelWrites).padStart(64, '0'); return json({ revision, provider })
  }
  const approvalMatch = path.match(/^\/api\/v1\/agent\/approvals\/([^/]+)(\/decision)?$/)
  if (approvalMatch) {
    const approval = approvals.get(approvalMatch[1]); if (!approval) return json({}, 404)
    if (approvalMatch[2]) {
      decisions++; approval.status = body.decision === 'allow_once' ? 'consumed' : 'rejected'
      const run = runs.get(approval.run_id); run.rejected = body.decision === 'reject'
      if (!run.rejected) emit(run, 'tool.started', { tool_call_id: run.toolID })
      finish(run); return json({ approval, delivered: true })
    }
    return json(approval)
  }
  const toolMatch = path.match(/^\/api\/v1\/agent\/runs\/([^/]+)\/tools\/([^/]+)$/)
  if (toolMatch) return results.has(toolMatch[2]) ? json(results.get(toolMatch[2])) : json({}, 404)
  if (path === '/api/v1/agent/sessions') {
    if (req.method === 'GET') return json({ items: sessions, next_cursor: null })
    const existing = sessions.find(s => s.key === req.headers['idempotency-key'])
    if (existing) return json(existing, 201)
    const session = { ...body, title_revision: 0, title_pending: false, key: req.headers['idempotency-key'], session_id: id(), status: 'active', created_at: new Date().toISOString() }
    sessions.unshift(session); transcript.set(session.session_id, []); return json(session, 201)
  }
  const sessionMatch = path.match(/^\/api\/v1\/agent\/sessions\/([^/]+)(\/(?:messages|metadata))?$/)
  if (sessionMatch) {
    const session = sessions.find(s => s.session_id === sessionMatch[1]); if (!session) return json({}, 404)
    if (sessionMatch[2] === '/metadata') return json(session)
    if (sessionMatch[2] === '/messages') {
      let run = [...runs.values()].find(r => r.session_id === session.session_id && r.key === body.client_request_id)
      if (!run) {
        sends++; run = { run_id: id(), session_id: session.session_id, key: body.client_request_id, message_id: id(), terminal: false, durable: true, state: 'running', last_event_id: '0', events: [] }
        runs.set(run.run_id, run); transcript.get(session.session_id).push({ message_id: run.message_id, run_id: run.run_id, role: 'user', content: body.content, omitted_content: false })
        const titleRevision = ++session.title_revision, title = '自动总结主题 ' + sends
        session.title_pending = true
        // Deliberately finish after the ordinary answer, exercising independent
        // metadata polling and fencing when a user manually renames meanwhile.
        setTimeout(() => {
          if (session.title_revision !== titleRevision) return
          session.title = title; session.title_pending = false; session.title_revision++
        }, 4000).unref()
        emit(run, 'run.started', { message_id: run.message_id, execution_envelope_digest: 'sha256:' + 'a'.repeat(64) })
        if (research && session.profile !== 'text_only') {
          run.toolID = id()
          const approval = p0 && session.profile !== 'research'
          run.toolName = approval ? session.profile === 'strategy_lab' ? 'create_strategy' : 'set_pipeline_status' : body.content[0].text.includes('K线统计图') ? 'analyze_kline' : 'query_kline'
          run.risk = approval ? session.profile === 'strategy_lab' ? 'R2' : 'R3' : 'R0'
          emit(run, 'tool.proposed', { tool_call_id: run.toolID, name: run.toolName, arguments: approval ? { id: 7, expected_version: 1, name: '<script>保持为文本</script>', status: 'enabled' } : { code: 'sh.600519' } })
          if (approval) {
            const value = { id: String(sequence++).padStart(64, '0'), session_id: session.session_id, run_id: run.run_id, tool_call_id: run.toolID, tool_name: run.toolName, arguments_hash: hash, risk: run.risk, status: 'pending', expires_at: new Date(Date.now() + 300000).toISOString() }
            approvals.set(value.id, value)
            emit(run, 'approval.required', { approval_id: value.id, tool_call_id: run.toolID, name: run.toolName, arguments_hash: hash, risk: run.risk, expires_at: value.expires_at })
          } else emit(run, 'tool.started', { tool_call_id: run.toolID })
        }
        if (!p0 || ['research', 'text_only'].includes(session.profile)) {
          setTimeout(() => {
            if (run.terminal) return
            run.partial = '正在整理研究思路…'
            emit(run, 'message.delta', { message_id: 'answer-' + run.run_id, text: run.partial })
          }, 800).unref()
          setTimeout(() => finish(run), body.content[0].text.includes('停止') ? 60000 : 2200).unref()
        }
      }
      return json({ run_id: run.run_id, durable: true, status: 'accepted' }, 202)
    }
    if (req.method === 'PATCH') { if ('title' in body) { session.title = body.title; session.title_pending = false; session.title_revision += 2 } if ('archived' in body) session.status = body.archived ? 'archived' : 'active'; return json(session) }
    const items = transcript.get(session.session_id).map((item, i) => ({ ...item, seq: String(i + 1) }))
    return json({ session, transcript: { session_id: session.session_id, snapshot_seq: String(items.length), items, has_more: false, next_before_seq: null } })
  }
  const runMatch = path.match(/^\/api\/v1\/agent\/runs\/([^/]+)(\/(?:cancel|events))?$/)
  if (runMatch) {
    const run = runs.get(runMatch[1]); if (!run) return json({}, 404)
    if (runMatch[2] === '/cancel') { cancels++; finish(run, true) }
    if (runMatch[2] === '/events') {
      res.writeHead(200, { 'content-type': 'text/event-stream', 'cache-control': 'no-store' }); res.write(': connected\n\n')
      const cursor = BigInt(req.headers['last-event-id'] ?? '0')
      for (const event of run.events.filter(e => BigInt(e.id) > cursor)) res.write(`id: ${event.id}\nevent: ${event.type}\ndata: ${JSON.stringify(event)}\n\n`)
      if (run.terminal) return res.end()
      const values = subscribers.get(run.run_id) ?? new Set(); values.add(res); subscribers.set(run.run_id, values); res.on('close', () => values.delete(res)); return
    }
    return json({ ...run, events: undefined, key: undefined })
  }
  if (path === '/api/fixture/metrics') return json({ sends, cancels, decisions, modelWrites, runs: runs.size, sessions: sessions.length })
  json({}, 404)
})
server.listen(0, '127.0.0.1', () => process.stdout.write(`http://127.0.0.1:${server.address().port}/assistant\n`))
process.on('SIGTERM', () => { server.closeAllConnections(); server.close() })
