import { execFileSync } from 'node:child_process'
import { readFileSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'

// Development-time generation only. Runtime/Go never discover new models or
// download metadata in a request path. --check detects drift without writing.
const root = resolve(import.meta.dirname, '..')
const lock = JSON.parse(readFileSync(join(root, 'upstream.lock.json'), 'utf8'))
const source = execFileSync(join(root, 'scripts/fetch-dsh-source.sh'), { encoding: 'utf8' }).trim()
const pinned = JSON.parse(execFileSync(process.execPath, ['--input-type=module', '-e', `
  import { readFileSync } from 'node:fs';
  import { getBuiltinModels } from '@earendil-works/pi-ai/providers/all';
  const pkg=JSON.parse(readFileSync(new URL('../../package.json',import.meta.resolve('@earendil-works/pi-ai/providers/all')),'utf8'));
  if(pkg.version!=='0.84.2') throw new Error('unexpected_pi_catalog_version');
  process.stdout.write(JSON.stringify(Object.fromEntries(['anthropic','deepseek','openai'].map(id=>[id,getBuiltinModels(id).map(m=>({id:m.id,contextWindow:m.contextWindow,maxTokens:m.maxTokens}))]))));
`], { cwd:join(source,'packages/llm/llm-pi-ai'), encoding:'utf8' }))
const models = []
for (const vendor of ['anthropic', 'deepseek', 'openai']) {
  for (const model of pinned[vendor]) {
    if (!/^[A-Za-z0-9_./:-]{1,128}$/.test(model.id) || !Number.isSafeInteger(model.contextWindow) || model.contextWindow < 2 ||
        !Number.isSafeInteger(model.maxTokens) || model.maxTokens < 1) throw new Error('invalid_pinned_model_metadata')
    models.push({ protocol: vendor === 'anthropic' ? 'anthropic-messages' : 'openai-completions', model: model.id,
      context_window: model.contextWindow, max_output_tokens: Math.min(8192, model.maxTokens, model.contextWindow - 1) })
  }
}
models.sort((a, b) => `${a.protocol}:${a.model}`.localeCompare(`${b.protocol}:${b.model}`, 'en'))
if (new Set(models.map(m => m.protocol + ':' + m.model)).size !== models.length) throw new Error('ambiguous_pinned_model_metadata')
const text = JSON.stringify({ schema_version: 1, source: '@earendil-works/pi-ai@0.84.2', dsh_source_sha256: lock.source.sha256, models }, null, 2) + '\n'
const output = resolve(root, '../internal/service/agent_model_defaults.json')
if (process.argv.includes('--check')) {
  if (readFileSync(output, 'utf8') !== text) throw new Error('model_defaults_drift')
} else writeFileSync(output, text)
process.stdout.write(`Pinned model defaults: ${models.length} entries\n`)
