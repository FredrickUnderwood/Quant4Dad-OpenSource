import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { dirname, join } from 'node:path'
import test from 'node:test'
import { fileURLToPath } from 'node:url'

const here = dirname(fileURLToPath(import.meta.url))
const runtimeRoot = join(here, '../../../../')

async function json(path) {
  return JSON.parse(await readFile(path, 'utf8'))
}

test('TEST-DSH-01 selects the checksummed source artifact, not the drifting npm graph', async () => {
  const upstream = await json(join(runtimeRoot, 'upstream.lock.json'))
  const manifest = await json(join(runtimeRoot, 'package.json'))
  const lock = await json(join(runtimeRoot, 'package-lock.json'))

  assert.equal(upstream.selected_artifact, 'source-archive')
  assert.match(upstream.source.tag, /^dsh-v0\.1\.2-alpha\.5$/)
  assert.match(upstream.source.sha256, /^[0-9a-f]{64}$/)
  assert.equal(upstream.source.package_manager, 'pnpm@11.7.0')
  assert.equal(upstream.source.installer, `pnpm@${manifest.devDependencies.pnpm}`)
  assert.match(upstream.source.lockfile_sha256, /^[0-9a-f]{64}$/)
  assert.equal(manifest.dependencies?.['@deepseek-ai/dsh'], undefined)
  assert.equal(lock.packages?.['node_modules/@deepseek-ai/dsh'], undefined)
  for (const field of ['dependencies', 'devDependencies']) {
    for (const [name, version] of Object.entries(manifest[field])) {
      assert.match(version, /^\d+\.\d+\.\d+$/, `${name} must be exactly pinned`)
      assert.equal(lock.packages[`node_modules/${name}`].version, version)
    }
  }
})
