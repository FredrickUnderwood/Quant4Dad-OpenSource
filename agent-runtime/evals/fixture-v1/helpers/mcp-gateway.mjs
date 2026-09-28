import assert from 'node:assert/strict'
import { createHash, generateKeyPairSync, randomUUID, sign, verify } from 'node:crypto'
import { createServer } from 'node:http'

export const queryTool = {
  name: 'query_kline',
  inputSchema: {
    type: 'object', required: ['symbol', 'limit'], additionalProperties: false,
    properties: { symbol: { type: 'string', minLength: 1, maxLength: 32 }, limit: { type: 'integer', minimum: 1, maximum: 500 } },
  },
}
const ulid = /^[0-7][0-9A-HJKMNP-TV-Z]{25}$/
const hash = args => createHash('sha256').update(JSON.stringify(Object.keys(args).sort().map(key => [key, args[key]]))).digest('hex')

/** An independent, loopback-only fake Go Gateway; all keys/data are synthetic. */
export async function gatewayFixture(t, { verifyCapability } = {}) {
  const { privateKey, publicKey } = generateKeyPairSync('ed25519')
  const runtimeToken = `fixture-runtime-${randomUUID()}`
  const runs = new Map()
  const revoked = new Set()
  const receipts = new Map()
  const ledger = new Map()
  const attempts = []
  const executions = []
  const sockets = new Set()
  const fixture = { runtimeToken, attempts, executions, redirect: undefined,
    publicKey: publicKey.export({ type: 'spki', format: 'pem' }) }
  const server = createServer((req, res) => {
    const send = (status, data) => {
      res.writeHead(status, { 'content-type': 'application/json' })
      res.end(JSON.stringify(data))
    }
    void (async () => {
      if (req.url !== '/internal/mcp') return send(404, {})
      if (req.headers.authorization !== `Bearer ${runtimeToken}`) return send(401, {})
      if (req.method !== 'POST') return send(405, {})
      const chunks = []
      let bytes = 0
      for await (const chunk of req) {
        bytes += chunk.length
        if (bytes > 128 * 1024) return send(413, {})
        chunks.push(chunk)
      }
      const body = JSON.parse(Buffer.concat(chunks).toString())
      const reply = result => {
        const response = { jsonrpc: '2.0', id: body.id, result }
        if (res.headersSent) return res.end(`event: message\ndata: ${JSON.stringify(response)}\n\n`)
        return send(200, response)
      }
      if (body.method === 'initialize') return reply({
        protocolVersion: body.params.protocolVersion, capabilities: { tools: {} },
        serverInfo: { name: 'q4d-test-gateway', version: '1.0.0' },
      })
      if (body.method === 'notifications/initialized') { res.writeHead(202); return res.end() }
      if (body.method !== 'tools/call') return send(400, {})

      const headers = req.headers
      attempts.push({ headers: { ...headers }, body })
      const capability = headers['x-q4d-run-capability']
      let claims, run
      if (verifyCapability) {
        try {
          // Unverified payload is only a lookup hint into the trusted fixture
          // index. The Go verifier checks signature, binding and current policy.
          const parts = String(capability).split('.')
          if (parts.length !== 3) return send(403, {})
          const hint = JSON.parse(Buffer.from(parts[1], 'base64url'))
          run = runs.get(hint.envelope?.run_id)
          if (!run || run.capability !== capability || revoked.has(capability)) return send(403, {})
          const verified = await verifyCapability(capability, run, body.params.name)
          claims = { runId: verified.envelope.run_id, sessionId: verified.session_id,
            aud: 'q4d-internal-mcp', expiresAt: Math.min(verified.exp * 1000, verified.iat * 1000 + verified.envelope.budgets.wall_time_ms) }
        } catch { return send(403, {}) }
      } else {
        const [payload, signature, extra] = String(capability).split('.')
        if (!payload || !signature || extra || revoked.has(capability) ||
            !verify(null, Buffer.from(payload), publicKey, Buffer.from(signature, 'base64url'))) return send(403, {})
        claims = JSON.parse(Buffer.from(payload, 'base64url'))
        run = runs.get(claims.runId)
      }
      if (!run || claims.aud !== 'q4d-internal-mcp' || claims.expiresAt <= Date.now() ||
          run.capability !== capability || run.sessionId !== claims.sessionId ||
          !run.allowedTools.includes(body.params.name)) return send(403, {})
      const id = headers['x-q4d-tool-call-id']
      const key = headers['idempotency-key']
      if (!ulid.test(id) || key !== `q4d:${claims.runId}:${id}`) return send(403, {})
      const args = body.params.arguments
      if (body.params.name !== 'query_kline' || !args || Object.keys(args).sort().join(',') !== 'limit,symbol' ||
          typeof args.symbol !== 'string' || !args.symbol || args.symbol.length > 32 ||
          !Number.isInteger(args.limit) || args.limit < 1 || args.limit > 500) return send(400, {})

      const digest = hash(args)
      if (args.symbol === 'APPROVAL') {
        const nonce = headers['x-q4d-approval-receipt']
        const approval = receipts.get(nonce)
        if (!approval) return reply({ isError: true, content: [{ type: 'text', text: 'Approval required' }],
          structuredContent: { error: { code: 'agent_approval_required', approval_id: key, arguments_hash: `sha256:${digest}`,
            risk: 'R3', expires_at: new Date(Math.min(claims.expiresAt, Date.now() + (fixture.approvalTtlMs ?? 60_000))).toISOString(),
            ...fixture.approvalOverrides } } })
        if (approval.key !== key || approval.capability !== capability || approval.digest !== digest) return send(403, {})
      }
      const started = () => {
        const progressToken = body.params._meta?.progressToken
        if (progressToken === undefined || args.symbol === 'NO_START') return
        res.writeHead(200, { 'content-type': 'text/event-stream' })
        const notification = { jsonrpc: '2.0', method: 'notifications/progress', params: {
          progressToken, progress: 0, total: 1, message: 'q4d.tool.started.v1',
        } }
        res.write(`event: message\ndata: ${JSON.stringify(notification)}\n\n`)
      }
      const previous = ledger.get(key)
      if (previous) {
        if (previous.digest !== digest || previous.capability !== capability) return send(409, {})
        started() // Replay the prior dispatch fact; never execute the body again.
        return reply(previous.result)
      }
      if (args.symbol === 'REDIRECT') {
        res.writeHead(307, { location: fixture.redirect }); return res.end()
      }
      started()
      if (args.symbol === 'WAIT') return // Cancellation must close this request.
      if (args.symbol === 'LARGE') return reply({ content: [{ type: 'text', text: 'x'.repeat(300 * 1024) }] })
      const result = args.symbol === 'FAIL'
        ? { isError: true, content: [{ type: 'text', text: 'Q4D_T00_TOOL_FAILURE' }] }
        : { content: [{ type: 'text', text: JSON.stringify({ symbol: args.symbol, bars: [{ date: '2026-09-04', close: 100 }] }) }] }
      ledger.set(key, { digest, capability, result })
      executions.push({ id, key, claims, args })
      if (args.symbol === 'SSE' || res.headersSent) {
        if (!res.headersSent) res.writeHead(200, { 'content-type': 'text/event-stream' })
        res.write(`event: message\ndata: ${JSON.stringify({ jsonrpc: '2.0', id: body.id, result })}\n\n`)
        return // A valid response need not wait for the server to close its stream.
      }
      return reply(result)
    })().catch(() => { if (!res.headersSent) send(400, {}); else res.destroy() })
  })
  server.on('connection', socket => { sockets.add(socket); socket.on('close', () => sockets.delete(socket)) })
  t.after(async () => {
    for (const socket of sockets) socket.destroy()
    await new Promise(resolve => server.close(resolve))
  })
  await new Promise((resolve, reject) => { server.once('error', reject); server.listen(0, '127.0.0.1', resolve) })
  fixture.url = `http://127.0.0.1:${server.address().port}/internal/mcp`
  fixture.issueRun = (runId, overrides = {}) => {
    const config = { sessionId: `session-${runId}`, runId, expiresAt: Date.now() + 60_000, allowedTools: ['query_kline'], ...overrides }
    const payload = Buffer.from(JSON.stringify({ ...config, aud: 'q4d-internal-mcp' })).toString('base64url')
    config.capability = `${payload}.${sign(null, Buffer.from(payload), privateKey).toString('base64url')}`
    runs.set(runId, structuredClone(config))
    return config
  }
  fixture.registerSignedRun = ({ token, claims, binding }) => {
    assert.ok(verifyCapability, 'shared authorization verifier required')
    runs.set(claims.envelope.run_id, { sessionId: claims.session_id, runId: claims.envelope.run_id,
      allowedTools: claims.allowed_tools, capability: token, binding })
  }
  fixture.approve = invocation => {
    const last = attempts.findLast(attempt => attempt.headers['idempotency-key'] === invocation.idempotency_key)
    assert.ok(last, 'approval must be tied to a previously proposed call')
    const nonce = `receipt-${randomUUID()}`
    receipts.set(nonce, { key: invocation.idempotency_key, digest: hash(last.body.params.arguments),
      capability: last.headers['x-q4d-run-capability'] })
    return nonce
  }
  fixture.revoke = capability => revoked.add(capability)
  return fixture
}
