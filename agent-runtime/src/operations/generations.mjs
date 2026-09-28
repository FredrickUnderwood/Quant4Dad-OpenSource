import { constants } from 'node:fs'
import { mkdir, lstat, realpath, readdir, open, rename, rm, statfs } from 'node:fs/promises'
import { join, resolve, isAbsolute } from 'node:path'
import { createHash, verify, createPublicKey } from 'node:crypto'

const fail = code => { throw new Error('agent_update_' + code) }
const digest = bytes => 'sha256:' + createHash('sha256').update(bytes).digest('hex')
const id = value => typeof value === 'string' && /^[a-zA-Z0-9_-]{1,80}$/.test(value)
const version = value => typeof value === 'string' && /^[a-zA-Z0-9_.+-]{1,128}$/.test(value)
export function canonical(value) {
  if (Array.isArray(value)) return '[' + value.map(canonical).join(',') + ']'
  if (value && typeof value === 'object') return '{' + Object.keys(value).sort().map(k => JSON.stringify(k) + ':' + canonical(value[k])).join(',') + '}'
  return JSON.stringify(value)
}
export function verifyRelease(document, trustedKey, allowEdge = false) {
  try {
    const { release: r, signature } = document
    const key = createPublicKey(trustedKey)
    if (key.asymmetricKeyType !== 'ed25519' || Object.keys(document).sort().join() !== 'release,signature' ||
        !r || r.schema_version !== 1 || !version(r.version) || !['verified', ...(allowEdge ? ['edge'] : [])].includes(r.channel) ||
        !/^[a-z0-9][a-z0-9./:_-]*@sha256:[0-9a-f]{64}$/.test(r.image) || r.dsh_version !== '0.1.2-alpha.5' ||
        r.bridge_protocol !== 1 || r.session_format !== 0 || r.event_journal_format !== 1 || r.session_binding_format !== 1 ||
        !version(r.adapter_version) || !version(r.q4d_version) || !/^sha256:[0-9a-f]{64}$/.test(r.meter_sha256) ||
        typeof signature !== 'string' || !/^[A-Za-z0-9_-]{86}$/.test(signature) ||
        !verify(null, Buffer.from(canonical(r)), key, Buffer.from(signature, 'base64url'))) fail('release_invalid')
    return { ...structuredClone(r), signature }
  } catch { fail('release_invalid') }
}

async function directory(path) {
  if (!isAbsolute(path) || resolve(path) !== path || await realpath(path) !== path) fail('path_invalid')
  const s = await lstat(path)
  if (!s.isDirectory() || s.isSymbolicLink() || s.uid !== process.getuid?.() || (s.mode & 0o7777) !== 0o700) fail('path_invalid')
}
async function bytes(path, max = 128 * 1024 * 1024) {
  const file = await open(path, constants.O_RDONLY | constants.O_NOFOLLOW | constants.O_NONBLOCK)
  try {
    const s = await file.stat()
    if (!s.isFile() || s.nlink !== 1 || s.size > max || s.uid !== process.getuid?.()) fail('file_invalid')
    const body = Buffer.alloc(s.size + 1)
    let n = 0
    while (n < body.length) { const { bytesRead } = await file.read(body, n, body.length - n, null); if (!bytesRead) break; n += bytesRead }
    if (n !== s.size) fail('source_changed')
    return body.subarray(0, n)
  } finally { await file.close() }
}
async function write(path, body) {
  const file = await open(path, 'wx', 0o600)
  try { await file.writeFile(body); await file.sync() } finally { await file.close() }
}
async function syncDirectory(path) {
  const file = await open(path, 'r')
  try { await file.sync() } finally { await file.close() }
}
async function exists(path) { try { await lstat(path); return true } catch (e) { if (e.code === 'ENOENT') return false; throw e } }
export async function stopped(path) {
  await directory(path)
  if (await exists(join(path, '.runtime-lock'))) fail('runtime_running_or_unclean')
}
export async function inventory(path) {
  await stopped(path)
  const items = [], budget = { bytes: 0, entries: 0 }
  async function walk(folder, prefix = '', depth = 0) {
    if (depth > 32) fail('snapshot_too_large')
    const names = (await readdir(folder)).sort()
    for (const name of names) {
      if (++budget.entries > 20000 || /[\p{Cc}]/u.test(name)) fail('snapshot_too_large')
      const absolute = join(folder, name), relative = prefix + name, s = await lstat(absolute)
      if (s.isDirectory() && !s.isSymbolicLink()) { await directory(absolute); items.push({ path: relative, directory: true }); await walk(absolute, relative + '/', depth + 1) }
      else {
        const body = await bytes(absolute)
        budget.bytes += body.length
        if (budget.bytes > 1024 ** 3) fail('snapshot_too_large')
        items.push({ path: relative, bytes: body.length, sha256: digest(body) })
      }
    }
  }
  await walk(path)
  return { items, bytes: budget.bytes }
}
async function copy(source, target) {
  const before = await inventory(source)
  const disk = await statfs(resolve(target, '..'))
  if (disk.bavail * disk.bsize < before.bytes * 2 + 64 * 1024 * 1024) fail('space_required')
  await mkdir(target, { mode: 0o700 })
  try {
    for (const item of before.items) {
      const dest = join(target, item.path)
      if (item.directory) await mkdir(dest, { mode: 0o700 })
      else { const body = await bytes(join(source, item.path)); if (digest(body) !== item.sha256) fail('source_changed'); await write(dest, body) }
    }
    if (canonical(await inventory(source)) !== canonical(before) || canonical(await inventory(target)) !== canonical(before)) fail('source_changed')
    await syncDirectory(target)
    return before
  } catch (e) { await rm(target, { recursive: true, force: true }); throw e }
}

// The maintenance lock serializes operators, separate from the Runtime writer
// lock. A crash retains it; explicit recovery follows container inspection.
export async function withStore(root, operation) {
  await directory(root)
  const lock = join(root, '.maintenance-lock')
  try { await mkdir(lock, { mode: 0o700 }) } catch { fail('maintenance_busy') }
  try { return await operation() } finally { await rm(lock, { recursive: true }); await syncDirectory(root) }
}
export async function active(root) {
  const value = JSON.parse(await bytes(join(root, 'active-generation.json'), 16 * 1024 * 1024))
  if (!id(value.generation) || !value.release) fail('active_invalid')
  return value
}
export function statePath(root, generation) { if (!id(generation)) fail('generation_invalid'); return join(root, 'generations', generation, 'state') }
async function atomicActive(root, value) {
  const temporary = join(root, 'active-generation.next')
  await write(temporary, JSON.stringify(value) + '\n')
  await rename(temporary, join(root, 'active-generation.json'))
  await syncDirectory(root)
}
export async function initialize(root, generation, release) {
  return withStore(root, async () => {
    if (await exists(join(root, 'active-generation.json')) || !id(generation)) fail('already_initialized')
    await mkdir(join(root, 'generations'), { mode: 0o700 })
    await mkdir(join(root, 'backups'), { mode: 0o700 })
    const folder = join(root, 'generations', generation)
    await mkdir(folder, { mode: 0o700 }); await mkdir(join(folder, 'state'), { mode: 0o700 })
    await write(join(folder, 'release.json'), JSON.stringify(release) + '\n')
    await atomicActive(root, { generation, release })
  })
}
export async function stage(root, generation, release) {
  return withStore(root, async () => {
    const previous = await active(root)
    if (!id(generation) || generation === previous.generation) fail('generation_invalid')
    const folder = join(root, 'generations', generation), backup = join(root, 'backups', generation)
    await mkdir(folder, { mode: 0o700 }); await mkdir(backup, { mode: 0o700 })
    const snapshot = await copy(statePath(root, previous.generation), join(backup, 'state'))
    await write(join(backup, 'manifest.json'), JSON.stringify({ previous, snapshot }) + '\n')
    await copy(join(backup, 'state'), join(folder, 'state'))
    await write(join(folder, 'release.json'), JSON.stringify(release) + '\n')
    await write(join(folder, 'candidate.json'), JSON.stringify({ previous, snapshot }) + '\n')
    await syncDirectory(folder); await syncDirectory(backup)
    return { generation, previous: previous.generation, files: snapshot.items.length, bytes: snapshot.bytes }
  })
}
export async function activate(root, generation, release) {
  return withStore(root, async () => {
    const previous = await active(root), folder = join(root, 'generations', generation)
    const candidate = JSON.parse(await bytes(join(folder, 'candidate.json'), 16 * 1024 * 1024))
    if (canonical(candidate.previous) !== canonical(previous) || canonical(JSON.parse(await bytes(join(folder, 'release.json'), 65536))) !== canonical(release)) fail('candidate_stale')
    if (canonical(await inventory(statePath(root, previous.generation))) !== canonical(candidate.snapshot) ||
        canonical(await inventory(statePath(root, generation))) !== canonical(candidate.snapshot)) fail('candidate_modified')
    await atomicActive(root, { generation, release, previous: { generation: previous.generation, release: previous.release },
      initial_snapshot_sha256: digest(canonical(candidate.snapshot)) })
  })
}
export async function rollback(root) {
  return withStore(root, async () => {
    const current = await active(root)
    if (!current.previous || digest(canonical(await inventory(statePath(root, current.generation)))) !== current.initial_snapshot_sha256) fail('rollback_would_lose_writes')
    if (digest(canonical(await inventory(statePath(root, current.previous.generation)))) !== current.initial_snapshot_sha256) fail('previous_modified')
    await atomicActive(root, current.previous)
    return current.previous
  })
}
