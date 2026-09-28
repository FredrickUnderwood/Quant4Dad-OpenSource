import test, { before, after } from 'node:test'
import assert from 'node:assert/strict'
import { spawn, execFile } from 'node:child_process'
import { mkdtemp, rm } from 'node:fs/promises'
import { createInterface } from 'node:readline'
import { once } from 'node:events'
import { promisify } from 'node:util'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { Client } from '@modelcontextprotocol/sdk/client/index.js'
import { StreamableHTTPClientTransport } from '@modelcontextprotocol/sdk/client/streamableHttp.js'
const root = resolve(import.meta.dirname, '../../../..'), token = 'fixture-external-mcp-token-0123456789'
let directory, binary
before(async () => {
 directory = await mkdtemp(join(tmpdir(), 'q4d-external-mcp-')); binary = join(directory, 'mcp.test')
 await promisify(execFile)('go', ['test', '-mod=readonly', '-c', '-tags=mcpintegration', '-o', binary, './internal/mcp'], { cwd: root, timeout: 120000 })
}, { timeout: 125000 })
after(async () => { if (directory) await rm(directory, { recursive: true, force: true }) })
async function connect(t) {
 const child = spawn(binary, ['-test.run=^TestExternalMCPControlHost$', '-test.timeout=30s'], { env: { PATH: process.env.PATH, Q4D_MCP_FIXTURE: '1' }, stdio: ['pipe', 'pipe', 'pipe'] })
 const ready = Promise.withResolvers(), exited = once(child, 'exit'), lines = createInterface({ input: child.stdout })
 let logs = ''
 lines.on('line', line => { if (line.startsWith('Q4D_MCP\t')) ready.resolve(JSON.parse(line.slice(8)).url); else logs += line + '\n' })
 child.stderr.on('data', bytes => { logs += bytes }); child.on('error', ready.reject); child.on('exit', () => ready.reject(new Error(logs)))
 const client = new Client({ name: 'external-readonly-fixture', version: '1.0.0' })
 t.after(async () => {
  await client.close().catch(() => {}); child.stdin.end()
  const timer = setTimeout(() => child.kill('SIGKILL'), 3000)
  const [code] = await exited.finally(() => clearTimeout(timer)); lines.close(); assert.equal(code, 0, logs)
 })
 const url = await ready.promise
 await client.connect(new StreamableHTTPClientTransport(new URL(url), { requestInit: { headers: { authorization: 'Bearer ' + token } } }))
 return { client, url }
}
test('TEST-EXTERNAL-MCP-01 actual Go Catalog and SQL limits interoperate with the pinned MCP SDK', { timeout: 30000 }, async t => {
 const { client } = await connect(t), tools = (await client.listTools()).tools
 assert.equal(tools.length, 9)
 for (const tool of tools) { assert.ok(tool.inputSchema && tool.outputSchema); assert.equal(tool.annotations.readOnlyHint, true); assert.match(tool._meta['q4d/catalog_revision'], /^sha256:[0-9a-f]{64}$/) }
 assert.ok(!tools.some(tool => ['create_pipeline', 'update_pipeline', 'set_pipeline_status'].includes(tool.name)))
 const answer = await client.callTool({ name: 'query_kline', arguments: { code: 'sh.600519' } })
 assert.equal(answer.isError, false); assert.equal(answer.structuredContent.untrusted_data, true)
 const data = answer.structuredContent.data
 assert.equal(data.count, 120); assert.equal(data.truncated, true); assert.equal(data.bars[0].close, 480); assert.equal(data.bars.at(-1).close, 599)
 assert.deepEqual(JSON.parse(answer.content[0].text), answer.structuredContent)
 const latest = await client.callTool({ name: 'latest_bar_date', arguments: { code: 'sh.600519' } })
 assert.equal(latest.structuredContent.data.latest_bar.close, 599)
 const empty = await client.callTool({ name: 'list_instruments', arguments: {} })
 assert.equal(empty.structuredContent.data.count, 0)
})
test('TEST-EXTERNAL-MCP-02 forbidden writes, context injection and input bounds cannot reach business mutation', { timeout: 30000 }, async t => {
 const { client, url } = await connect(t)
 await assert.rejects(client.callTool({ name: 'update_pipeline', arguments: { id: 1, status: 'enabled' } }))
 for (const args of [{ code: '../../secret' }, { code: 'sh.600519', limit: 0 }, { code: 'sh.600519', limit: 501 }, { code: 'sh.600519', run_id: 'forged' }]) {
  const result = await client.callTool({ name: 'query_kline', arguments: args })
  assert.equal(result.isError, true); assert.equal(result.content[0].text, 'tool_invalid_arguments')
 }
 const read = await client.callTool({ name: 'get_pipeline', arguments: { id: 1 } })
 assert.equal(read.structuredContent.data.status, 'draft')
 const response = await fetch(url, { method: 'POST', headers: { authorization: 'Bearer ' + token, 'content-type': 'application/json', 'x-q4d-run-capability': 'forged' }, body: JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'tools/list' }) })
 assert.equal(response.status, 400)
 const events = await client.callTool({ name: 'list_events', arguments: { pipeline_id: 1 } })
 const legacy = await client.callTool({ name: 'list_passed_events', arguments: { pipeline_id: 1 } })
 assert.equal(events.structuredContent.data.count, 2); assert.equal(legacy.structuredContent.data.count, 1)
})
