import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtemp, realpath, readFile, writeFile, rm, mkdir } from 'node:fs/promises'
import { generateKeyPairSync, createHash } from 'node:crypto'
import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { loadLaunchConfig, loadInputMeter } from '../../../src/runtime/launch-config.mjs'
import { active } from '../../../src/operations/generations.mjs'
import { defaultProfiles } from '../../../profiles/defaults.mjs'
const exec = promisify(execFile), runtimeRoot = resolve(import.meta.dirname, '../../..')
test('TEST-OPERATIONS-01 signed release provisioning produces aligned Go, Runtime, profiles and private credentials', { timeout: 120000 }, async t => {
  const root = await realpath(await mkdtemp(join(tmpdir(), 'q4d-provision-')))
  t.after(() => rm(root, { recursive: true, force: true }))
  const { privateKey, publicKey } = generateKeyPairSync('ed25519')
  const meter = await readFile(join(runtimeRoot, 'src/meter/byte-budget.mjs'))
  const release = { schema_version: 1, version: 'candidate-v1', channel: 'verified', image: 'registry.example/q4d@sha256:' + 'a'.repeat(64),
    q4d_version: '0.0.0', adapter_version: 'v1', dsh_version: '0.1.2-alpha.5', bridge_protocol: 1, session_format: 0,
    event_journal_format: 1, session_binding_format: 1, meter_sha256: 'sha256:' + createHash('sha256').update(meter).digest('hex') }
  await writeFile(join(root, 'private.pem'), privateKey.export({ type: 'pkcs8', format: 'pem' }), { mode: 0o600 })
  await writeFile(join(root, 'public.pem'), publicKey.export({ type: 'spki', format: 'pem' }), { mode: 0o600 })
  await writeFile(join(root, 'release.json'), JSON.stringify(release), { mode: 0o600 })
  const run = (script, ...args) => exec(process.execPath, [join(runtimeRoot, 'scripts', script), ...args], { timeout: 15000 })
  await run('sign-release.mjs', join(root, 'release.json'), join(root, 'private.pem'), join(root, 'signed.json'))
  await run('provision.mjs', join(root, 'deployment'), join(root, 'signed.json'), join(root, 'public.pem'), 'local-validation')
  const config = join(root, 'deployment/config')
  const loaded = await loadLaunchConfig(join(config, 'runtime.json'), { Q4D_AGENT_CONTROL_TOKEN_FILE: join(config, 'control-token'), Q4D_AGENT_BRIDGE_TOKEN_FILE: join(config, 'bridge-token') })
  assert.equal(loaded.profiles.length, 3)
  const go = JSON.parse(await readFile(join(config, 'agent-fragment.json'), 'utf8')).agent
  for (const p of loaded.profiles) {
    assert.equal(go.sessions.profiles[p.id], p.revision)
    assert.equal(go.runs.profiles[p.id].prompt_bundle_digest, p.promptBundleDigest)
  }
  assert.equal(go.runs.manifest.agent_image_digest, loaded.manifest.agent_image_digest)
  const measure = await loadInputMeter(loaded.inputMeter)
  assert.ok(measure({ provider: 'p', model: 'gpt-4o', messages: [] }, { provider: 'p', model: 'gpt-4o', protocol: 'openai-completions', modelConfigRevision: 'a'.repeat(32) }) > 4096)
  assert.equal((await active(join(root, 'deployment/data'))).release.signature.length, 86)
  await exec('go', ['test', '-mod=readonly', '-tags=provisionintegration', './config', '-run=^TestAgentProvisionedConfig$', '-count=1'], {
    cwd: resolve(runtimeRoot, '..'), env: { ...process.env, Q4D_PROVISION_CONFIG: config }, timeout: 90000 })
  await assert.rejects(run('provision.mjs', join(root, 'deployment'), join(root, 'signed.json'), join(root, 'public.pem'), 'local-validation'))
  assert.equal(go.runs.allow_edge, false)
  // Upgrade an old mounted config and keep its credentials and environment.
  const stale = JSON.parse(await readFile(join(config, 'runtime.json'), 'utf8'))
  delete stale.profileSource
  stale.profiles = stale.profiles.map(p => ({ ...p, systemPrompt: 'legacy prompt', revision: 'sha256:' + 'b'.repeat(64), promptBundleDigest: 'sha256:' + 'c'.repeat(64) }))
  await writeFile(join(config, 'old-runtime.json'), JSON.stringify(stale), { mode: 0o600 })
  await run('q4dctl.mjs', 'stage', join(root, 'deployment/data'), 'g-upgrade', join(root, 'signed.json'), join(root, 'public.pem'))
  await run('q4dctl.mjs', 'render-config', join(root, 'deployment/data'), 'g-upgrade', join(config, 'old-runtime.json'), join(config, 'upgraded-runtime.json'))
  const upgraded = await loadLaunchConfig(join(config, 'upgraded-runtime.json'), { Q4D_AGENT_CONTROL_TOKEN_FILE: join(config, 'control-token'), Q4D_AGENT_BRIDGE_TOKEN_FILE: join(config, 'bridge-token') })
  assert.deepEqual(upgraded.profiles, defaultProfiles())
  assert.equal(upgraded.profileSource, 'repository')
  assert.equal(upgraded.controlToken, loaded.controlToken)
  assert.equal(upgraded.bridgeToken, loaded.bridgeToken)
  assert.equal(upgraded.bootstrapURL, loaded.bootstrapURL)
  assert.equal(upgraded.stateDirectory, join(root, 'deployment/data/generations/g-upgrade/state'))
  // Artifact metadata and meter bytes come from this checkout, without secrets.
  const artifact = join(root, 'artifact'); await mkdir(artifact)
  await run('q4dctl.mjs', 'export-artifact', artifact)
  const metadata = JSON.parse(await readFile(join(artifact, 'artifact.json')))
  assert.deepEqual(await readFile(join(artifact, 'input-meter.mjs')), meter)
  assert.equal(metadata.meter_sha256, release.meter_sha256)
  assert.equal(metadata.adapter_version, 'bridge-v1')
  assert.equal(metadata.channel, 'edge')
  await assert.rejects(run('q4dctl.mjs', 'export-artifact', artifact))
  release.channel = 'edge'
  await writeFile(join(root, 'edge.json'), JSON.stringify(release), { mode: 0o600 })
  await run('sign-release.mjs', join(root, 'edge.json'), join(root, 'private.pem'), join(root, 'edge-signed.json'))
  const edgeArgs = [join(runtimeRoot, 'scripts/provision.mjs'), join(root, 'edge-deployment'), join(root, 'edge-signed.json'), join(root, 'public.pem'), 'production']
  await assert.rejects(exec(process.execPath, edgeArgs, { env: { ...process.env, Q4D_ALLOW_EDGE: '0' } }))
  await exec(process.execPath, edgeArgs, { env: { ...process.env, Q4D_ALLOW_EDGE: '1' } })
  const edge = JSON.parse(await readFile(join(root, 'edge-deployment/config/agent-fragment.json'), 'utf8'))
  assert.equal(edge.agent.runs.allow_edge, true)

})
