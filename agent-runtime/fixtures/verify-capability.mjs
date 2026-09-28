import { verify } from 'node:crypto'

/** Synthetic T-00 issuer format only. The production verifier/bootstrap and
 * Go Gateway's independent authorization remain separate integration work. */
export function verifyFixtureCapability(request, publicKey) {
  if (!publicKey) throw new Error('agent_runtime_unavailable')
  try {
    const [payload, signature, extra] = request.run_capability.split('.')
    if (!payload || !signature || extra || !verify(null, Buffer.from(payload), publicKey, Buffer.from(signature, 'base64url'))) throw new Error()
    const claims = JSON.parse(Buffer.from(payload, 'base64url').toString())
    if (claims.aud !== 'q4d-internal-mcp' || claims.sessionId !== request.q4d_session_id || claims.runId !== request.run_id ||
        !Number.isSafeInteger(claims.expiresAt) || claims.expiresAt <= Date.now() || claims.expiresAt - Date.now() > 86_400_000 ||
        !Array.isArray(claims.allowedTools) || claims.allowedTools.some(name => name !== 'query_kline')) throw new Error()
    return { sessionId: claims.sessionId, runId: claims.runId, expiresAt: claims.expiresAt,
      allowedTools: claims.allowedTools, capability: request.run_capability }
  } catch { throw new Error('agent_capability_rejected') }
}
