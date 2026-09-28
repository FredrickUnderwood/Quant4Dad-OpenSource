import { createHash } from 'node:crypto'
import { mkdtemp, mkdir, writeFile, realpath } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

export const digest = 'sha256:' + 'a'.repeat(64)
export const profile = { id: 'text_only', revision: digest, systemPrompt: 'Answer.', promptBundleDigest: digest,
  skillsDigest: digest, toolCatalogRevision: digest }
export const manifest = { q4d_version: '0.0.0', agent_image_digest: digest, agent_runtime_version: 'fixture-v1',
  adapter_version: 'fixture-v1', dsh_version: '0.1.2-alpha.5', bridge_protocol: 1 }
export const meterSource = `export const contract = 'q4d-input-meter-v1'
// Synthetic fixture only. Assert actual provider identity and count all payload
// bytes to exercise changing inputs. This is NOT a production token bound.
export function measureInput(request, context) {
  if (request.provider !== context.provider || request.provider.startsWith('q4d-session-') || !Object.isFrozen(request.messages)) throw new Error('fixture_bad_meter_request')
  if (process.env.OPENAI_API_KEY || process.env.HTTP_PROXY) throw new Error('fixture_ambient_environment')
  if (JSON.stringify(request.messages).includes('reject-meter-fixture')) throw new Error('private-meter-error-marker')
  return Math.ceil(Buffer.byteLength(JSON.stringify(request)) / 4) + 32
}
`

export async function launchConfig({ root, bootstrapURL = 'http://127.0.0.1:1/internal/v1/agent/bootstrap',
  controlToken = 'fixture-control-token-0123456789-abcdef', bridgeToken = 'fixture-bridge-token-0123456789-abcdef',
  selectedProfile = profile, selectedProfiles, selectedManifest = manifest, selectedMeterSource = meterSource } = {}) {
  const directory = root ?? await realpath(await mkdtemp(join(tmpdir(), 'q4d-runtime-launch-')))
  for (const name of ['state', 'snapshots']) await mkdir(join(directory, name), { mode: 0o700, recursive: true })
  const bundlePath = join(directory, 'meter.mjs'), file = join(directory, 'runtime.json')
  const env = { Q4D_AGENT_CONTROL_TOKEN_FILE: join(directory, 'control.token'), Q4D_AGENT_BRIDGE_TOKEN_FILE: join(directory, 'bridge.token') }
  await writeFile(env.Q4D_AGENT_CONTROL_TOKEN_FILE, controlToken + '\n', { mode: 0o600 })
  await writeFile(env.Q4D_AGENT_BRIDGE_TOKEN_FILE, bridgeToken + '\n', { mode: 0o600 })
  await writeFile(bundlePath, selectedMeterSource, { mode: 0o600 })
  const config = { schemaVersion: 1, mode: 'local-validation', listen: { host: '127.0.0.1', port: 0 }, bootstrapURL,
    stateDirectory: join(directory, 'state'), snapshotDirectory: join(directory, 'snapshots'),
    ...(selectedProfiles ? { profiles: selectedProfiles } : { profile: selectedProfile }), manifest: selectedManifest,
    inputMeter: { bundlePath, sha256: 'sha256:' + createHash('sha256').update(selectedMeterSource).digest('hex') } }
  const save = async (value = config) => writeFile(file, JSON.stringify(value), { mode: 0o600 })
  await save()
  return { directory, file, env, config, save }
}
