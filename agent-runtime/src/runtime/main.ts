export {}

// The launcher starts this module with a fixed Node/DSH dependency closure.
// Logs contain fixed lifecycle codes only, never configuration or raw errors.
let host: { port: number; mode: string; close: () => Promise<void> } | undefined
let stopping = false
let shutdownTimer: ReturnType<typeof setTimeout> | undefined
function deadline() {
  shutdownTimer ??= setTimeout(() => { process.stderr.write('agent_runtime_shutdown_timeout\n'); process.exit(1) }, 15000)
}
async function stop() {
  if (stopping) return
  stopping = true
  deadline()
  if (!host) return // startup finishes or fails under the same deadline
  try {
    await host.close()
    process.stdout.write('agent_runtime_stopped\n')
    process.exit(0)
  } catch { process.stderr.write('agent_runtime_shutdown_failed\n'); process.exit(1) }
}
process.on('SIGTERM', () => { void stop() })
process.on('SIGINT', () => { void stop() })
try {
  const [major, minor] = process.versions.node.split('.').map(Number)
  if (process.argv.length !== 3 || major !== 24 || minor! < 20) throw new Error()
  // Module/configuration failures share the same redacted startup boundary.
  const { createBootstrapRuntime } = await import('./bootstrap-runtime.ts')
  const { startRuntimeServer } = await import('./server.mjs')
  host = await startRuntimeServer(process.argv[2], createBootstrapRuntime)
  if (stopping) { stopping = false; await stop() }
  else process.stdout.write(JSON.stringify({ event: 'agent_runtime_ready', port: host.port, mode: host.mode }) + '\n')
} catch { process.stderr.write('agent_runtime_startup_failed\n'); process.exit(1) }
