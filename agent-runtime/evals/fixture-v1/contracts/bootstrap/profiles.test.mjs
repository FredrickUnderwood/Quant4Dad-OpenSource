import test from 'node:test'
import assert from 'node:assert/strict'
import { rm } from 'node:fs/promises'
import { defaultProfiles, hash } from '../../../../profiles/defaults.mjs'
import { loadLaunchConfig } from '../../../../src/runtime/launch-config.mjs'
import { profileIdentity, profilesAgree } from '../../../../src/runtime/profile-agreement.mjs'
import { launchConfig } from '../../helpers/launch-config.mjs'

test('legacy mounted builtin prompts follow the image without replacing environment or credentials', async t => {
  const expected = defaultProfiles().find(p => p.id === 'strategy_lab')
  const f = await launchConfig({ selectedProfiles: [{ ...expected, systemPrompt: 'old prompt', revision: hash('old revision'), promptBundleDigest: hash('old prompt') }] })
  t.after(() => rm(f.directory, { recursive: true, force: true }))
  const loaded = await loadLaunchConfig(f.file, f.env)
  assert.deepEqual(loaded.profiles, [expected])
  for (const key of ['stateDirectory', 'snapshotDirectory', 'bootstrapURL', 'inputMeter', 'manifest']) assert.deepEqual(loaded[key], f.config[key])
  assert.equal(loaded.controlToken, 'fixture-control-token-0123456789-abcdef')
  const custom = { ...expected, systemPrompt: 'Explicit custom prompt.' }
  custom.revision = hash(custom.id + '\n' + custom.systemPrompt)
  custom.promptBundleDigest = hash(custom.systemPrompt)
  await f.save({ ...f.config, profileSource: 'config', profiles: [custom] })
  assert.deepEqual((await loadLaunchConfig(f.file, f.env)).profiles, [custom])
  custom.promptBundleDigest = hash('wrong body')
  await f.save({ ...f.config, profileSource: 'config', profiles: [custom] })
  await assert.rejects(loadLaunchConfig(f.file, f.env))
  await f.save({ ...f.config, profileSource: 'typo' })
  await assert.rejects(loadLaunchConfig(f.file, f.env))
})

test('deployment profile agreement rejects missing API metadata and each mismatched identity field', () => {
  const profiles = defaultProfiles()
  const bootstrap = { profiles: profiles.map(profileIdentity) }
  assert.equal(profilesAgree(profiles, bootstrap, true), true)
  assert.equal(profilesAgree(profiles, {}, true), false)
  assert.equal(profilesAgree(profiles, {}, false), true)
  for (const key of ['id', 'revision', 'promptBundleDigest', 'skillsDigest', 'toolCatalogRevision']) {
    const other = structuredClone(bootstrap)
    other.profiles[0][key] = 'different'
    assert.equal(profilesAgree(profiles, other, true), false, key)
  }
  assert.equal(JSON.stringify(bootstrap).includes('systemPrompt'), false)
})
