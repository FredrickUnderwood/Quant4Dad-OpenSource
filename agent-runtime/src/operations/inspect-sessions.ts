import { Context } from '@deepseek-ai/cordis'
import SessionStore from '@deepseek-ai/dsh-session'
import JsonlSessionPersistence from '@deepseek-ai/dsh-session-persistence-jsonl'
import { readFile, lstat } from 'node:fs/promises'
import { resolve, join } from 'node:path'
import { inventory, canonical, statePath, withStore } from './generations.mjs'
import { compareSessions, readJournal } from './session-inventory.mjs'

try {
  if (process.argv.length !== 5) throw new Error()
  const [root, generation, projection] = process.argv.slice(2) as [string, string, string]
  const stat = await lstat(projection)
  if (!stat.isFile() || stat.isSymbolicLink() || stat.size > 4 * 1024 * 1024 || (stat.mode & 0o7777) !== 0o600) throw new Error()
  const product = JSON.parse(await readFile(projection, 'utf8'))
  const result = await withStore(resolve(root), async () => {
    const state = statePath(resolve(root), generation), before = await inventory(state)
    const ctx = new Context()
    try {
      // Public read-only listing; no AgentLoop, Tools, model or credentials.
      await ctx.plugin(SessionStore)
      await ctx.plugin(JsonlSessionPersistence, { root: join(state, 'sessions'), compression: 'none' })
      const headers = await ctx.sessionPersistence.list(AbortSignal.timeout(30000))
      const logs = before.items.filter((i: { path: string; directory?: boolean }) => !i.directory && i.path.startsWith('sessions/') && i.path.endsWith('/session.jsonl'))
      if (headers.length !== logs.length) throw new Error()
      const report = compareSessions(await readJournal(join(state, 'session-bindings.jsonl')), headers, product)
      if (canonical(before) !== canonical(await inventory(state))) throw new Error()
      return report
    } finally { await ctx.fiber.dispose() }
  })
  process.stdout.write(JSON.stringify(result) + '\n')
  if (result.issues.length) process.exitCode = 2
} catch { process.stderr.write('agent_inventory_failed\n'); process.exitCode = 1 }
