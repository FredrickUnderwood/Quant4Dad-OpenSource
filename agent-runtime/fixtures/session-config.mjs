import { createHash } from 'node:crypto'
import { queryTool } from '../evals/fixture-v1/helpers/mcp-gateway.mjs'
import { sessionProvisionHash } from '../src/persistence/session-bindings.mjs'

// A synthetic, explicit catalog. Nothing in an HTTP request supplies endpoints,
// credentials, plugins, cwd, or Tool definitions. Production bootstrap is T-03.
export function fixtureProfileRevision(profile = 'research') {
  const tools = profile === 'research' ? [queryTool] : []
  return 'sha256:' + createHash('sha256').update(JSON.stringify({ profile, tools })).digest('hex')
}

export function sessionRequest(id = 'session-1', overrides = {}) {
  const value = { q4d_session_id: id, provider: 'q4d-control-fixture', model: 'alpha', profile: 'research',
    profile_revision: fixtureProfileRevision(), model_config_revision: 'fixture-v1', ...overrides }
  return { ...value, provision_request_hash: sessionProvisionHash(value) }
}

export function resolveFixtureSession(request, creating) {
  if (process.env.Q4D_T00_DISABLE_MODEL === '1' || request.provider !== 'q4d-control-fixture' || request.model !== 'alpha' ||
      !['research', 'fixture_empty'].includes(request.profile)) throw new Error('agent_configuration_unavailable')
  if (creating && (request.profile_revision !== fixtureProfileRevision(request.profile) ||
      request.model_config_revision !== (process.env.Q4D_T00_MODEL_REVISION ?? 'fixture-v1'))) throw new Error('agent_configuration_conflict')
  return { provider: request.provider, model: request.model, tools: request.profile === 'research' ? ['query_kline'] : [] }
}
