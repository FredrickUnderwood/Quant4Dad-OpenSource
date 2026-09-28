import { readFileSync } from 'node:fs'
try {
  const config = JSON.parse(readFileSync(process.argv[2], 'utf8'))
  const token = readFileSync(process.env.Q4D_AGENT_BRIDGE_TOKEN_FILE, 'utf8').trimEnd()
  const host = config.listen.host === '::1' ? '[::1]' : config.listen.host
  const response = await fetch(`http://${host}:${config.listen.port}/q4d/v1/runtime-health`, {
    headers: { authorization: 'Bearer ' + token }, signal: AbortSignal.timeout(2000), redirect: 'error' })
  if (!response.ok || (await response.json()).status !== 'ready') throw new Error()
} catch {
  process.stderr.write('agent_runtime_health_unavailable\n')
  process.exitCode = 1
}
