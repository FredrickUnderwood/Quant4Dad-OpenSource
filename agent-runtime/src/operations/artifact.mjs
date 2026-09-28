import { readFile, writeFile } from 'node:fs/promises'
import { join } from 'node:path'
import { hash } from '../../profiles/defaults.mjs'

// Export only public artifact data. Signing stays with the deployment owner;
// no signing key, existing credentials or state is mounted for this command.
export async function exportArtifact(directory) {
  const metadata = JSON.parse(await readFile(new URL('../../release-defaults.json', import.meta.url), 'utf8'))
  const meter = await readFile(new URL('../meter/byte-budget.mjs', import.meta.url))
  metadata.meter_sha256 = hash(meter)
  await writeFile(join(directory, 'input-meter.mjs'), meter, { flag: 'wx', mode: 0o600 })
  await writeFile(join(directory, 'artifact.json'), JSON.stringify(metadata) + '\n', { flag: 'wx', mode: 0o600 })
  return { status: 'artifact_exported', meter_sha256: metadata.meter_sha256 }
}
