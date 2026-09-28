import { readFile } from 'node:fs/promises'
import { Context } from '@deepseek-ai/cordis'
import LlmRuntime from '@deepseek-ai/dsh-llm'
import type { GenerateOptions } from '@deepseek-ai/dsh-llm'
import { SettingsProvider } from '@deepseek-ai/dsh-settings'
import { CredentialProvider } from '@deepseek-ai/dsh-credentials'
import type { CredentialRef, CredentialKey } from '@deepseek-ai/dsh-credentials'
import { createLaunchEnvironmentSnapshot } from '@deepseek-ai/dsh-launch-environment'
import * as PiAi from '@deepseek-ai/dsh-llm-pi-ai'

// Public service implementations, bound to one immutable owned file pair.
// No FileSettings watcher, credentials-local, OAuth or ambient key fallback.
class SnapshotSettings extends SettingsProvider {
  readonly writable = false
  #document: Record<string, unknown>
  constructor(ctx: Context, config: { document: Record<string, unknown> }) { super(ctx); this.#document = config.document }
  protected load = async () => structuredClone(this.#document)
  protected async persist(): Promise<void> { throw new Error('agent_configuration_read_only') }
}
class SnapshotCredentials extends CredentialProvider {
  #refs: Record<string, string>
  constructor(ctx: Context, config: { refs: Record<string, string> }) { super(ctx); this.#refs = config.refs }
  // Cordis calls services through a context proxy: bind methods that use JS
  // private fields to their owning instance, rather than the proxy receiver.
  resolve = async (ref: CredentialRef) => Object.hasOwn(this.#refs, ref) ? { value: this.#refs[ref]!, source: 'q4d-bootstrap' } : undefined
  describe = async (ref: CredentialRef) => ({ configured: Object.hasOwn(this.#refs, ref), writable: false })
  async set(): Promise<void> { throw new Error('agent_configuration_read_only') }
  async unset(): Promise<void> { throw new Error('agent_configuration_read_only') }
  async readRecord(_key: CredentialKey) { return undefined }
  async describeRecord(_key: CredentialKey) { return { configured: false, writable: false } }
  async listRecords() { return [] }
  async modifyRecord(): Promise<never> { throw new Error('agent_configuration_read_only') }
  async deleteRecord(): Promise<void> { throw new Error('agent_configuration_read_only') }
}

export async function createDshGeneration({ settingsPath, credentialsPath, signal }: {
  settingsPath: string; credentialsPath: string; signal?: AbortSignal
}) {
  const ctx = new Context()
  try {
    signal?.throwIfAborted()
    const document = JSON.parse(await readFile(settingsPath, { encoding: 'utf8', signal }))
    const credentials = JSON.parse(await readFile(credentialsPath, { encoding: 'utf8', signal }))
    // Even pi-ai's secondary native-auth lookup sees an empty launch layer.
    ctx.provide('launchEnvironment', createLaunchEnvironmentSnapshot([]))
    await ctx.plugin(LlmRuntime)
    await ctx.plugin(SnapshotSettings, { document })
    await ctx.plugin(SnapshotCredentials, { refs: credentials.refs })
    await ctx.plugin(PiAi, {})
    signal?.throwIfAborted()
    return {
      async describe() {
        const actual = []
        for (const p of ctx.llm.listProviders()) {
          for (const model of await ctx.llm.listModels(p.id)) {
            const info = await ctx.llm.resolveModelInfo(p.id, model.id, signal)
            actual.push({ provider: p.id, model: model.id, context_window: info.context?.contextWindow, max_output_tokens: info.defaultMaxTokens })
          }
        }
        return actual
      },
      stream: (request: GenerateOptions) => ctx.llm.stream(request),
      close: () => ctx.fiber.dispose(),
    }
  } catch {
    await ctx.fiber.dispose().catch(() => {})
    throw new Error('agent_bootstrap_apply_failed')
  }
}
