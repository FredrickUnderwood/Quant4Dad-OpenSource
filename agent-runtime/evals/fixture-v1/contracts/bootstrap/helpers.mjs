import { createServer } from 'node:http'
import { readFileSync } from 'node:fs'
import { BootstrapClient } from '../../../../src/bootstrap/client.mjs'

export const fixture = JSON.parse(readFileSync(new URL('../../../../contracts/bootstrap-v1/fixtures/bootstrap.valid.json', import.meta.url)))
export const controlToken = 'fixture-control-token-0123456789-abcdef'
export const clone = () => structuredClone(fixture)
export const headers = value => ({ 'content-type': 'application/json; charset=utf-8', 'cache-control': 'no-store',
  'x-content-type-options': 'nosniff', etag: `"${value.revision}"` })
export function send(res, value = fixture, status = 200) {
  res.writeHead(status, headers(value))
  res.end(status === 304 ? undefined : JSON.stringify(value))
}
export async function host(t, handler = (_req, res) => send(res), options = {}) {
  const requests = []
  let current = handler
  const server = createServer((req, res) => { requests.push(req); current(req, res) })
  t.after(() => { server.closeAllConnections(); return new Promise(resolve => server.close(resolve)) })
  await new Promise((resolve, reject) => { server.once('error', reject); server.listen(0, '127.0.0.1', resolve) })
  const url = `http://127.0.0.1:${server.address().port}/internal/v1/agent/bootstrap`
  return { server, url, requests, setHandler(handler) { current = handler },
    client: new BootstrapClient({ url, controlToken, ...options }) }
}
export function revise(value = clone(), model = '3', epoch = '1') {
  value.model_config_revision = model.repeat(32)
  value.revision = epoch.repeat(32) + '.' + value.model_config_revision
  return value
}
