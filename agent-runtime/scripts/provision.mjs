import { readFile, writeFile, mkdir, realpath } from 'node:fs/promises'
import { resolve, join } from 'node:path'
import { randomBytes, generateKeyPairSync, createHash, createPublicKey } from 'node:crypto'
import { initialize, verifyRelease } from '../src/operations/generations.mjs'
import { defaultProfiles } from '../profiles/defaults.mjs'

try {
  if (process.argv.length !== 6) throw new Error()
  const [output, releaseFile, publicKeyFile, mode] = process.argv.slice(2)
  if (!['local-validation', 'production'].includes(mode)) throw new Error()
  const publicKey = await readFile(publicKeyFile, 'utf8')
  const r = verifyRelease(JSON.parse(await readFile(releaseFile, 'utf8')), publicKey, process.env.Q4D_ALLOW_EDGE === '1')
  const root = resolve(output)
  await mkdir(root, { mode: 0o700 }) // never overwrite credentials or generations
  if (await realpath(root) !== root) throw new Error()
  const config = join(root, 'config'), data = join(root, 'data')
  await mkdir(config, { mode: 0o700 }); await mkdir(data, { mode: 0o700 })
  const save = (name, value) => writeFile(join(config, name), value, { flag: 'wx', mode: 0o600 })
  const hash = value => 'sha256:' + createHash('sha256').update(value).digest('hex')
  const meter = await readFile(new URL('../src/meter/byte-budget.mjs', import.meta.url))
  if (hash(meter) !== r.meter_sha256) throw new Error()
  await save('input-meter.mjs', meter)
  for (const token of ['control', 'bridge', 'mcp']) await save(token + '-token', randomBytes(48).toString('base64url') + '\n')
  const keys = generateKeyPairSync('ed25519'), jwk = keys.privateKey.export({ format: 'jwk' })
  await save('run-signing-key', Buffer.concat([Buffer.from(jwk.d, 'base64url'), Buffer.from(jwk.x, 'base64url')]).toString('base64url') + '\n')
  await save('release-public-key.pem', publicKey)
  const profiles = defaultProfiles()
  const budgets = JSON.parse(await readFile(new URL('../fixtures/long-run-budgets.json', import.meta.url), 'utf8'))
  const manifest = { q4d_version: r.q4d_version, agent_image_digest: r.image.split('@')[1], agent_runtime_version: r.version,
    adapter_version: r.adapter_version, dsh_version: r.dsh_version, bridge_protocol: 1 }
  await initialize(data, 'g-initial', r)
  const container = mode === 'production', configPath = container ? '/run/q4d-config' : config, dataPath = container ? '/var/lib/q4d-agent' : data
  const snapshots = container ? '/run/q4d-snapshots' : join(root, 'snapshots')
  if (!container) await mkdir(snapshots, { mode: 0o700 })
  await save('runtime.json', JSON.stringify({ schemaVersion: 1, mode, profileSource: 'repository', listen: { host: '127.0.0.1', port: 29091 },
    bootstrapURL: 'http://127.0.0.1:8080/internal/v1/agent/bootstrap', stateDirectory: dataPath + '/generations/g-initial/state',
    snapshotDirectory: snapshots, profiles, manifest, inputMeter: { bundlePath: configPath + '/input-meter.mjs', sha256: r.meter_sha256 } }, null, 2) + '\n')
  const secret = async name => (await readFile(join(config, name), 'utf8')).trimEnd()
  const goManifest = { ...manifest }; delete goManifest.bridge_protocol
  await save('agent-fragment.json', JSON.stringify({ agent: { enabled: true, profile_source: 'repository',
    bootstrap: { enabled: true, control_token: await secret('control-token'), mcp_token: await secret('mcp-token'), mcp_url: 'http://127.0.0.1:8080/internal/mcp', capability_issuer: 'quant4dad', capability_public_keys: { 'run-1': jwk.x } },
    sessions: { enabled: true, profiles: Object.fromEntries(profiles.map(p => [p.id, p.revision])) },
    runs: { enabled: true, allow_edge: r.channel === 'edge', signing_private_key: await secret('run-signing-key'), signing_key_id: 'run-1', manifest: goManifest, release_file: dataPath + '/active-generation.json',
      release_public_key: createPublicKey(publicKey).export({ format: 'jwk' }).x,
      profiles: Object.fromEntries(profiles.map(p => [p.id, { prompt_bundle_digest: p.promptBundleDigest, skills_digest: p.skillsDigest,
        tool_catalog_revision: '', budgets }])) },
    gateway: { enabled: true, result_directory: container ? '/app/data/agent-tool-results' : join(root, 'tool-results') },
    model_probe: { enabled: true, bridge_token: await secret('bridge-token'), runtime_url: 'http://127.0.0.1:29091' } } }, null, 2) + '\n')
  process.stdout.write(JSON.stringify({ status: 'provisioned', mode, generation: 'g-initial', meter: 'candidate_requires_route_calibration' }) + '\n')
} catch { process.stderr.write('agent_provision_failed\n'); process.exitCode = 1 }
