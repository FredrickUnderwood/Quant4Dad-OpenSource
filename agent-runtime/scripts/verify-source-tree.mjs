import assert from 'node:assert/strict'
import { lstat, readFile, readdir, readlink } from 'node:fs/promises'
import { join, resolve } from 'node:path'

// Only dependency directories beside an original package.json may be added.
// Extra source/config/build files can change resolution even if originals match.
// Never modify either tree to make this pass.
async function compare(expected, actual) {
  const originalNames = new Set(await readdir(expected))
  for (const name of await readdir(actual)) {
    if (originalNames.has(name)) continue
    const to = join(actual, name)
    const installed = await lstat(to)
    const dependencyDirectory = name === 'node_modules'
      && originalNames.has('package.json')
      && (await lstat(join(expected, 'package.json'))).isFile()
      && installed.isDirectory()
    assert.ok(dependencyDirectory, `Unexpected upstream entry: ${to}`)
  }
  for (const name of originalNames) {
    const from = join(expected, name)
    const to = join(actual, name)
    const original = await lstat(from)
    const installed = await lstat(to)
    assert.equal(installed.isSymbolicLink(), original.isSymbolicLink(), `Changed entry type: ${to}`)
    assert.equal(installed.isDirectory(), original.isDirectory(), `Changed entry type: ${to}`)
    assert.equal(installed.isFile(), original.isFile(), `Changed entry type: ${to}`)
    if (original.isSymbolicLink()) {
      assert.equal(await readlink(to), await readlink(from), `Changed source symlink: ${to}`)
    } else if (original.isDirectory()) {
      await compare(from, to)
    } else {
      assert.ok((await readFile(from)).equals(await readFile(to)), `Modified upstream file: ${to}`)
    }
  }
}
assert.equal(process.argv.length, 4, 'Usage: verify-source-tree.mjs expected-directory installed-directory')
await compare(resolve(process.argv[2]), resolve(process.argv[3]))
