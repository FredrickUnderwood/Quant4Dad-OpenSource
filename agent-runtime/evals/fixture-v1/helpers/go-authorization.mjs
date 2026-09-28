import assert from 'node:assert/strict'
import { spawn, execFile } from 'node:child_process'
import { once } from 'node:events'
import { mkdtemp, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { createInterface } from 'node:readline'
import { promisify } from 'node:util'
import { runtimeRoot } from './durable-host.mjs'

export async function compileAuthorizationHarness() {
  const directory = await mkdtemp(join(tmpdir(), 'q4d-run-auth-'))
  const binary = join(directory, 'authorization-harness')
  try {
    await promisify(execFile)('go', ['build', '-mod=readonly', '-o', binary, './internal/agentrunauth/testdata/harness'], {
      cwd: resolve(runtimeRoot, '..'), timeout: 120_000, maxBuffer: 1024 * 1024,
    })
  } catch (error) { await rm(directory, { recursive: true, force: true }); throw error }
  return { binary, close: () => rm(directory, { recursive: true, force: true }) }
}

export async function authorizationHarness(t, binary) {
  const child = spawn(binary, [], { stdio: ['pipe', 'pipe', 'pipe'], env: { PATH: process.env.PATH } })
  const exited = once(child, 'exit')
  const ready = Promise.withResolvers()
  const pending = new Map()
  const lines = createInterface({ input: child.stdout })
  let nextID = 0
  child.stderr.resume()
  child.stdin.on('error', () => {})
  lines.on('line', line => {
    try {
      const response = JSON.parse(line)
      if (response.event === 'ready') { ready.resolve(response); return }
      const request = pending.get(response.id)
      pending.delete(response.id)
      if (response.error) request?.reject(new Error(response.error))
      else request?.resolve(response.result)
    } catch { child.kill('SIGKILL') }
  })
  const failed = () => {
    ready.reject(new Error('Go authorization fixture exited'))
    for (const request of pending.values()) request.reject(new Error('Go authorization fixture exited'))
    pending.clear()
  }
  child.on('error', failed)
  child.on('exit', failed)
  t.after(async () => {
    child.stdin.end()
    const timer = setTimeout(() => child.kill('SIGKILL'), 5000)
    const [code] = await exited.finally(() => { clearTimeout(timer); lines.close() })
    assert.equal(code, 0, 'Go authorization fixture did not exit cleanly')
  })
  return { ...await ready.promise, call(method, params) {
    const id = ++nextID
    const request = Promise.withResolvers()
    pending.set(id, request)
    child.stdin.write(JSON.stringify({ id, method, params }) + '\n')
    return request.promise
  } }
}
