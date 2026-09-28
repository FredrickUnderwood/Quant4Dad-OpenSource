import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { once } from 'node:events'
import { createInterface } from 'node:readline'
import { rm } from 'node:fs/promises'
import { resolve } from 'node:path'
import { launchConfig } from './launch-config.mjs'

/** Actual offline launcher/main/listener. Only the model HTTP server and the
 * explicit metering bundle are synthetic; no policy/readiness override or IPC. */
export async function runtimeProcess(t, options) {
  const f = await launchConfig(options)
  const child = spawn('/bin/bash', [resolve(import.meta.dirname, '../../../scripts/start-runtime.sh'), f.file], {
    env: { PATH: process.env.PATH, Q4D_DSH_CACHE_DIR: process.env.Q4D_DSH_CACHE_DIR,
      OPENAI_API_KEY: 'ambient-private-key-marker', HTTP_PROXY: 'http://127.0.0.1:1', ...f.env },
    cwd: f.directory, stdio: ['ignore', 'pipe', 'pipe'],
  })
  const ready = Promise.withResolvers(), exited = once(child, 'exit')
  let logs = ''
  child.stderr.on('data', bytes => { logs += bytes })
  const lines = createInterface({ input: child.stdout })
  lines.on('line', line => {
    logs += line + '\n'
    if (!line.startsWith('{')) return
    const message = JSON.parse(line)
    if (message.event === 'agent_runtime_ready') ready.resolve(message)
  })
  child.on('error', ready.reject)
  child.on('exit', () => { lines.close(); ready.reject(new Error('runtime process exited: ' + logs)) })
  async function close() {
    if (child.exitCode !== null || child.signalCode !== null) return
    child.kill('SIGTERM')
    const timer = setTimeout(() => child.kill('SIGKILL'), 18000)
    const [code, signal] = await exited.finally(() => clearTimeout(timer))
    assert.equal(code, 0, `runtime process ${signal}: ${logs}`)
  }
  t.after(async () => { try { await close() } finally { if (!options.root) await rm(f.directory, { recursive: true, force: true }) } })
  const timeout = setTimeout(() => { child.kill('SIGKILL'); ready.reject(new Error('runtime startup timeout: ' + logs)) }, 15000)
  const info = await ready.promise.finally(() => clearTimeout(timeout))
  assert.equal(info.mode, 'local-validation')
  return { directory: f.directory, port: info.port, close, logs: () => logs,
    kill: async () => { child.kill('SIGKILL'); await exited } }
}
