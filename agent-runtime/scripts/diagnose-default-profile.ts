/** Optional upstream-baseline diagnostic; never used as the minimal-host gate. */
import { mkdtemp, realpath, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { launchAcpTestAgent } from '@deepseek-ai/dsh-session-snapshot'

const source = resolve(process.env.Q4D_DSH_SOURCE_DIR!)
const cwd = await realpath(await mkdtemp(join(tmpdir(), 'q4d-default-profile-')))
const host = launchAcpTestAgent({
  agent: {
    binScript: join(source, 'apps/cli/src/bin.ts'),
    configPath: join(source, 'apps/cli/tests/profiles/acp/tests/fixtures/control-surface/cordis.yml'),
    profile: 'acp',
    tsconfigPath: join(source, 'tsconfig.json'),
  },
  cwd,
  env: { DSH_CONFORMANCE_PERSISTENCE_ROOT: join(cwd, 'sessions'), DSH_TELEMETRY_DISABLED: '1' },
})
try {
  await host.spawned
  console.log('initialize', await host.client.initialize({ protocolVersion: 1, clientCapabilities: {} }))
  console.log('session/new', await host.client.newSession({ cwd, mcpServers: [] }))
} catch (error) {
  console.error(error)
  console.error(host.stderr())
  process.exitCode = 1
} finally {
  await host.close()
  await rm(cwd, { recursive: true, force: true })
}
