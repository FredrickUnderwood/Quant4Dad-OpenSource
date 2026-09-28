import { constants } from 'node:fs'
import { lstat, statfs, realpath, mkdtemp, open, rename, rm, chmod } from 'node:fs/promises'
import { isAbsolute, join } from 'node:path'
import { inspect } from 'node:util'
import { failBootstrap } from './validation.mjs'

/** Private, immutable generations. The adapter pins BOTH files to a single
 * generation directory, never independently follows two mutable file paths.
 * Production roots must already be an owned 0700 tmpfs mount. The explicit
 * requireTmpfs:false option is for synthetic tests on non-Linux hosts only. */
export class BootstrapSnapshotStore {
  #root
  #tmpfs
  constructor({ root = '/run/q4d-agent', requireTmpfs = true } = {}) {
    if (typeof root !== 'string' || !isAbsolute(root) || typeof requireTmpfs !== 'boolean') failBootstrap('agent_bootstrap_configuration_invalid')
    this.#root = root
    this.#tmpfs = requireTmpfs
  }
  toJSON() { return { type: 'BootstrapSnapshotStore', redacted: true } }
  [inspect.custom]() { return 'BootstrapSnapshotStore { redacted }' }

  async write({ settings, credentials }, { signal } = {}) {
    let directory
    try {
      signal?.throwIfAborted()
      const stat = await lstat(this.#root)
      if (!stat.isDirectory() || stat.isSymbolicLink() || stat.uid !== process.getuid() ||
          (stat.mode & 0o7777) !== 0o700 || await realpath(this.#root) !== this.#root ||
          (this.#tmpfs && (process.platform !== 'linux' || (await statfs(this.#root)).type !== 0x01021994))) throw new Error()
      const settingsBytes = Buffer.from(JSON.stringify(settings) + '\n')
      const credentialBytes = Buffer.from(JSON.stringify(credentials) + '\n')
      if (settingsBytes.length > 1024 * 1024 || credentialBytes.length > 1024 * 1024) throw new Error()
      directory = await mkdtemp(join(this.#root, '.stage-'))
      await chmod(directory, 0o700)
      for (const [name, bytes, mode] of [['settings.json', settingsBytes, 0o640], ['credentials.json', credentialBytes, 0o600]]) {
        signal?.throwIfAborted()
        const file = await open(join(directory, name), constants.O_WRONLY | constants.O_CREAT | constants.O_EXCL | constants.O_NOFOLLOW, mode)
        try { await file.chmod(mode); await file.writeFile(bytes); await file.sync() } finally { await file.close() }
      }
      signal?.throwIfAborted()
      const published = join(this.#root, 'generation-' + directory.slice(directory.lastIndexOf('.stage-') + 7))
      await rename(directory, published)
      directory = published
      const dir = directory
      let removed = false
      return Object.freeze({ directory: dir, settingsPath: join(dir, 'settings.json'), credentialsPath: join(dir, 'credentials.json'),
        async remove() {
          if (removed) return
          await rm(dir, { recursive: true, force: true })
          removed = true
        } })
    } catch {
      if (directory) await rm(directory, { recursive: true, force: true }).catch(() => {})
      failBootstrap('agent_bootstrap_storage_failed')
    }
  }
}
