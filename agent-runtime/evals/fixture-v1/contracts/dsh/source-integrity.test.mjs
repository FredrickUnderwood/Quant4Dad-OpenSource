import assert from 'node:assert/strict'
import { execFile } from 'node:child_process'
import { mkdtemp, mkdir, rm, symlink, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { promisify } from 'node:util'
import test from 'node:test'
import { fileURLToPath } from 'node:url'

const exec = promisify(execFile)
const verifier = fileURLToPath(new URL('../../../../scripts/verify-source-tree.mjs', import.meta.url))

async function sourceTrees(t) {
  const root = await mkdtemp(join(tmpdir(), 'q4d-source-integrity-'))
  t.after(() => rm(root, { recursive: true, force: true }))
  const original = join(root, 'original')
  const installed = join(root, 'installed')
  for (const directory of [original, installed]) {
    await mkdir(directory)
    await writeFile(join(directory, 'package.json'), '{"private":true}\n')
    await writeFile(join(directory, 'source.ts'), 'export const version = 1\n')
    await symlink('source.ts', join(directory, 'entry.ts'))
    await mkdir(join(directory, 'packages/core/src'), { recursive: true })
    await writeFile(join(directory, 'packages/core/package.json'), '{"private":true}\n')
    await writeFile(join(directory, 'packages/core/src/index.ts'), 'export const original = true\n')
  }
  return { original, installed }
}

test('TEST-DSH-01 rejects modified source or symlinks while allowing installed dependencies', async t => {
  const { original, installed } = await sourceTrees(t)
  await mkdir(join(installed, 'node_modules'))
  await exec(process.execPath, [verifier, original, installed])
  await writeFile(join(installed, 'source.ts'), 'export const version = 2\n')
  await assert.rejects(exec(process.execPath, [verifier, original, installed]), /Modified upstream file/)
  await writeFile(join(installed, 'source.ts'), 'export const version = 1\n')
  await rm(join(installed, 'entry.ts'))
  await symlink('other.ts', join(installed, 'entry.ts'))
  await assert.rejects(exec(process.execPath, [verifier, original, installed]), /Changed source symlink/)
})

test('TEST-DSH-01 rejects added source, config, directories and symlinks', async t => {
  const { original, installed } = await sourceTrees(t)
  for (const name of ['packages/core/src.ts', 'packages/core/src/extra.ts',
    'packages/core/src/package.json', '.pnpmfile.cjs']) {
    const path = join(installed, name)
    await writeFile(path, 'export const unverified = true\n')
    await assert.rejects(exec(process.execPath, [verifier, original, installed]), /Unexpected upstream entry/)
    await rm(path)
  }
  await mkdir(join(installed, 'lib'))
  await writeFile(join(installed, 'lib/index.js'), 'export const unverified = true\n')
  await assert.rejects(exec(process.execPath, [verifier, original, installed]), /Unexpected upstream entry/)
  await rm(join(installed, 'lib'), { recursive: true })
  await symlink('source.ts', join(installed, 'extra.ts'))
  await assert.rejects(exec(process.execPath, [verifier, original, installed]), /Unexpected upstream entry/)
})

test('TEST-DSH-01 only allows real dependency directories at original package roots', async t => {
  const { original, installed } = await sourceTrees(t)
  for (const name of ['node_modules', 'packages/core/node_modules']) {
    await mkdir(join(installed, name))
    await writeFile(join(installed, name, 'dependency.js'), '// Installed dependency\n')
  }
  await exec(process.execPath, [verifier, original, installed])

  await mkdir(join(installed, 'packages/core/src/node_modules'))
  await assert.rejects(exec(process.execPath, [verifier, original, installed]), /Unexpected upstream entry/)
  await rm(join(installed, 'packages/core/src/node_modules'), { recursive: true })

  await rm(join(installed, 'node_modules'), { recursive: true })
  await symlink('packages/core/node_modules', join(installed, 'node_modules'))
  await assert.rejects(exec(process.execPath, [verifier, original, installed]), /Unexpected upstream entry/)
  await rm(join(installed, 'node_modules'))
  await writeFile(join(installed, 'node_modules'), '// Not a dependency directory\n')
  await assert.rejects(exec(process.execPath, [verifier, original, installed]), /Unexpected upstream entry/)
})
