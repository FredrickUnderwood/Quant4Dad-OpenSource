// Linux operator command. Local signed configuration only; never a Web API.
// Container names, mounts and exact digest are checked before stopping anything.
import { readFile, writeFile, mkdir, lstat, realpath } from 'node:fs/promises'
import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import { resolve, join } from 'node:path'
import { createHash } from 'node:crypto'
import { verifyRelease, active, statePath, stage, rollback, canonical } from '../src/operations/generations.mjs'
import { updateTransaction } from '../src/operations/update-transaction.mjs'

const fail = () => { throw new Error('agent_update_configuration_invalid') }
const run = async (file, args, timeout = 45000) => (await promisify(execFile)(file, args, {
  timeout, maxBuffer: 2 * 1024 * 1024, env: { PATH: process.env.PATH, HOME: process.env.HOME }, encoding: 'utf8' })).stdout
const docker = (...args) => run('docker', args)
const nodeCLI = (...args) => run(process.execPath, [resolve(import.meta.dirname, 'q4dctl.mjs'), ...args])
async function privateFile(path, max = 65536) {
  if (!path || resolve(path) !== path || await realpath(path) !== path) fail()
  const s = await lstat(path)
  if (!s.isFile() || s.nlink !== 1 || s.uid !== process.getuid() || (s.mode & 0o7777) !== 0o600 || s.size > max) fail()
  return readFile(path)
}
async function privateDirectory(path) {
  if (/[,\p{Cc}]/u.test(path) || resolve(path) !== path || await realpath(path) !== path) fail()
  const s = await lstat(path)
  if (!s.isDirectory() || s.uid !== process.getuid() || (s.mode & 0o7777) !== 0o700) fail()
}
try {
  if (process.platform !== 'linux' || process.getuid() !== 10001 || process.argv.length !== 3) fail()
  const config = JSON.parse((await privateFile(process.argv[2])).toString())
  const keys = ['api_container', 'candidate_container', 'candidate_generation', 'config_root', 'data_root', 'meter_file', 'previous_container', 'public_key_file', 'release_file']
  if (Object.keys(config).sort().join() !== keys.sort().join() || Object.values(config).some(v => typeof v !== 'string')) fail()
  for (const k of ['api_container', 'candidate_container', 'previous_container']) if (!/^[a-zA-Z0-9][a-zA-Z0-9_-]{0,79}$/.test(config[k])) fail()
  if (new Set([config.api_container, config.candidate_container, config.previous_container]).size !== 3) fail()
  await privateDirectory(config.data_root); await privateDirectory(config.config_root)
  const publicKey = await privateFile(config.public_key_file), signed = JSON.parse((await privateFile(config.release_file)).toString())
  const release = verifyRelease(signed, publicKey.toString())
  const meter = await privateFile(config.meter_file, 16 * 1024 * 1024)
  if ('sha256:' + createHash('sha256').update(meter).digest('hex') !== release.meter_sha256) fail()
  statePath(config.data_root, config.candidate_generation)
  const previous = await active(config.data_root), { signature, ...payload } = previous.release
  verifyRelease({ release: payload, signature }, publicKey.toString())
  if (previous.generation === config.candidate_generation || release.q4d_version !== payload.q4d_version) fail()
  const folder = join(config.config_root, config.candidate_generation)
  const token = '/run/q4d-config/bridge-token', origin = 'http://127.0.0.1:29091'
  const containerCLI = (container, ...args) => docker('exec', container, 'node', '/opt/q4d/agent-runtime/scripts/q4dctl.mjs', ...args)
  const inspect = async name => JSON.parse(await docker('inspect', '--type', 'container', name))[0]
  let oldConfig, created = false
  async function health(container) {
    const until = Date.now() + 30000
    while (Date.now() < until) {
      try {
        await docker('exec', container, 'node', '/opt/q4d/agent-runtime/scripts/healthcheck.mjs', '/run/q4d-config/runtime.json')
        return
      } catch { await new Promise(r => setTimeout(r, 250)) }
    }
    throw new Error('agent_update_health_failed')
  }
  async function stop(container) {
    const current = await inspect(container)
    if (current.State.Running) await docker('stop', '--time', '25', container)
    if ((await inspect(container)).State.Running) throw new Error()
  }
  const steps = {
    async preflight() {
      const old = await inspect(config.previous_container), api = await inspect(config.api_container)
      if (!old.State.Running || !api.State.Running || old.Config.Image !== payload.image ||
          ![config.api_container, api.Id].some(id => old.HostConfig.NetworkMode === 'container:' + id)) fail()
      const dataMount = old.Mounts.find(m => m.Destination === '/var/lib/q4d-agent')
      const configMount = old.Mounts.find(m => m.Destination === '/run/q4d-config')
      if (dataMount?.Source !== config.data_root || !dataMount.RW || !configMount || configMount.RW) fail()
      oldConfig = configMount.Source; await privateDirectory(oldConfig)
      const launch = JSON.parse((await privateFile(join(oldConfig, 'runtime.json'))).toString())
      if (launch.mode !== 'production' || launch.listen.host !== '127.0.0.1' || launch.listen.port !== 29091 ||
          launch.stateDirectory !== '/var/lib/q4d-agent/generations/' + previous.generation + '/state') fail()
      // Pre-pull/check the exact signed image before service interruption.
      await run('docker', ['pull', release.image], 300000)
      const names = (await docker('ps', '-a', '--format', '{{.Names}}')).trim().split('\n')
      if (names.includes(config.candidate_container)) fail()
      await mkdir(folder, { mode: 0o700 })
      for (const name of ['control-token', 'bridge-token']) await writeFile(join(folder, name), await privateFile(join(oldConfig, name), 258), { flag: 'wx', mode: 0o600 })
      await writeFile(join(folder, 'input-meter.mjs'), meter, { flag: 'wx', mode: 0o600 })
    },
    async drain() {
      const until = Date.now() + 600000
      try {
        await containerCLI(config.previous_container, 'drain', origin, token)
        while (Date.now() < until) {
          const status = JSON.parse(await containerCLI(config.previous_container, 'maintenance', origin, token))
          if (status.active_runs === 0 && status.pending_operations === 0) return
          await new Promise(r => setTimeout(r, 500))
        }
        throw new Error()
      } catch {
        await containerCLI(config.previous_container, 'resume', origin, token)
        throw new Error('agent_update_drain_failed')
      }
    },
    stopPrevious: () => stop(config.previous_container),
    async stage() {
      if (canonical(await active(config.data_root)) !== canonical(previous)) fail()
      await stage(config.data_root, config.candidate_generation, release)
      await writeFile(join(folder, 'runtime-template.json'), await privateFile(join(oldConfig, 'runtime.json'), 256 * 1024), { flag: 'wx', mode: 0o600 })
      // Defaults must come from the candidate image, not the operator checkout.
      await docker('run', '--rm', '--network', 'none', '--user', '10001:10001', '--read-only',
        '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges', '--memory', '384m', '--cpus', '1',
        '--mount', `type=bind,source=${config.data_root},target=/var/lib/q4d-agent,readonly`,
        '--mount', `type=bind,source=${folder},target=/upgrade`, '--entrypoint', 'node', release.image,
        '/opt/q4d/agent-runtime/scripts/q4dctl.mjs', 'render-config', '/var/lib/q4d-agent', config.candidate_generation,
        '/upgrade/runtime-template.json', '/upgrade/runtime.json')
    },
    async startCandidate() {
      if (created) { await docker('start', config.candidate_container); return }
      // create and start separately so every possibly-created container can be
      // stopped during compensation, even when start loses its acknowledgement.
      await docker('create', '--name', config.candidate_container, '--network', 'container:' + config.api_container,
        '--restart', 'unless-stopped', '--init', '--user', '10001:10001', '--read-only', '--cap-drop', 'ALL',
        '--security-opt', 'no-new-privileges', '--pids-limit', '256', '--memory', '2g', '--cpus', '2',
        '--tmpfs', '/tmp:rw,noexec,nosuid,nodev,size=512m,mode=1777',
        '--tmpfs', '/run/q4d-snapshots:rw,noexec,nosuid,nodev,size=16m,mode=0700,uid=10001,gid=10001',
        '--mount', 'type=bind,src=' + folder + ',dst=/run/q4d-config,readonly',
        '--mount', 'type=bind,src=' + config.data_root + ',dst=/var/lib/q4d-agent',
        '-e', 'Q4D_AGENT_CONTROL_TOKEN_FILE=/run/q4d-config/control-token', '-e', 'Q4D_AGENT_BRIDGE_TOKEN_FILE=/run/q4d-config/bridge-token',
        '--entrypoint', 'bash', release.image, '/opt/q4d/agent-runtime/scripts/start-runtime.sh', '/run/q4d-config/runtime.json')
      created = true; await docker('start', config.candidate_container)
    },
    async stopCandidate() {
      if (created) return stop(config.candidate_container)
      // A timed-out create may still have created this unique preflighted name.
      const names = (await docker('ps', '-a', '--format', '{{.Names}}')).trim().split('\n')
      if (names.includes(config.candidate_container)) { created = true; await stop(config.candidate_container) }
    },
    async verifyCandidate() {
      await health(config.candidate_container)
      await containerCLI(config.candidate_container, 'verify-candidate', '/var/lib/q4d-agent', config.candidate_generation, origin, token)
    },
    seal: () => nodeCLI('seal-candidate', config.data_root, config.candidate_generation),
    activate: () => nodeCLI('activate', config.data_root, config.candidate_generation, config.release_file, config.public_key_file),
    healthCandidate: () => health(config.candidate_container),
    isCandidateActive: async () => (await active(config.data_root)).generation === config.candidate_generation,
    rollback: () => rollback(config.data_root),
    async startPrevious() {
      if (!(await inspect(config.previous_container)).State.Running) {
        await docker('start', config.previous_container)
        await health(config.previous_container)
      }
      await containerCLI(config.previous_container, 'resume', origin, token)
    },
    healthPrevious: () => health(config.previous_container),
  }
  // Serializes the complete deployment across stage/activate's shorter locks.
  const lock = join(config.data_root, '.deployment-lock')
  await mkdir(lock, { mode: 0o700 })
  const result = await updateTransaction(steps)
  const { rm } = await import('node:fs/promises')
  await rm(lock, { recursive: true })
  process.stdout.write(JSON.stringify({ ...result, container: result.status === 'updated' ? config.candidate_container : config.previous_container }) + '\n')
  if (result.status !== 'updated') process.exitCode = 2
} catch (e) {
  process.stderr.write(e?.message === 'agent_update_reconciliation_required' ? e.message + '\n' : 'agent_update_failed\n')
  process.exitCode = 1
}
