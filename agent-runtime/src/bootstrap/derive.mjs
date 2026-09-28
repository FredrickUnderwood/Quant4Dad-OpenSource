import { createHash } from 'node:crypto'
import { failBootstrap, validateBootstrap } from './validation.mjs'

// Explicit marker for endpoints that need no real key. Naming a credential
// reference even here prevents pi-ai from discovering an ambient account key.
export const keylessMarker = 'q4d-keyless-no-credential'
const efforts = new Set(['off', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max'])
export function deriveDshConfiguration(input) {
  const snapshot = validateBootstrap(input)
  const providers = {}, refs = {}, routes = {}
  for (const provider of snapshot.providers) {
    if (provider.api_key && !/^[\x21-\x7e]+$/.test(provider.api_key)) failBootstrap('agent_bootstrap_apply_failed')
    const effort = provider.agent.reasoning_effort || undefined
    if (effort !== undefined && !efforts.has(effort)) failBootstrap('agent_bootstrap_apply_failed')
    // Only the non-secret provider identity participates in these names.
    const hash = createHash('sha256').update(provider.id).digest('hex').slice(0, 32)
    const slug = provider.id.toLowerCase().replace(/[^a-z0-9]+/g, '-').slice(0, 16).replace(/^-|-$/g, '') || 'provider'
    const route = `q4d-${slug}-${hash}`
    const ref = `Q4D_LLM_${hash.toUpperCase()}`
    if (Object.hasOwn(providers, route) || Object.hasOwn(refs, ref)) failBootstrap('agent_bootstrap_apply_failed')
    refs[ref] = provider.api_key || keylessMarker
    Object.defineProperty(routes, provider.id, { value: route, enumerable: true })
    providers[route] = {
      displayName: provider.id, api: provider.agent.protocol,
      baseURL: provider.base_url || (provider.type === 'openai' ? 'https://api.openai.com/v1' : 'https://api.anthropic.com'),
      apiKeyEnv: ref,
      models: [{ id: provider.default_model, name: provider.default_model, input: ['text'],
        contextWindow: provider.agent.context_window, maxTokens: provider.agent.max_output_tokens,
        ...(effort && effort !== 'off' ? { reasoningEfforts: { off: null, [effort]: effort } } : { reasoningEfforts: false }) }],
      ...(effort ? { reasoning: effort } : {}),
      retryPolicy: { mode: 'normal', maxRetries: 0 }, transport: 'sse', timeoutMs: 30000, streamIdleTimeoutMs: 30000,
    }
  }
  return { settings: { 'llm-pi-ai': { providers } }, credentials: { version: 1, refs }, routes }
}
