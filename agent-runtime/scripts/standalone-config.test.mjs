import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtemp, realpath, readFile, writeFile, rm, mkdir, stat, chmod, symlink, link } from 'node:fs/promises'
import { createPrivateKey, createPublicKey, sign, verify } from 'node:crypto'
import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { provisionStandalone } from './standalone-config.mjs'
import { defaultProfiles, hash } from '../profiles/defaults.mjs'
import { loadLaunchConfig, loadInputMeter } from '../src/runtime/launch-config.mjs'

const exec = promisify(execFile)
const imageDigest = 'sha256:' + 'a'.repeat(64)
const secretNames = ['control-token', 'bridge-token', 'mcp-token', 'run-signing-key', 'run-public-key']
async function fixture(t) {
  const temporary = await realpath(await mkdtemp(join(tmpdir(), 'q4d-standalone-')))
  t.after(() => rm(temporary, { recursive: true, force: true }))
  return { temporary, output: join(temporary, 'agent'), config: join(temporary, 'agent/config') }
}
const secretsAt = async config => Object.fromEntries(await Promise.all(secretNames.map(async name => [name, await readFile(join(config, name), 'utf8')])))

test('standalone initialization aligns Runtime, Go, keys, shipped profiles, meter and honest local image provenance', async t => {
  const { temporary, output, config } = await fixture(t)
  // A pre-created empty mount is supported.
  await mkdir(output, { mode: 0o700 })
  const result = await provisionStandalone(output, imageDigest)
  assert.equal(result.status, 'initialized')
  for (const path of [output, config, join(output, 'data'), join(output, 'data/state')]) {
    const value = await stat(path)
    assert.equal(value.mode & 0o7777, 0o700)
    assert.equal(value.uid, process.getuid())
  }
  for (const name of [...secretNames, 'runtime.json', 'agent-fragment.json', 'input-meter.mjs', 'standalone-provenance.json']) {
    const value = await stat(join(config, name))
    assert.equal(value.mode & 0o7777, 0o600)
    assert.equal(value.uid, process.getuid())
    assert.equal(value.nlink, 1)
  }
  const secrets = await secretsAt(config)
  assert.equal(new Set(['control-token', 'bridge-token', 'mcp-token'].map(name => secrets[name])).size, 3)
  const runtime = JSON.parse(await readFile(join(config, 'runtime.json'), 'utf8'))
  const agent = JSON.parse(await readFile(join(config, 'agent-fragment.json'), 'utf8')).agent
  const provenance = JSON.parse(await readFile(join(config, 'standalone-provenance.json'), 'utf8'))
  const runtimePackage = JSON.parse(await readFile(new URL('../package.json', import.meta.url), 'utf8'))
  const budgets = JSON.parse(await readFile(new URL('../fixtures/long-run-budgets.json', import.meta.url), 'utf8'))
  assert.deepEqual(runtime.profiles, defaultProfiles())
  assert.equal(runtime.mode, 'production')
  assert.deepEqual(runtime.listen, { host: '127.0.0.1', port: 29091 })
  assert.equal(runtime.bootstrapURL, 'http://127.0.0.1:8080/internal/v1/agent/bootstrap')
  assert.equal(runtime.stateDirectory, '/var/lib/q4d-agent/state')
  assert.equal(runtime.snapshotDirectory, '/run/q4d-snapshots')
  assert.equal(runtime.manifest.q4d_version, 'local-release')
  assert.equal(runtime.manifest.agent_runtime_version, runtimePackage.version)
  assert.equal(runtime.manifest.agent_image_digest, imageDigest)
  assert.equal(runtime.manifest.dsh_version, '0.1.2-alpha.5')
  assert.equal(runtime.manifest.adapter_version, 'bridge-v1')
  assert.deepEqual({ ...agent.runs.manifest, bridge_protocol: 1 }, runtime.manifest)
  assert.equal(agent.runs.release_file, '')
  assert.equal(agent.runs.release_public_key, '')
  assert.equal(agent.runs.allow_edge, false)
  assert.equal(agent.bootstrap.control_token, secrets['control-token'].trim())
  assert.equal(agent.bootstrap.mcp_token, secrets['mcp-token'].trim())
  assert.equal(agent.model_probe.bridge_token, secrets['bridge-token'].trim())
  for (const profile of runtime.profiles) {
    assert.equal(agent.sessions.profiles[profile.id], profile.revision)
    assert.equal(agent.runs.profiles[profile.id].prompt_bundle_digest, profile.promptBundleDigest)
    assert.deepEqual(agent.runs.profiles[profile.id].budgets, budgets)
  }
  const keyBytes = Buffer.from(agent.runs.signing_private_key, 'base64url')
  const privateKey = createPrivateKey({ format: 'der', type: 'pkcs8', key: Buffer.concat([
    Buffer.from('302e020100300506032b657004220420', 'hex'), keyBytes.subarray(0, 32)
  ]) })
  const publicKey = createPublicKey({ format: 'jwk', key: { kty: 'OKP', crv: 'Ed25519', x: secrets['run-public-key'].trim() } })
  assert.equal(createPublicKey(privateKey).export({ format: 'jwk' }).x, agent.bootstrap.capability_public_keys['run-1'])
  assert.equal(verify(null, Buffer.from('standalone run'), publicKey, sign(null, Buffer.from('standalone run'), privateKey)), true)
  const meter = await readFile(join(config, 'input-meter.mjs'))
  assert.deepEqual(meter, await readFile(new URL('../src/meter/byte-budget.mjs', import.meta.url)))
  assert.equal(hash(meter), runtime.inputMeter.sha256)
  assert.equal(provenance.image_digest_kind, 'docker-image-id')
  assert.equal(provenance.signed_release, false)
  assert.equal(provenance.meter_qualification, 'requires-route-calibration')
  for (const value of Object.values(secrets)) assert.equal(JSON.stringify(provenance).includes(value.trim()), false)
  // Exercise the real loader offline. Only host paths and tmpfs requirement
  // change in this test copy; the emitted production configuration stays intact.
  const snapshots = join(temporary, 'snapshots'); await mkdir(snapshots, { mode: 0o700 })
  const localFile = join(config, 'local-test.json')
  await writeFile(localFile, JSON.stringify({ ...runtime, mode: 'local-validation', stateDirectory: join(output, 'data/state'),
    snapshotDirectory: snapshots, inputMeter: { ...runtime.inputMeter, bundlePath: join(config, 'input-meter.mjs') } }), { mode: 0o600 })
  const loaded = await loadLaunchConfig(localFile, {
    Q4D_AGENT_CONTROL_TOKEN_FILE: join(config, 'control-token'), Q4D_AGENT_BRIDGE_TOKEN_FILE: join(config, 'bridge-token')
  })
  const measure = await loadInputMeter(loaded.inputMeter)
  assert.ok(measure({ provider: 'p', model: 'm', messages: [] }, {
    provider: 'p', model: 'm', protocol: 'openai-completions', modelConfigRevision: 'a'.repeat(32)
  }) > 4096)
})

test('refresh preserves all credentials and state while updating image provenance and profiles', async t => {
  const { output, config } = await fixture(t)
  await provisionStandalone(output, imageDigest)
  const secrets = await secretsAt(config)
  const marker = join(output, 'data/state/existing-session')
  await writeFile(marker, 'session state', { mode: 0o600 })
  const stale = JSON.parse(await readFile(join(config, 'runtime.json'), 'utf8'))
  stale.profiles[0].systemPrompt = 'old prompt'
  await writeFile(join(config, 'runtime.json'), JSON.stringify(stale))
  const changedDigest = 'sha256:' + 'b'.repeat(64)
  assert.equal((await provisionStandalone(output, changedDigest)).status, 'refreshed')
  assert.deepEqual(await secretsAt(config), secrets)
  assert.equal(await readFile(marker, 'utf8'), 'session state')
  const runtime = JSON.parse(await readFile(join(config, 'runtime.json'), 'utf8'))
  assert.equal(runtime.manifest.agent_image_digest, changedDigest)
  assert.deepEqual(runtime.profiles, defaultProfiles())
})

test('existing incomplete, malformed or unsafe credentials fail before refreshing any generated file', async t => {
  const mutations = {
    missing: config => rm(join(config, 'bridge-token')),
    malformed: config => writeFile(join(config, 'control-token'), 'bad\n'),
    trailingNewline: config => writeFile(join(config, 'control-token'), 'a'.repeat(64) + '\n\n'),
    duplicate: async config => writeFile(join(config, 'bridge-token'), await readFile(join(config, 'control-token'))),
    wrongMode: config => chmod(join(config, 'mcp-token'), 0o644),
    inconsistentKey: async config => {
      const bytes = Buffer.from((await readFile(join(config, 'run-signing-key'), 'utf8')).trim(), 'base64url')
      bytes[32] ^= 1
      await writeFile(join(config, 'run-signing-key'), bytes.toString('base64url') + '\n')
    },
    symlink: async config => { await rm(join(config, 'mcp-token')); await symlink('control-token', join(config, 'mcp-token')) },
    hardlink: async config => { await rm(join(config, 'mcp-token')); await link(join(config, 'control-token'), join(config, 'mcp-token')) },
    missingGenerated: config => rm(join(config, 'standalone-provenance.json'))
  }
  for (const [name, mutate] of Object.entries(mutations)) await t.test(name, async t => {
    const { output, config } = await fixture(t)
    await provisionStandalone(output, imageDigest)
    const before = await readFile(join(config, 'runtime.json'))
    const signingKey = await readFile(join(config, 'run-signing-key'))
    await mutate(config)
    await assert.rejects(provisionStandalone(output, 'sha256:' + 'b'.repeat(64)))
    assert.deepEqual(await readFile(join(config, 'runtime.json')), before)
    if (name !== 'inconsistentKey') assert.deepEqual(await readFile(join(config, 'run-signing-key')), signingKey)
  })
})

test('rejects unsafe roots, concurrent initialization and invalid CLI digest without secret output', async t => {
  const { temporary, output } = await fixture(t)
  await mkdir(output, { mode: 0o755 })
  await assert.rejects(provisionStandalone(output, imageDigest))
  await chmod(output, 0o700)
  const alias = join(temporary, 'alias'); await symlink(output, alias)
  await assert.rejects(provisionStandalone(alias, imageDigest))
  await mkdir(join(output, '.standalone-config.lock'), { mode: 0o700 })
  await assert.rejects(provisionStandalone(output, imageDigest))
  assert.equal((await stat(join(output, '.standalone-config.lock'))).isDirectory(), true)
  await assert.rejects(provisionStandalone(join(temporary, 'unused'), imageDigest + '\n'))
  const script = new URL('./standalone-config.mjs', import.meta.url).pathname
  await assert.rejects(exec(process.execPath, [script, output, '--image-digest', 'invalid']), error => {
    assert.equal(error.stdout, '')
    assert.equal(error.stderr, 'agent_standalone_configuration_invalid\n')
    return true
  })
})
