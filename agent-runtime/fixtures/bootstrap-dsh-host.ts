import { BootstrapClient } from '../src/bootstrap/client.mjs'
import { BootstrapSync } from '../src/bootstrap/sync.mjs'
import { BootstrapSnapshotStore } from '../src/bootstrap/snapshot-store.mjs'
import { DshBootstrapApplier } from '../src/bootstrap/applier.mjs'
import { createDshGeneration } from '../src/bootstrap/dsh-generation.ts'
import { BlockAssembler } from '@deepseek-ai/dsh-llm'

// Synthetic process fixture: disk allowance and always-true policy are ONLY
// for bounded local provider tests. This does not expose a product entrypoint.
let sync: BootstrapSync
const applier = new DshBootstrapApplier({
  store: new BootstrapSnapshotStore({ root: process.env.Q4D_BOOTSTRAP_ROOT, requireTmpfs: false }),
  createGeneration: createDshGeneration, currentStatus: () => sync.status(),
})
sync = new BootstrapSync({ applier, authorize: () => true,
  client: new BootstrapClient({ url: process.env.Q4D_BOOTSTRAP_URL!, controlToken: process.env.Q4D_BOOTSTRAP_TOKEN! }),
})
process.send?.({ event: 'ready' })
process.on('message', (message: any) => {
  void (async () => {
    let result
    if (message.method === 'refresh') result = await sync.refresh()
    else if (message.method === 'status') result = sync.status()
    else if (message.method === 'model') {
      const assembler = new BlockAssembler()
      for await (const chunk of applier.stream({ ...message.params, messages: [] }, () => true)) assembler.push(chunk)
      result = { finish: assembler.finish.kind, content: assembler.message().content }
    } else if (message.method === 'drain') { await applier.drain(); result = {} }
    else throw new Error('fixture_unknown_method')
    process.send?.({ id: message.id, result })
  })().catch((error: Error) => process.send?.({ id: message.id, error: error.message.startsWith('agent_') ? error.message : 'fixture_failed' }))
})
process.on('disconnect', () => {
  void sync.stop().then(() => process.exit(0), () => process.exit(1))
})
