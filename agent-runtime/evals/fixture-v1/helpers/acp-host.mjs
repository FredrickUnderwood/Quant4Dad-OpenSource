import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { once } from 'node:events'
import { mkdtemp, realpath, rm } from 'node:fs/promises'
import { createRequire } from 'node:module'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { Readable, Writable } from 'node:stream'
import { setTimeout as delay } from 'node:timers/promises'
import { fileURLToPath } from 'node:url'
import { client, methods, ndJsonStream, PROTOCOL_VERSION } from '@agentclientprotocol/sdk'

const runtimeRoot = fileURLToPath(new URL('../../../', import.meta.url))
const source = process.env.Q4D_DSH_SOURCE_DIR
assert.ok(source, 'Set Q4D_DSH_SOURCE_DIR to the checksummed, installed source archive')
const requireUpstream = createRequire(join(resolve(source), 'package.json'))
const tsxLoader = requireUpstream.resolve('tsx/esm')
const ownedHosts = new WeakMap()

export async function launch(t, directory, permission, options = {}) {
  const child = spawn(process.execPath, [
    '--import', tsxLoader, join(runtimeRoot, 'fixtures/minimal-acp-host.ts'),
  ], {
    cwd: directory,
    // Intentionally do not inherit provider keys, proxy settings, or developer config.
    env: {
      PATH: process.env.PATH,
      TSX_TSCONFIG_PATH: join(resolve(source), 'tsconfig.json'),
      Q4D_T00_PERSISTENCE_ROOT: join(directory, 'sessions'),
      Q4D_T00_MODEL_CALL_LOG: join(directory, 'model-calls.jsonl'),
      Q4D_T00_PERMISSION: permission ? '1' : '0',
      DSH_TELEMETRY_DISABLED: '1',
      ...options.env,
    },
    stdio: ['pipe', 'pipe', 'pipe', 'ipc'],
  })
  let stderr = ''
  child.stderr.on('data', chunk => { stderr += chunk })
  const exited = once(child, 'exit')
  await once(child, 'spawn')
  const updates = []
  const permissions = []
  const controlEvents = []
  const pendingControls = new Map()
  let controlSequence = 0
  child.on('message', message => {
    if (message.event) {
      controlEvents.push(message)
      void options.onEvent?.(message, host)
    } else {
      const pending = pendingControls.get(message.id)
      pendingControls.delete(message.id)
      if (message.error) pending?.reject(new Error(message.error))
      else pending?.resolve(message.result)
    }
  })
  child.on('exit', () => {
    for (const pending of pendingControls.values()) pending.reject(new Error('fixture control process exited'))
    pendingControls.clear()
  })
  const app = client({ name: 'q4d-t00' })
    .onNotification(methods.client.session.update, async ({ params }) => { updates.push(params) })
    .onRequest(methods.client.session.requestPermission, async ({ params }) => {
      permissions.push(params)
      return permission?.(params) ?? { outcome: { outcome: 'cancelled' } }
    })
  const connection = app.connect(ndJsonStream(Writable.toWeb(child.stdin), Readable.toWeb(child.stdout)))
  let closed = false
  const host = {
    updates, permissions, controlEvents,
    control: async (method, params) => {
      const id = ++controlSequence
      const result = Promise.withResolvers()
      pendingControls.set(id, result)
      child.send({ id, method, params })
      return result.promise
    },
    request: async (method, params) => {
      try { return await connection.agent.request(method, params) }
      catch (error) { throw new Error(`${method.method ?? String(method)}: ${error.message}\n${stderr}`, { cause: error }) }
    },
    cancel: sessionId => connection.agent.notify(methods.agent.session.cancel, { sessionId }),
    waitUpdate: async predicate => {
      const deadline = Date.now() + 10_000
      while (Date.now() < deadline) {
        const found = updates.find(({ update }) => predicate(update))
        if (found) return found
        if (child.exitCode !== null) throw new Error(`ACP exited: ${stderr}`)
        await delay(10)
      }
      throw new Error(`Timed out waiting for ACP update: ${stderr}`)
    },
    close: async () => {
      if (closed) return
      closed = true
      child.stdin.end()
      const killTimer = setTimeout(() => child.kill('SIGKILL'), 5000)
      const [code, signal] = await exited.finally(() => clearTimeout(killTimer))
      assert.equal(code, 0, `ACP exit ${signal}: ${stderr}`)
    },
  }
  ownedHosts.get(t).push(host)
  t.after(() => host.close())
  const initialized = await host.request(methods.agent.initialize, { protocolVersion: PROTOCOL_VERSION, clientCapabilities: {} })
  assert.equal(initialized.protocolVersion, PROTOCOL_VERSION)
  assert.deepEqual(initialized.agentCapabilities.sessionCapabilities, { close: {}, list: {}, resume: {} })
  assert.deepEqual(initialized.agentCapabilities.promptCapabilities, { image: false, audio: false, embeddedContext: false })
  return host
}

export async function fixtureDirectory(t) {
  const directory = await realpath(await mkdtemp(join(tmpdir(), 'q4d-t00-acp-')))
  ownedHosts.set(t, [])
  t.after(async () => {
    // Close every process before deleting its persistence root, including on
    // failed assertions or timeouts. The per-host cleanup remains idempotent.
    const results = await Promise.allSettled(ownedHosts.get(t).map(host => host.close()))
    await rm(directory, { recursive: true, force: true })
    const failed = results.find(result => result.status === 'rejected')
    if (failed) throw failed.reason
  })
  return directory
}
export function mcpServers(directory) {
  return [{ name: 'q4d', command: process.execPath, args: [join(runtimeRoot, 'fixtures/mcp-q4d-server.mjs')],
    env: [{ name: 'Q4D_T00_MCP_CALL_LOG', value: join(directory, 'calls.jsonl') }] }]
}
export function prompt(host, sessionId, text) {
  return host.request(methods.agent.session.prompt, { sessionId, prompt: [{ type: 'text', text }] })
}
