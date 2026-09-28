import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { createHash } from 'node:crypto'
import { once } from 'node:events'
import { mkdtemp, readFile, rm } from 'node:fs/promises'
import { createRequire } from 'node:module'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

export const runtimeRoot = fileURLToPath(new URL('../../../', import.meta.url))
const source = resolve(process.env.Q4D_DSH_SOURCE_DIR)
const loader = createRequire(join(source, 'package.json')).resolve('tsx/esm')
export const bridgeToken = 'q4d-t00-synthetic-bridge-token'

export async function fixture(t) {
  const directory = await mkdtemp(join(tmpdir(), 'q4d-cordis-durable-'))
  const hosts = []
  t.after(async () => {
    await Promise.all(hosts.map(host => host.close()))
    await rm(directory, { recursive: true, force: true })
  })
  return {
    directory,
    async launch(options = {}) {
      const child = spawn(process.execPath, ['--import', loader, join(runtimeRoot, 'fixtures/durable-cordis-host.ts')], {
        cwd: directory,
        env: { PATH: process.env.PATH, TSX_TSCONFIG_PATH: join(source, 'tsconfig.json'),
          Q4D_T00_ROOT: directory, Q4D_T00_BRIDGE_TOKEN: bridgeToken,
          Q4D_T00_MODEL_CALL_LOG: join(directory, 'model-calls.jsonl'), DSH_TELEMETRY_DISABLED: '1', ...options.env },
        stdio: ['ignore', 'pipe', 'pipe', 'ipc'],
      })
      child.stdout.resume()
      let stderr = ''
      child.stderr.on('data', chunk => { stderr += chunk })
      const ready = Promise.withResolvers()
      const exited = once(child, 'exit')
      const requests = new Map()
      let nextId = 0
      child.on('message', message => {
        if (message.event === 'ready') return ready.resolve(message.port)
        if (message.event) { options.onEvent?.(message); return }
        const pending = requests.get(message.id)
        requests.delete(message.id)
        if (message.error) pending?.reject(new Error(message.error))
        else pending?.resolve(message.result)
      })
      child.on('exit', () => {
        ready.reject(new Error(`fixture exited: ${stderr}`))
        for (const pending of requests.values()) pending.reject(new Error(`fixture exited: ${stderr}`))
        requests.clear()
      })
      const host = {
        exited,
        kill() { child.kill('SIGKILL'); return exited },
        call(method, params) {
          const id = ++nextId
          const result = Promise.withResolvers()
          requests.set(id, result)
          child.send({ id, method, params })
          return result.promise
        },
        async close() {
          if (child.exitCode !== null || child.signalCode !== null) return
          child.disconnect()
          const timer = setTimeout(() => child.kill('SIGKILL'), 5000)
          const [code, signal] = await exited.finally(() => clearTimeout(timer))
          assert.equal(code, 0, `fixture exit ${signal}: ${stderr}`)
        },
      }
      hosts.push(host)
      const port = await ready.promise
      host.url = `http://127.0.0.1:${port}`
      host.transcript = (sessionId = 'session-1', query = '') => fetch(`${host.url}/q4d/v1/sessions/${sessionId}${query ? '?' + query : ''}`, {
        headers: { authorization: `Bearer ${bridgeToken}` },
      })
      host.events = (runId, cursor) => fetch(`${host.url}/q4d/v1/runs/${runId}/events`, {
        headers: { authorization: `Bearer ${bridgeToken}`, ...(cursor !== undefined ? { 'Last-Event-ID': cursor } : {}) },
      })
      return host
    },
  }
}

export function request(text = 'query', runId = 'run-1') {
  const content = [{ type: 'text', text }]
  return {
    q4d_session_id: 'session-1', run_id: runId, client_request_id: `client-${runId}`, content,
    request_hash: 'sha256:' + createHash('sha256').update(JSON.stringify({ session_id: 'session-1', content })).digest('hex'),
    execution_envelope_digest: 'sha256:' + 'a'.repeat(64),
  }
}
export async function sse(response) {
  assert.equal(response.status, 200)
  assert.match(response.headers.get('content-type'), /text\/event-stream/)
  const text = await response.text()
  return text.trim() ? text.trim().split('\n\n').map(block => {
    const lines = block.split('\n')
    const event = JSON.parse(lines.find(line => line.startsWith('data: ')).slice(6))
    assert.equal(lines.find(line => line.startsWith('id: ')).slice(4), event.id)
    return event
  }) : []
}
