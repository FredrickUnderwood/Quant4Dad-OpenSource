import { readFile, writeFile, lstat } from 'node:fs/promises'
import { createPrivateKey, sign } from 'node:crypto'
import { canonical, verifyRelease } from '../src/operations/generations.mjs'
try {
  if (process.argv.length !== 5) throw new Error()
  const [metadata, keyPath, output] = process.argv.slice(2)
  const stat = await lstat(keyPath)
  if (!stat.isFile() || stat.size > 4096 || (stat.mode & 0o7777) !== 0o600) throw new Error()
  const key = createPrivateKey(await readFile(keyPath))
  if (key.asymmetricKeyType !== 'ed25519') throw new Error()
  const bytes = await readFile(metadata)
  if (bytes.length > 65536) throw new Error()
  const release = JSON.parse(bytes)
  const document = { release, signature: sign(null, Buffer.from(canonical(release)), key).toString('base64url') }
  // Verifies metadata as well as the signature before creating any artifact.
  const { createPublicKey } = await import('node:crypto')
  verifyRelease(document, createPublicKey(key).export({ type: 'spki', format: 'pem' }), release.channel === 'edge')
  await writeFile(output, JSON.stringify(document, null, 2) + '\n', { flag: 'wx', mode: 0o600 })
  process.stdout.write('agent_release_signed\n')
} catch { process.stderr.write('agent_release_sign_failed\n'); process.exitCode = 1 }
