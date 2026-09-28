#!/usr/bin/env node
import { readFile, writeFile, lstat, realpath } from 'node:fs/promises'
import { resolve, join } from 'node:path'
import { createHash } from 'node:crypto'
import { verifyRelease, initialize, stage, activate, rollback, active, inventory, statePath, canonical } from '../src/operations/generations.mjs'
import { resolveProfiles } from '../profiles/defaults.mjs'
import { exportArtifact } from '../src/operations/artifact.mjs'

// Offline commands never run a candidate image or arbitrary shell commands.
// The private Bridge commands use mounted tokens, never command-line secrets.
const json = async path => { const b = await readFile(path); if (b.length > 16 * 1024 * 1024) throw new Error(); return JSON.parse(b) }
async function journalRows(path) {
  const stat = await lstat(path)
  if (!stat.isFile() || stat.size > 16 * 1024 * 1024) throw new Error()
  const text = await readFile(path, 'utf8')
  if (text && !text.endsWith('\n')) throw new Error()
  return text.split('\n').filter(Boolean).map((line, index) => {
    const row = JSON.parse(line)
    if (row.sequence !== index + 1 || row.checksum !== createHash('sha256').update(JSON.stringify(row.data)).digest('hex')) throw new Error()
    return row.data
  })
}
async function release(path, key) { return verifyRelease(await json(path), await readFile(key, 'utf8'), process.env.Q4D_ALLOW_EDGE === '1') }
async function bridge(origin, tokenFile, path, method = 'GET') {
  const url = new URL(origin)
  if (!['http:', 'https:'].includes(url.protocol) || !['127.0.0.1', '[::1]', 'localhost'].includes(url.hostname) ||
      url.username || url.password || url.pathname !== '/' || url.search || url.hash) throw new Error()
  const stat = await lstat(tokenFile)
  if (!stat.isFile() || stat.uid !== process.getuid?.() || (stat.mode & 0o7777) !== 0o600 || stat.size > 258 || await realpath(tokenFile) !== resolve(tokenFile)) throw new Error()
  const token = (await readFile(tokenFile, 'utf8')).trimEnd()
  if (!/^[A-Za-z0-9_.~+/=-]{32,256}$/.test(token)) throw new Error()
  const response = await fetch(new URL('/q4d/v1/' + path, url), { method, headers: { authorization: 'Bearer ' + token, 'content-type': 'application/json' },
    ...(method === 'POST' ? { body: '{}' } : {}), redirect: 'error', signal: AbortSignal.timeout(10000) })
  const body = await response.text()
  if (!response.ok || body.length > 2 * 1024 * 1024) throw new Error()
  return JSON.parse(body)
}
try {
  const [command, ...args] = process.argv.slice(2)
  let result
  if (command === 'export-artifact' && args.length === 1) result = await exportArtifact(args[0])
  else if (command === 'verify-release' && args.length === 2) result = await release(...args)
  else if (command === 'init' && args.length === 4) { await initialize(resolve(args[0]), args[1], await release(args[2], args[3])); result = { status: 'initialized' } }
  else if (command === 'stage' && args.length === 4) result = await stage(resolve(args[0]), args[1], await release(args[2], args[3]))
  else if (command === 'activate' && args.length === 4) {
    const root = resolve(args[0]), mark = await json(join(root, 'generations', args[1], 'verified.json'))
    const snapshot = await inventory(statePath(root, args[1])), r = await release(args[2], args[3])
    if (mark.snapshot_sha256 !== createHash('sha256').update(canonical(snapshot)).digest('hex') || canonical(mark.release) !== canonical(r)) throw new Error()
    await activate(root, args[1], r); result = { status: 'activated', generation: args[1] }
  } else if (command === 'rollback' && args.length === 1) result = await rollback(resolve(args[0]))
  else if (command === 'status' && args.length === 1) { const value = await active(resolve(args[0])); result = { generation: value.generation, release: value.release } }
  else if (['drain', 'resume', 'maintenance'].includes(command) && args.length === 2) {
    result = await bridge(args[0], args[1], 'maintenance' + (command === 'maintenance' ? '' : '/' + command), command === 'maintenance' ? 'GET' : 'POST')
  } else if (command === 'verify-candidate' && args.length === 4) {
    const [directory, generation, origin, token] = args, root = resolve(directory)
    const r = await json(join(root, 'generations', generation, 'release.json'))
    const health = await bridge(origin, token, 'health'), caps = await bridge(origin, token, 'capabilities')
    if (health.status !== 'ready' || ['adapter_version', 'dsh_version', 'bridge_protocol', 'session_format', 'event_journal_format', 'session_binding_format'].some(k => caps[k] !== r[k]) ||
        caps.runtime_manifest?.agent_image_digest !== r.image.split('@')[1] || caps.runtime_manifest?.agent_runtime_version !== r.version || caps.runtime_manifest?.q4d_version !== r.q4d_version ||
        caps.backend !== 'cordis' || caps.profiles_aligned !== true || !caps.features?.event_replay || !caps.features?.compaction_events || !caps.features?.approval) throw new Error()
    const status = await bridge(origin, token, 'maintenance/drain', 'POST')
    if (status.active_runs !== 0 || status.pending_operations !== 0) throw new Error()
    const state = statePath(root, generation)
    const sessions = (await journalRows(join(state, 'session-bindings.jsonl'))).filter(r => r.kind === 'materialized').slice(-32)
    for (const session of sessions) {
      if (!/^[A-Za-z0-9_-]{1,128}$/.test(session.session_id)) throw new Error()
      const transcript = await bridge(origin, token, 'sessions/' + session.session_id + '?limit=1')
      if (transcript.session_id !== session.session_id || !Array.isArray(transcript.items)) throw new Error()
    }
    const runs = (await journalRows(join(state, 'request-index.jsonl'))).slice(-32)
    for (const run of runs) {
      if (!/^[A-Za-z0-9_-]{1,128}$/.test(run.run_id)) throw new Error()
      const projected = await bridge(origin, token, 'runs/' + run.run_id)
      if (projected.run_id !== run.run_id || projected.terminal !== true) throw new Error()
    }
    // Candidate must already be a stopped, unchanged copy before activation.
    // Stop its container, then use seal-candidate to bind this health proof to
    // the exact cold data inventory. A unique generation is never reused.
    await writeFile(join(root, 'generations', generation, 'health-proof.json'), JSON.stringify({ release: r, capabilities: caps, cold_sessions: sessions.length, terminal_runs: runs.length }) + '\n', { flag: 'wx', mode: 0o600 })
    result = { status: 'candidate_drained', cold_sessions: sessions.length, terminal_runs: runs.length, next: 'stop candidate, then seal-candidate' }
  } else if (command === 'seal-candidate' && args.length === 2) {
    const root = resolve(args[0]), folder = join(root, 'generations', args[1])
    const proof = await json(join(folder, 'health-proof.json')), snapshot = await inventory(statePath(root, args[1]))
    await writeFile(join(folder, 'verified.json'), JSON.stringify({ release: proof.release,
      snapshot_sha256: createHash('sha256').update(canonical(snapshot)).digest('hex') }) + '\n', { flag: 'wx', mode: 0o600 })
    result = { status: 'candidate_sealed' }
  } else if (command === 'render-config' && args.length === 4) {
    const root = resolve(args[0]), generation = args[1], template = await json(args[2])
    statePath(root, generation)
    const r = await json(join(root, 'generations', generation, 'release.json'))
    const parts = template.stateDirectory.split('/generations/')
    if (parts.length !== 2) throw new Error()
    template.stateDirectory = parts[0] + '/generations/' + generation + '/state'
    template.manifest = { q4d_version: r.q4d_version, agent_image_digest: r.image.split('@')[1], agent_runtime_version: r.version,
      adapter_version: r.adapter_version, dsh_version: r.dsh_version, bridge_protocol: r.bridge_protocol }
    template.inputMeter.sha256 = r.meter_sha256
    resolveProfiles(template)
    template.profileSource ??= 'repository'
    await writeFile(args[3], JSON.stringify(template, null, 2) + '\n', { flag: 'wx', mode: 0o600 })
    result = { status: 'configuration_rendered', generation }
  } else throw new Error('agent_update_usage')
  process.stdout.write(JSON.stringify(result) + '\n')
} catch (e) { process.stderr.write(/^agent_update_[a-z_]+$/.test(e?.message) ? e.message + '\n' : 'agent_update_failed\n'); process.exitCode = 1 }
