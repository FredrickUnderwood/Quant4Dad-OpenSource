import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtemp, rm, writeFile, mkdir, symlink, readFile, realpath } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { generateKeyPairSync, sign } from 'node:crypto'
import { canonical, verifyRelease, initialize, active, stage, activate, rollback, statePath, inventory } from '../../../../src/operations/generations.mjs'

const release = version => ({ schema_version: 1, version, channel: 'verified', image: 'registry.example/q4d@sha256:' + 'a'.repeat(64),
  adapter_version: 'v1', q4d_version: 'v1', dsh_version: '0.1.2-alpha.5', bridge_protocol: 1, session_format: 0,
  session_binding_format: 1, event_journal_format: 1, meter_sha256: 'sha256:' + 'b'.repeat(64) })
async function setup(t) {
  const root = await realpath(await mkdtemp(join(tmpdir(), 'q4d-generations-'))); t.after(() => rm(root, { recursive: true, force: true }))
  await initialize(root, 'g1', release('v1'))
  await writeFile(join(statePath(root, 'g1'), 'request-index.jsonl'), '{"identity":"original"}\n', { mode: 0o600 })
  return root
}
test('release signature, explicit edge opt-in and exact formats gate the candidate', () => {
  const { privateKey, publicKey } = generateKeyPairSync('ed25519'), r = release('v1')
  const signed = value => ({ release: value, signature: sign(null, Buffer.from(canonical(value)), privateKey).toString('base64url') })
  const key = publicKey.export({ type: 'spki', format: 'pem' })
  assert.deepEqual(verifyRelease(signed(r), key), { ...r, signature: signed(r).signature })
  assert.throws(() => verifyRelease({ ...signed(r), release: { ...r, image: 'elsewhere:latest' } }, key))
  assert.throws(() => verifyRelease(signed({ ...r, session_format: 1 }), key))
  assert.throws(() => verifyRelease(signed({ ...r, channel: 'edge' }), key))
  assert.equal(verifyRelease(signed({ ...r, channel: 'edge' }), key, true).channel, 'edge')
})
test('copy-on-upgrade preserves original and backup; atomic switch and rollback do not rewind new writes', async t => {
  const root = await setup(t)
  await stage(root, 'g2', release('v2'))
  assert.equal((await active(root)).generation, 'g1')
  assert.deepEqual(await inventory(statePath(root, 'g1')), await inventory(statePath(root, 'g2')))
  assert.equal(await readFile(join(root, 'backups/g2/state/request-index.jsonl'), 'utf8'), '{"identity":"original"}\n')
  await activate(root, 'g2', release('v2')); assert.equal((await active(root)).generation, 'g2')
  await rollback(root); assert.equal((await active(root)).generation, 'g1')
  await activate(root, 'g2', release('v2'))
  await writeFile(join(statePath(root, 'g2'), 'new-business-call.jsonl'), 'new committed identity\n', { mode: 0o600 })
  await assert.rejects(rollback(root), /rollback_would_lose_writes/)
  assert.equal((await active(root)).generation, 'g2')
})
test('live/unclean writers, concurrent operators, symlinks and corrupted candidates fail without switching', async t => {
  const root = await setup(t), path = statePath(root, 'g1')
  await mkdir(join(path, '.runtime-lock'), { mode: 0o700 })
  await assert.rejects(stage(root, 'locked', release('v2')), /runtime_running_or_unclean/)
  await rm(join(path, '.runtime-lock'), { recursive: true })
  await mkdir(join(root, '.maintenance-lock'), { mode: 0o700 })
  await assert.rejects(stage(root, 'busy', release('v2')), /maintenance_busy/)
  await rm(join(root, '.maintenance-lock'), { recursive: true })
  await symlink('/etc/passwd', join(path, 'outside'))
  await assert.rejects(stage(root, 'symlink', release('v2')))
  await rm(join(path, 'outside'))
  await stage(root, 'g2', release('v2'))
  await writeFile(join(statePath(root, 'g2'), 'request-index.jsonl'), 'corrupt')
  await assert.rejects(activate(root, 'g2', release('v2')), /candidate_modified/)
  assert.equal((await active(root)).generation, 'g1')
})
