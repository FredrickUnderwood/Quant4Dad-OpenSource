import { constants } from 'node:fs'
import { open, realpath, lstat, statfs } from 'node:fs/promises'
import { isAbsolute, resolve, sep } from 'node:path'
import { createHash } from 'node:crypto'
import { bootstrapURL, bootstrapPath, controlTokenPattern, rejectDuplicateMembers } from '../bootstrap/validation.mjs'
import { resolveProfiles } from '../../profiles/defaults.mjs'

const fail = () => { throw new Error('agent_runtime_configuration_invalid') }
const digest = /^sha256:[0-9a-f]{64}$/
const id = /^[A-Za-z0-9_-]{1,128}$/
const version = /^[A-Za-z0-9_.+-]{1,128}$/
const match = (pattern, value) => typeof value === 'string' && !/[\r\n]/.test(value) && pattern.test(value)
const exact = (value, names) => value && typeof value === 'object' && !Array.isArray(value) &&
  Object.keys(value).sort().join(',') === names.split(',').sort().join(',')
const path = value => typeof value === 'string' && isAbsolute(value) && resolve(value) === value && !/[\p{Cc}]/u.test(value)

export async function readOwnedFile(file, maxBytes, privateFile = true) {
  let handle
  try {
    if (!path(file) || await realpath(file) !== file) fail()
    handle = await open(file, constants.O_RDONLY | constants.O_NOFOLLOW | constants.O_NONBLOCK)
    const stat = await handle.stat()
    if (!stat.isFile() || stat.uid !== process.getuid?.() || stat.nlink !== 1 || stat.size > maxBytes ||
        (privateFile ? (stat.mode & 0o7777) !== 0o600 : (stat.mode & 0o7022) !== 0)) fail()
    const buffer = Buffer.alloc(maxBytes + 1)
    let size = 0
    while (size < buffer.length) {
      const { bytesRead } = await handle.read(buffer, size, buffer.length - size, null)
      if (!bytesRead) break
      size += bytesRead
    }
    if (size > maxBytes) fail()
    return buffer.subarray(0, size)
  } catch { fail() } finally { await handle?.close() }
}

export async function validateDirectory(directory, tmpfs = false) {
  try {
    if (!path(directory)) fail()
    const stat = await lstat(directory)
    if (!stat.isDirectory() || stat.isSymbolicLink() || stat.uid !== process.getuid?.() ||
        (stat.mode & 0o7777) !== 0o700 || await realpath(directory) !== directory ||
        (tmpfs && (process.platform !== 'linux' || (await statfs(directory)).type !== 0x01021994))) fail()
  } catch { fail() }
}

/** No inline credentials, endpoint overrides from HTTP, or executable strings.
 * State/tmpfs mounts and files are provisioned by the deployment owner. */
export async function loadLaunchConfig(file, env = process.env) {
  try {
    const text = new TextDecoder('utf-8', { fatal: true }).decode(await readOwnedFile(file, 256 * 1024))
    const value = JSON.parse(text)
    rejectDuplicateMembers(text)
    const { profileSource, ...shape } = value
    if (!(exact(shape, 'schemaVersion,mode,listen,bootstrapURL,stateDirectory,snapshotDirectory,profile,manifest,inputMeter') ||
          exact(shape, 'schemaVersion,mode,listen,bootstrapURL,stateDirectory,snapshotDirectory,profiles,manifest,inputMeter')) ||
        value.schemaVersion !== 1 || !['production', 'local-validation'].includes(value.mode) ||
        !exact(value.listen, 'host,port') || !['127.0.0.1', '::1'].includes(value.listen.host) ||
        !Number.isSafeInteger(value.listen.port) || value.listen.port < (value.mode === 'production' ? 1 : 0) || value.listen.port > 65535) fail()
    bootstrapURL(value.bootstrapURL, bootstrapPath)
    await validateDirectory(value.stateDirectory)
    await validateDirectory(value.snapshotDirectory, value.mode === 'production')
    const [state, snapshots] = [value.stateDirectory, value.snapshotDirectory]
    if (state === snapshots || state.startsWith(snapshots + sep) || snapshots.startsWith(state + sep)) fail()
    const m = value.manifest, meter = value.inputMeter
    const profiles = value.profiles ?? [value.profile]
    if (!Array.isArray(profiles) || !profiles.length || profiles.length > 128 || new Set(profiles.map(p => p?.id)).size !== profiles.length) fail()
    for (const p of profiles) {
      if (!exact(p, 'id,revision,systemPrompt,promptBundleDigest,skillsDigest,toolCatalogRevision') ||
          !match(id, p.id) || !match(digest, p.revision) || !match(digest, p.promptBundleDigest) || !match(digest, p.skillsDigest) ||
          !(match(digest, p.toolCatalogRevision) || (['research', 'strategy_lab', 'pipeline_builder'].includes(p.id) && p.toolCatalogRevision === '')) ||
          typeof p.systemPrompt !== 'string' || !p.systemPrompt.isWellFormed() || !p.systemPrompt.trim() || Buffer.byteLength(p.systemPrompt) > 64 * 1024) fail()
    }
    resolveProfiles(value)
    if (
        !exact(m, 'q4d_version,agent_image_digest,agent_runtime_version,adapter_version,dsh_version,bridge_protocol') ||
        !['q4d_version', 'agent_runtime_version', 'adapter_version'].every(key => match(version, m[key])) ||
        !match(digest, m.agent_image_digest) || m.dsh_version !== '0.1.2-alpha.5' || m.bridge_protocol !== 1 ||
        !exact(meter, 'bundlePath,sha256') || !path(meter.bundlePath) || !match(digest, meter.sha256)) fail()
    // Refuse ambiguous env/file precedence. These exact values match Go's
    // separately configured Bridge and control tokens; neither enters JSON.
    const tokens = []
    for (const name of ['Q4D_AGENT_CONTROL_TOKEN', 'Q4D_AGENT_BRIDGE_TOKEN']) {
      if (Object.hasOwn(env, name) || !env[name + '_FILE']) fail()
      const token = new TextDecoder('utf-8', { fatal: true }).decode(await readOwnedFile(env[name + '_FILE'], 258)).replace(/\r?\n$/, '')
      if (!controlTokenPattern.test(token)) fail()
      tokens.push(token)
    }
    if (tokens[0] === tokens[1]) fail()
    return { ...value, controlToken: tokens[0], bridgeToken: tokens[1] }
  } catch { fail() }
}

export async function loadInputMeter(config) {
  try {
    const bytes = await readOwnedFile(config.bundlePath, 16 * 1024 * 1024, false)
    if ('sha256:' + createHash('sha256').update(bytes).digest('hex') !== config.sha256) fail()
    // Import the exact checked bytes, avoiding a check/read race. The operator
    // supplies a self-contained ESM bundle; relative/bare imports cannot resolve.
    const module = await import('data:text/javascript;base64,' + bytes.toString('base64'))
    if (module.contract !== 'q4d-input-meter-v1' || typeof module.measureInput !== 'function') fail()
    return module.measureInput
  } catch { fail() }
}
