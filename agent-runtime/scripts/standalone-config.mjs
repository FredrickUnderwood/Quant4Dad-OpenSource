import { constants } from 'node:fs'
import { lstat, mkdir, readdir, readFile, open, rename, rm, realpath } from 'node:fs/promises'
import { resolve, join } from 'node:path'
import { pathToFileURL } from 'node:url'
import { randomBytes, generateKeyPairSync, createPrivateKey, createPublicKey } from 'node:crypto'
import { defaultProfiles, hash } from '../profiles/defaults.mjs'
import { readOwnedFile, validateDirectory } from '../src/runtime/launch-config.mjs'

const fail = () => { throw new Error('agent_standalone_configuration_invalid') }
const digestPattern = /^sha256:[0-9a-f]{64}$/
const versionPattern = /^[A-Za-z0-9_.+-]{1,128}$/
const tokenPattern = /^[A-Za-z0-9_.~+/=-]{32,256}$/
const generatedFiles = ['runtime.json', 'agent-fragment.json', 'input-meter.mjs', 'standalone-provenance.json']
const secretFiles = ['control-token', 'bridge-token', 'mcp-token', 'run-signing-key', 'run-public-key']
const json = value => JSON.stringify(value, null, 2) + '\n'

async function exists(file) {
  try { await lstat(file); return true } catch (error) { if (error.code === 'ENOENT') return false; throw error }
}
async function saveNew(file, bytes) {
  const handle = await open(file, constants.O_WRONLY | constants.O_CREAT | constants.O_EXCL | constants.O_NOFOLLOW, 0o600)
  try { await handle.chmod(0o600); await handle.writeFile(bytes); await handle.sync() } finally { await handle.close() }
}
async function replacePrivate(file, bytes) {
  const temporary = file + '.new-' + randomBytes(12).toString('hex')
  try { await saveNew(temporary, bytes); await rename(temporary, file) } finally { await rm(temporary, { force: true }) }
}
async function privateText(file, maxBytes = 258) {
  return new TextDecoder('utf-8', { fatal: true }).decode(await readOwnedFile(file, maxBytes)).replace(/\n$/, '')
}
function publicFromPrivate(value) {
  if (!/^[A-Za-z0-9_-]{86}$/.test(value)) fail()
  const bytes = Buffer.from(value, 'base64url')
  if (bytes.length !== 64 || bytes.toString('base64url') !== value) fail()
  // Derive the public key from the seed; do not trust the appended public half.
  const key = createPrivateKey({ format: 'der', type: 'pkcs8', key: Buffer.concat([
    Buffer.from('302e020100300506032b657004220420', 'hex'), bytes.subarray(0, 32)
  ]) })
  const publicKey = createPublicKey(key).export({ format: 'jwk' }).x
  if (publicKey !== bytes.subarray(32).toString('base64url')) fail()
  return publicKey
}

/** Offline initialization or refresh, run as the deployment's runtime user.
 * The installer selects the same uid for setup and services. Private files are never repaired or
 * rotated implicitly. A partial first initialization therefore fails closed.
 */
export async function provisionStandalone(output, imageDigest) {
  if (typeof output !== 'string' || !output || /[\p{Cc}]/u.test(output) ||
      typeof imageDigest !== 'string' || imageDigest.length !== 71 || !digestPattern.test(imageDigest)) fail()
  const root = resolve(output), config = join(root, 'config'), data = join(root, 'data')
  const state = join(data, 'state')
  const [runtimePackage, releaseDefaults, upstreamLock, budgets, meter] = await Promise.all([
    readFile(new URL('../package.json', import.meta.url), 'utf8').then(JSON.parse),
    readFile(new URL('../release-defaults.json', import.meta.url), 'utf8').then(JSON.parse),
    readFile(new URL('../upstream.lock.json', import.meta.url), 'utf8').then(JSON.parse),
    readFile(new URL('../fixtures/long-run-budgets.json', import.meta.url), 'utf8').then(JSON.parse),
    readFile(new URL('../src/meter/byte-budget.mjs', import.meta.url))
  ])
  const dshVersion = upstreamLock.source?.tag?.replace(/^dsh-v/, '')
  if (![runtimePackage.version, releaseDefaults.adapter_version].every(value => typeof value === 'string' && !/[\r\n]/.test(value) && versionPattern.test(value)) ||
      dshVersion !== '0.1.2-alpha.5' || dshVersion !== releaseDefaults.dsh_version || releaseDefaults.bridge_protocol !== 1) fail()
  const limits = { max_turns: [1, 4096], max_tool_calls: [0, 4096], max_input_tokens: [1, 100000000],
    max_output_tokens: [1, 10000000], wall_time_ms: [1, 3600000] }
  if (Object.keys(budgets).sort().join() !== Object.keys(limits).sort().join() ||
      Object.entries(limits).some(([key, [min, max]]) => !Number.isSafeInteger(budgets[key]) || budgets[key] < min || budgets[key] > max)) fail()
  const profiles = defaultProfiles(), meterHash = hash(meter)
  const manifest = { q4d_version: 'local-release', agent_image_digest: imageDigest, agent_runtime_version: runtimePackage.version,
    adapter_version: releaseDefaults.adapter_version, dsh_version: dshVersion, bridge_protocol: 1 }
  if (!await exists(root)) {
    if (await realpath(resolve(root, '..')) !== resolve(root, '..')) fail()
    await mkdir(root, { mode: 0o700 })
  }
  await validateDirectory(root)
  // This lock coordinates repeat invocations only; a stale lock is deliberately
  // not deleted automatically after an interrupted initialization.
  const lock = join(root, '.standalone-config.lock')
  await mkdir(lock, { mode: 0o700 })
  try {
    const initial = (await readdir(root)).filter(name => name !== '.standalone-config.lock').length === 0
    let secrets
    if (initial) {
      for (const directory of [config, data, state]) await mkdir(directory, { mode: 0o700 })
      const { privateKey } = generateKeyPairSync('ed25519')
      const jwk = privateKey.export({ format: 'jwk' })
      secrets = Object.fromEntries(['control-token', 'bridge-token', 'mcp-token'].map(name => [name, randomBytes(48).toString('base64url')]))
      secrets['run-signing-key'] = Buffer.concat([Buffer.from(jwk.d, 'base64url'), Buffer.from(jwk.x, 'base64url')]).toString('base64url')
      secrets['run-public-key'] = jwk.x
      for (const name of secretFiles) await saveNew(join(config, name), secrets[name] + '\n')
    } else {
      for (const directory of [config, data, state]) await validateDirectory(directory)
      secrets = Object.fromEntries(await Promise.all(secretFiles.map(async name => [name, await privateText(join(config, name))])))
      const tokens = ['control-token', 'bridge-token', 'mcp-token'].map(name => secrets[name])
      if (!tokens.every(token => !/[\r\n]/.test(token) && tokenPattern.test(token)) || new Set(tokens).size !== tokens.length ||
          publicFromPrivate(secrets['run-signing-key']) !== secrets['run-public-key']) fail()
      // Validate every replaceable destination before any refresh write. Missing
      // metadata is also treated as an incomplete initialization.
      for (const name of generatedFiles) await readOwnedFile(join(config, name), 16 * 1024 * 1024)
    }
    const runtime = { schemaVersion: 1, mode: 'production', profileSource: 'repository', listen: { host: '127.0.0.1', port: 29091 },
      bootstrapURL: 'http://127.0.0.1:8080/internal/v1/agent/bootstrap', stateDirectory: '/var/lib/q4d-agent/state',
      snapshotDirectory: '/run/q4d-snapshots', profiles, manifest,
      inputMeter: { bundlePath: '/run/q4d-config/input-meter.mjs', sha256: meterHash } }
    const goManifest = { ...manifest }; delete goManifest.bridge_protocol
    const fragment = { agent: { enabled: true, profile_source: 'repository',
      bootstrap: { enabled: true, control_token: secrets['control-token'], mcp_token: secrets['mcp-token'],
        mcp_url: 'http://127.0.0.1:8080/internal/mcp', capability_issuer: 'quant4dad', capability_public_keys: { 'run-1': secrets['run-public-key'] } },
      sessions: { enabled: true, profiles: Object.fromEntries(profiles.map(p => [p.id, p.revision])) },
      runs: { enabled: true, allow_edge: false, signing_private_key: secrets['run-signing-key'], signing_key_id: 'run-1',
        manifest: goManifest, release_file: '', release_public_key: '',
        profiles: Object.fromEntries(profiles.map(p => [p.id, { prompt_bundle_digest: p.promptBundleDigest, skills_digest: p.skillsDigest,
          tool_catalog_revision: p.toolCatalogRevision, budgets }])) },
      gateway: { enabled: true, result_directory: '/app/data/agent-tool-results' },
      model_probe: { enabled: true, bridge_token: secrets['bridge-token'], runtime_url: 'http://127.0.0.1:29091' } } }
    const provenance = { schema_version: 1, deployment: 'standalone-local-build', image_digest: imageDigest,
      image_digest_kind: 'docker-image-id', image_digest_source: 'docker image inspect --format {{.Id}}',
      signed_release: false, manifest, profile_revisions: Object.fromEntries(profiles.map(p => [p.id, p.revision])),
      budgets, meter_sha256: meterHash, meter_qualification: 'requires-route-calibration' }
    const contents = { 'runtime.json': json(runtime), 'agent-fragment.json': json(fragment), 'input-meter.mjs': meter,
      'standalone-provenance.json': json(provenance) }
    for (const name of generatedFiles) {
      if (initial) await saveNew(join(config, name), contents[name])
      else await replacePrivate(join(config, name), contents[name])
    }
    return { status: initial ? 'initialized' : 'refreshed', mode: 'production', image_digest_kind: provenance.image_digest_kind,
      meter: 'candidate_requires_route_calibration' }
  } finally { await rm(lock, { recursive: true }) }
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  try {
    const [output, option, imageDigest, ...extra] = process.argv.slice(2)
    if (option !== '--image-digest' || extra.length) fail()
    process.stdout.write(json(await provisionStandalone(output, imageDigest)))
  } catch { process.stderr.write('agent_standalone_configuration_invalid\n'); process.exitCode = 1 }
}
