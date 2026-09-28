import { readFileSync } from 'node:fs'
import Ajv2020 from 'ajv/dist/2020.js'
import { isDeepStrictEqual } from 'node:util'

export const bootstrapPath = '/internal/v1/agent/bootstrap'
export const maxBootstrapBytes = 1024 * 1024
export const bootstrapRevision = /^[0-9a-f]{32}\.[0-9a-f]{32}(?![\s\S])/
export const controlTokenPattern = /^[A-Za-z0-9_.~+/=-]{32,256}(?![\s\S])/
const schema = JSON.parse(readFileSync(new URL('../../contracts/bootstrap-v1/bootstrap.schema.json', import.meta.url)))
const validate = new Ajv2020({ strict: true }).compile(schema)
const errorCodes = new Set(['agent_bootstrap_invalid', 'agent_bootstrap_configuration_invalid',
  'agent_bootstrap_input_invalid', 'agent_bootstrap_cancelled', 'agent_bootstrap_timeout',
  'agent_bootstrap_unavailable', 'agent_bootstrap_unauthorized', 'agent_bootstrap_stale',
  'agent_bootstrap_stopped', 'agent_configuration_unavailable', 'agent_capability_rejected'])
for (const code of ['agent_bootstrap_apply_failed', 'agent_bootstrap_storage_failed', 'agent_model_request_failed']) errorCodes.add(code)

export class BootstrapError extends Error {
  constructor(code) {
    code = errorCodes.has(code) ? code : 'agent_bootstrap_unavailable'
    super(code); this.name = 'BootstrapError'; this.code = code
  }
}
export function failBootstrap(code = 'agent_bootstrap_invalid') { throw new BootstrapError(code) }

// Reject syntax that WHATWG URL would silently repair (userinfo, backslashes,
// whitespace, missing authority, query/fragment, dot segments on control paths).
// A provider endpoint may have a path; only trusted local configuration supplies
// the control endpoint. Neither response nor request may redirect that endpoint.
export function bootstrapURL(value, requiredPath) {
  if (typeof value !== 'string' || !value.isWellFormed() || Buffer.byteLength(value) > 2048 ||
      /[\p{Cc}\s\\?#]/u.test(value) || !/^https?:\/\//.test(value)) failBootstrap()
  let url
  try { url = new URL(value) } catch { failBootstrap() }
  const authority = value.slice(value.indexOf('://') + 3).split('/')[0]
  if (!url.hostname || authority.includes('@') || !authority || url.username || url.password) failBootstrap()
  if (requiredPath && (value.slice(value.indexOf('://') + 3 + authority.length) !== requiredPath ||
      url.pathname !== requiredPath)) failBootstrap()
  return url
}

function freeze(value) {
  if (value && typeof value === 'object') {
    for (const child of Object.values(value)) freeze(child)
    Object.freeze(value)
  }
  return value
}

export function validateBootstrap(input) {
  try {
    const value = structuredClone(input)
    if (!validate(value) || value.revision.split('.')[1] !== value.model_config_revision) failBootstrap()
    bootstrapURL(value.mcp.url, '/internal/mcp')
    const market = ['list_instruments', 'get_instrument', 'query_kline', 'read_kline_file', 'analyze_kline', 'execute_python', 'latest_bar_date', 'get_data_coverage']
    const news = ['list_news', 'get_news', 'list_events', 'get_event']
    const scopes = { research: [...market, ...news], strategy_lab: [...market, 'list_indicators', 'list_strategies', 'get_strategy', 'validate_strategy', 'create_strategy', 'update_strategy', 'list_cost_models', 'run_backtest', 'list_backtests', 'get_backtest_job', 'get_backtest_report'], pipeline_builder: [...news, 'get_pipeline_node_types', 'list_pipelines', 'get_pipeline', 'validate_pipeline', 'create_pipeline', 'update_pipeline', 'dry_run_pipeline_safe', 'set_pipeline_status'] }
    const writes = ['create_strategy', 'update_strategy', 'create_pipeline', 'update_pipeline', 'set_pipeline_status']
    for (const catalog of [...(value.tool_catalog ? [value.tool_catalog] : []), ...(value.tool_catalogs ?? [])]) {
      if (catalog.tools.some((tool, i) => !scopes[catalog.profile]?.includes(tool.name) || (i > 0 && catalog.tools[i - 1].name >= tool.name) ||
          tool.risk !== (writes.includes(tool.name) ? 'R2' : ['run_backtest', 'dry_run_pipeline_safe'].includes(tool.name) ? 'R1' : 'R0'))) failBootstrap()
    }
    if (value.tool_catalogs?.some((catalog, i) => i > 0 && value.tool_catalogs[i - 1].profile >= catalog.profile)) failBootstrap()
    if (value.tool_catalog && value.tool_catalogs && !isDeepStrictEqual(value.tool_catalog, value.tool_catalogs.find(c => c.profile === 'research'))) failBootstrap()
    for (const raw of Object.values(value.capability.public_keys)) {
      const bytes = Buffer.from(raw, 'base64url')
      if (bytes.length !== 32 || bytes.toString('base64url') !== raw) failBootstrap()
    }
    for (const [i, provider] of value.providers.entries()) {
      if ((i && value.providers[i - 1].id >= provider.id) ||
          provider.agent.protocol !== ({ openai: 'openai-completions', anthropic: 'anthropic-messages' })[provider.type] ||
          provider.agent.max_output_tokens >= provider.agent.context_window ||
          !provider.api_key.isWellFormed() || !provider.base_url.isWellFormed() ||
          (provider.agent.reasoning_effort !== undefined && !provider.agent.reasoning_effort.isWellFormed())) failBootstrap()
      if (provider.base_url !== '') bootstrapURL(provider.base_url)
    }
    return freeze(value)
  } catch { failBootstrap() }
}

// JSON.parse establishes syntax; this bounded scanner additionally rejects
// duplicate decoded member names (including escaped aliases) at every level.
// No canonical spelling is required: Go's normal Sonic output is accepted.
export function rejectDuplicateMembers(text) {
  const tokens = text.match(/"(?:[^"\\]|\\[\s\S])*"|[{}\[\]:,]|true|false|null|-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?/g) ?? []
  let index = 0
  function visit(depth) {
    if (depth > 32) failBootstrap()
    const token = tokens[index++]
    if (token === '{') {
      const names = new Set()
      while (tokens[index] !== '}') {
        const name = JSON.parse(tokens[index++])
        if (names.has(name)) failBootstrap()
        names.add(name)
        index++ // colon; syntax was checked by JSON.parse
        visit(depth + 1)
        if (tokens[index] !== ',') break
        index++
      }
      index++
    } else if (token === '[') {
      while (tokens[index] !== ']') {
        visit(depth + 1)
        if (tokens[index] !== ',') break
        index++
      }
      index++
    }
  }
  visit(0)
}

export function decodeBootstrap(bytes) {
  try {
    if (!Buffer.isBuffer(bytes) || bytes.length > maxBootstrapBytes) failBootstrap()
    const text = new TextDecoder('utf-8', { fatal: true, ignoreBOM: true }).decode(bytes)
    const value = JSON.parse(text)
    rejectDuplicateMembers(text)
    return validateBootstrap(value)
  } catch { failBootstrap() }
}
