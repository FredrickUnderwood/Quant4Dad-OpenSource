import { readFileSync } from 'node:fs'
import { createHash } from 'node:crypto'

export const hash = value => 'sha256:' + createHash('sha256').update(value).digest('hex')
const prompts = JSON.parse(readFileSync(new URL('./p0.json', import.meta.url), 'utf8'))

export function defaultProfiles() {
  return Object.entries(prompts).map(([id, systemPrompt]) => ({ id, systemPrompt,
    revision: hash(id + '\n' + systemPrompt), promptBundleDigest: hash(systemPrompt),
    skillsDigest: hash('[]'), toolCatalogRevision: '' }))
}

// Old generated files implicitly follow repository defaults. An operator can
// deliberately pin custom prompts by selecting profileSource: "config".
export function resolveProfiles(config) {
  const source = config.profileSource ?? 'repository'
  if (!['repository', 'config'].includes(source)) throw new Error('agent_runtime_configuration_invalid')
  const defaults = new Map(defaultProfiles().map(p => [p.id, p]))
  const resolve = p => {
    if (source === 'repository' && defaults.has(p?.id)) return defaults.get(p.id)
    if (source === 'config' && (p.promptBundleDigest !== hash(p.systemPrompt) || p.revision !== hash(p.id + '\n' + p.systemPrompt))) {
      throw new Error('agent_runtime_configuration_invalid')
    }
    return p
  }
  if (Array.isArray(config.profiles)) config.profiles = config.profiles.map(resolve)
  else if (config.profile) config.profile = resolve(config.profile)
  return config
}
