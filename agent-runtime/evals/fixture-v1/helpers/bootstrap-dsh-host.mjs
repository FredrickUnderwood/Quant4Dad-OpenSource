import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { once } from 'node:events'
import { mkdtemp, realpath, rm } from 'node:fs/promises'
import { createRequire } from 'node:module'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

export async function bootstrapDshHost(t, { url, token, env = {}, fixture = 'bootstrap-dsh-host.ts', root }) {
  const directory = root ?? await realpath(await mkdtemp(join(tmpdir(), 'q4d-bootstrap-dsh-')))
  const source = resolve(process.env.Q4D_DSH_SOURCE_DIR)
  const loader = createRequire(join(source, 'package.json')).resolve('tsx/esm')
  const child = spawn(process.execPath, ['--import', loader,
    fileURLToPath(new URL('../../../fixtures/' + fixture, import.meta.url))], {
    cwd: directory, env: { PATH: process.env.PATH, TSX_TSCONFIG_PATH: join(source, 'tsconfig.json'),
      DSH_TELEMETRY_DISABLED: '1', Q4D_BOOTSTRAP_ROOT: directory, Q4D_BOOTSTRAP_URL: url, Q4D_BOOTSTRAP_TOKEN: token, ...env },
    stdio: ['ignore', 'pipe', 'pipe', 'ipc'],
  })
  let stdout = '', stderr = '', sequence = 0
  child.stdout.on('data', bytes => { stdout += bytes })
  child.stderr.on('data', bytes => { stderr += bytes })
  const ready = Promise.withResolvers(), pending = new Map(), exited = once(child, 'exit')
  child.on('error', ready.reject)
  child.on('message', message => {
    if (message.event === 'ready') { ready.resolve(message); return }
    const request = pending.get(message.id)
    pending.delete(message.id)
    if (message.error) request?.reject(new Error(message.error))
    else request?.resolve(message.result)
  })
  child.on('exit', () => {
    ready.reject(new Error('bootstrap fixture exited: ' + stderr))
    for (const request of pending.values()) request.reject(new Error('bootstrap fixture exited: ' + stderr))
    pending.clear()
  })
  async function close() {
    if (child.exitCode !== null || child.signalCode !== null) return
    child.disconnect()
    const timer = setTimeout(() => child.kill('SIGKILL'), 5000)
    const [code, signal] = await exited.finally(() => clearTimeout(timer))
    assert.equal(code, 0, `bootstrap fixture ${signal}: ${stderr}`)
  }
  t.after(async () => { try { await close() } finally { if (!root) await rm(directory, { recursive: true, force: true }) } })
  const info = await ready.promise
  return { directory, close, port: info.port, kill: async () => { child.kill('SIGKILL'); await exited }, logs: () => stdout + stderr, call(method, params) {
    const id = ++sequence, response = Promise.withResolvers()
    pending.set(id, response); child.send({ id, method, params }); return response.promise
  } }
}
