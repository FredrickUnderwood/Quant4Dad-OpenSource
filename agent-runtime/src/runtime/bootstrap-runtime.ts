import { BootstrapClient } from '../bootstrap/client.mjs'
import { BootstrapSync } from '../bootstrap/sync.mjs'
import { BootstrapSnapshotStore } from '../bootstrap/snapshot-store.mjs'
import { DshBootstrapApplier } from '../bootstrap/applier.mjs'
import { createDshGeneration } from '../bootstrap/dsh-generation.ts'
import { createSessionHost } from './session-host.ts'
import { ModelProbe } from '../models/probe.ts'
import { RunPolicy } from '../auth/run-policy.mjs'

type HostOptions = Parameters<typeof createSessionHost>[0]
type RuntimeOptions = Omit<HostOptions, 'sync' | 'applier' | 'modelReady' | 'probe' | 'prepareRun' | 'releaseRun'> & {
  modelReady?: HostOptions['modelReady'];
  bootstrapURL: string; controlToken: string; authorize?: (claims: any) => boolean;
  snapshotStore?: BootstrapSnapshotStore
}

/** Application composition with fixed model probing and current Go Run policy.
 * A trusted tokenizer is required; fixtures may override synchronous policy.
 * Startup succeeds only after the first complete DSH generation is applied.
 * The application owns listening on a private socket and shutdown signals. */
export async function createBootstrapRuntime(options: RuntimeOptions) {
  if (options.bridgeToken === options.controlToken) throw new Error('agent_runtime_configuration_invalid')
  const policy = options.authorize ? undefined : new RunPolicy(options)
  let sync: BootstrapSync
  const applier = new DshBootstrapApplier({ store: options.snapshotStore ?? new BootstrapSnapshotStore(),
    createGeneration: createDshGeneration, currentStatus: () => sync.status() })
  sync = new BootstrapSync({ applier, authorize: options.authorize ?? policy!.authorize,
    client: new BootstrapClient({ url: options.bootstrapURL, controlToken: options.controlToken }) })
  const probes = new ModelProbe(sync, applier, options.measureInput)
  try {
    await sync.refresh()
    if (options.bridgeToken === sync.readConfiguration().mcp.runtime_token) throw new Error('agent_runtime_configuration_invalid')
    const host = await createSessionHost({ ...options, sync, applier, modelReady: options.modelReady ?? probes.ready,
      ...(policy ? { prepareRun: policy.prepare, releaseRun: policy.forget } : {}),
      probe: async (request, signal) => { await sync.refresh(); return probes.probe(request, signal) } })
    sync.start()
    return Object.freeze({ handler: host.handler, status: () => sync.status(), refresh: () => sync.refresh(),
      async close() { probes.close(); try { await host.close() } finally { await policy?.close(); await sync.stop() } } })
  } catch { probes.close(); await policy?.close(); await sync.stop(); throw new Error('agent_runtime_unavailable') }
}
