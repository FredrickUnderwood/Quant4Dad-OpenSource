import { createServer } from 'node:http'
import { mkdir } from 'node:fs/promises'
import { join } from 'node:path'
import { createBootstrapRuntime } from '../src/runtime/bootstrap-runtime.ts'
import { BootstrapSnapshotStore } from '../src/bootstrap/snapshot-store.mjs'
import { canonicalAuthorizationJSON } from '../src/auth/run-capability.mjs'

// Only this process fixture supplies synthetic probe/tokenizer/current policy.
const root = process.env.Q4D_BOOTSTRAP_ROOT!
await mkdir(join(root, 'snapshots'), { mode: 0o700, recursive: true })
await mkdir(join(root, 'state'), { mode: 0o700, recursive: true })
const policies = new Map<string, string>()
const config = JSON.parse(process.env.Q4D_SESSION_CONFIG!)
let ready = true
const runtime = await createBootstrapRuntime({ directory: join(root, 'state'),
  bootstrapURL: process.env.Q4D_BOOTSTRAP_URL!, controlToken: process.env.Q4D_BOOTSTRAP_TOKEN!,
  bridgeToken: process.env.Q4D_SESSION_BRIDGE_TOKEN!, profile: config.profile, profiles: config.profiles, manifest: config.manifest,
  requireProfileAgreement: config.requireProfileAgreement,
  snapshotStore: new BootstrapSnapshotStore({ root: join(root, 'snapshots'), requireTmpfs: false }),
  ...(config.realPolicy ? {} : { authorize: (claims: any) => policies.get(claims.envelope.run_id) === canonicalAuthorizationJSON(claims) }),
  ...(config.realProbe ? {} : { modelReady: () => ready }), measureInput: () => 100,
})
const server = createServer(runtime.handler)
await new Promise<void>(resolve => server.listen(0, '127.0.0.1', resolve))
process.send?.({ event: 'ready', port: (server.address() as any).port })
process.on('message', (message: any) => {
  void (async () => {
    let result: unknown = {}
    if (message.method === 'authorize') policies.set(message.params.envelope.run_id, canonicalAuthorizationJSON(message.params))
    else if (message.method === 'revoke') policies.delete(message.params.runId)
    else if (message.method === 'unready') ready = false
    else if (message.method === 'refresh') result = await runtime.refresh()
    else if (message.method === 'status') result = runtime.status()
    else throw new Error('fixture_unknown_method')
    process.send?.({ id: message.id, result })
  })().catch(() => process.send?.({ id: message.id, error: 'fixture_operation_failed' }))
})
process.on('disconnect', () => {
  server.closeAllConnections(); server.close()
  void runtime.close().then(() => process.exit(0), () => process.exit(1))
})
