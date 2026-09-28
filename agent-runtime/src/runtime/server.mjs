import { createServer } from 'node:http'
import { mkdir, writeFile, rm } from 'node:fs/promises'
import { join } from 'node:path'
import { BootstrapSnapshotStore } from '../bootstrap/snapshot-store.mjs'
import { loadLaunchConfig, loadInputMeter } from './launch-config.mjs'

/** Single owner of persistent state and the private loopback listener. A lock
 * left by SIGKILL requires operator reconciliation; never guess another owner's
 * liveness from a PID or silently run a second writer against JSONL state. */
export async function startRuntimeServer(file, createRuntime, env = process.env) {
  let runtime, server, lock, closing
  async function close() {
    if (closing) return closing
    closing = (async () => {
      // Stop admission first. Closing sockets does not cancel a Run; runtime
      // close records interruption and settles admitted work before releasing.
      const stopped = server ? new Promise(resolve => { server.close(resolve); server.closeAllConnections() }) : Promise.resolve()
      try { await runtime?.close(); await stopped }
      catch { throw new Error('agent_runtime_shutdown_failed') }
      // An unsuccessful close retains the lock for explicit reconciliation.
      if (lock) { await rm(lock, { recursive: true }); lock = undefined }
    })()
    return closing
  }
  try {
    const config = await loadLaunchConfig(file, env)
    const measureInput = await loadInputMeter(config.inputMeter)
    const candidate = join(config.stateDirectory, '.runtime-lock')
    await mkdir(candidate, { mode: 0o700 })
    lock = candidate
    await writeFile(join(lock, 'owner.json'), JSON.stringify({ pid: process.pid }) + '\n', { flag: 'wx', mode: 0o600 })
    runtime = await createRuntime({ directory: config.stateDirectory, bootstrapURL: config.bootstrapURL,
      controlToken: config.controlToken, bridgeToken: config.bridgeToken, profile: config.profile, profiles: config.profiles, manifest: config.manifest,
      profileSource: config.profileSource ?? 'repository', requireProfileAgreement: config.mode === 'production',
      measureInput, snapshotStore: new BootstrapSnapshotStore({ root: config.snapshotDirectory, requireTmpfs: config.mode === 'production' }) })
    server = createServer({ maxHeaderSize: 16 * 1024, requestTimeout: 10000, headersTimeout: 10000,
      keepAliveTimeout: 5000, connectionsCheckingInterval: 1000 }, (req, res) => {
      void Promise.resolve().then(() => runtime.handler(req, res)).catch(() => res.destroy())
    })
    server.maxConnections = 128
    server.on('clientError', (_error, socket) => socket.destroy())
    await new Promise((resolve, reject) => {
      const onError = error => reject(error)
      server.once('error', onError)
      server.listen({ ...config.listen, exclusive: true }, () => { server.off('error', onError); resolve() })
    })
    return Object.freeze({ port: server.address().port, mode: config.mode, close })
  } catch {
    await close().catch(() => {})
    throw new Error('agent_runtime_startup_failed')
  }
}
