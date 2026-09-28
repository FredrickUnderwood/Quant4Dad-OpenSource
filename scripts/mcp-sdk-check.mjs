#!/usr/bin/env node
// Read-only check using the repository's pinned MCP SDK; no credentials are logged.
// Inputs: Q4D_MCP_URL + Q4D_MCP_TOKEN_FILE, or Q4D_MCP_CONFIG_STDIN=1
// with stdin JSON {"url":"...","token":"...","sdkRoot":"...","code":"..."}.
// Q4D_MCP_SDK_ROOT is the package directory containing package.json and dist/.
// --check imports the SDK and validates this script without making a request.
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { resolve, join, dirname } from 'node:path';
import { pathToFileURL, fileURLToPath } from 'node:url';

const expectedTools = [
  'list_instruments', 'get_instrument', 'query_kline', 'read_kline_file',
  'analyze_kline', 'execute_python', 'latest_bar_date', 'get_data_coverage',
  'list_news', 'get_news', 'list_events', 'get_event', 'list_indicators',
  'list_strategies', 'get_strategy', 'validate_strategy', 'create_strategy',
  'update_strategy', 'list_cost_models', 'run_backtest', 'list_backtests',
  'get_backtest_job', 'get_backtest_report', 'get_pipeline_node_types',
  'list_pipelines', 'get_pipeline', 'validate_pipeline', 'create_pipeline',
  'update_pipeline', 'dry_run_pipeline_safe', 'set_pipeline_status',
].sort();

let token = '';
const report = {
  check: 'pinned_mcp_sdk', ok: false, sdk_version: null,
  connected: false, session_received: false, tool_count: 0,
  calls: [], http: [], terminated: false, closed: false,
};
const safeError = error => ({
  name: String(error?.name || 'Error'),
  code: typeof error?.code === 'number' || typeof error?.code === 'string' ? error.code : undefined,
  message: String(error?.message || 'MCP SDK check failed')
    .split(token || '\u0000').join('[redacted]')
    .replace(/Bearer\s+[A-Za-z0-9._~+\/-]+=*/gi, 'Bearer [redacted]')
    .slice(0, 600),
});

async function main() {
  const checkOnly = process.argv.includes('--check');
  let input = {};
  if (!checkOnly && process.env.Q4D_MCP_CONFIG_STDIN === '1') {
    const chunks = [];
    let size = 0;
    for await (const chunk of process.stdin) {
      size += chunk.length;
      if (size > 16384) throw new Error('stdin configuration exceeds 16 KiB');
      chunks.push(chunk);
    }
    try { input = JSON.parse(Buffer.concat(chunks).toString('utf8')); }
    catch { throw new Error('stdin configuration must be valid JSON'); }
    if (!input || typeof input !== 'object' || Array.isArray(input)) throw new Error('stdin configuration must be an object');
  }
  const repoRoot = process.env.Q4D_REPO_ROOT || input.repoRoot || resolve(dirname(fileURLToPath(import.meta.url)), '..');
  const sdkRoot = resolve(process.env.Q4D_MCP_SDK_ROOT || input.sdkRoot || join(repoRoot, 'agent-runtime/node_modules/@modelcontextprotocol/sdk'));
  const sdkPackage = JSON.parse(await readFile(join(sdkRoot, 'package.json'), 'utf8'));
  assert.equal(sdkPackage.version, '1.29.0', 'the pinned MCP SDK must be version 1.29.0');
  report.sdk_version = sdkPackage.version;
  const { Client } = await import(pathToFileURL(join(sdkRoot, 'dist/esm/client/index.js')).href);
  const { StreamableHTTPClientTransport } = await import(pathToFileURL(join(sdkRoot, 'dist/esm/client/streamableHttp.js')).href);
  assert.equal(typeof Client, 'function');
  assert.equal(typeof StreamableHTTPClientTransport, 'function');
  if (checkOnly) {
    report.ok = true;
    report.network = 'not_requested';
    return;
  }

  const endpoint = process.env.Q4D_MCP_URL || input.url;
  if (typeof endpoint !== 'string' || !endpoint) throw new Error('Q4D_MCP_URL or stdin url is required');
  const url = new URL(endpoint);
  if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password || url.search || url.hash) {
    throw new Error('MCP URL must use HTTP(S) without credentials, query, or fragment');
  }
  const tokenFile = process.env.Q4D_MCP_TOKEN_FILE || input.tokenFile;
  if (typeof input.token === 'string') token = input.token.trim();
  else if (typeof tokenFile === 'string' && tokenFile) token = (await readFile(tokenFile, 'utf8')).trim();
  else throw new Error('Q4D_MCP_TOKEN_FILE or stdin token is required');
  if (!/^[A-Za-z0-9._~+\/-]{32,256}={0,2}$/.test(token)) throw new Error('MCP token has an invalid format');

  const fetchChecked = async (resource, init = {}) => {
    const entry = { method: init.method || 'GET' };
    if (typeof init.body === 'string') {
      try { entry.rpc = JSON.parse(init.body).method; } catch { /* SDK owns encoding. */ }
    }
    if (report.http.length < 80) report.http.push(entry);
    const timeout = AbortSignal.timeout(45000);
    const response = await fetch(resource, {
      ...init, signal: init.signal ? AbortSignal.any([init.signal, timeout]) : timeout,
    });
    entry.status = response.status;
    entry.type = response.headers.get('content-type')?.split(';')[0];
    return response;
  };
  const transport = new StreamableHTTPClientTransport(url, {
    requestInit: { headers: { authorization: `Bearer ${token}` } },
    fetch: fetchChecked,
    reconnectionOptions: { maxRetries: 0, initialReconnectionDelay: 1000, maxReconnectionDelay: 1000, reconnectionDelayGrowFactor: 1 },
  });
  const client = new Client({ name: 'quant4dad-live-sdk-check', version: '1.0.0' });
  const requestOptions = { timeout: 40000 };
  const call = async (name, args) => {
    const started = performance.now();
    const result = await client.callTool({ name, arguments: args }, undefined, requestOptions);
    assert.equal(result.isError, false, `${name} returned a tool error`);
    assert.equal(result.structuredContent?.untrusted_data, true, `${name} omitted the result envelope`);
    const text = result.content.find(item => item.type === 'text')?.text;
    assert.ok(text, `${name} omitted text content`);
    assert.deepEqual(JSON.parse(text), result.structuredContent, `${name} text and structured content differ`);
    report.calls.push({ tool: name, ok: true, elapsed_ms: Math.round(performance.now() - started) });
    return result.structuredContent.data;
  };

  let primaryError;
  try {
    await client.connect(transport, requestOptions);
    report.connected = true;
    report.session_received = typeof transport.sessionId === 'string' && transport.sessionId.length > 0;
    assert.equal(report.session_received, true, 'initialize must return a session');
    report.protocol_version = transport.protocolVersion;
    const tools = (await client.listTools(undefined, requestOptions)).tools;
    report.tool_count = tools.length;
    assert.deepEqual(tools.map(tool => tool.name).sort(), expectedTools, 'MCP must cover the complete Agent tool union');
    for (const tool of tools) {
      assert.ok(tool.inputSchema && tool.outputSchema, `${tool.name} omitted schema`);
      assert.match(tool._meta?.['q4d/catalog_revision'] || '', /^sha256:[0-9a-f]{64}$/, `${tool.name} omitted catalog revision`);
    }
    report.catalog_complete = true;
    const instruments = await call('list_instruments', { size: 20 });
    assert.ok(Array.isArray(instruments.items), 'instrument list is not an array');
    const preferredCode = process.env.Q4D_MCP_CODE || input.code || 'sh.600519';
    assert.match(preferredCode, /^(sh|sz|bj)\.[0-9]{6}$/, 'instrument code must be canonical');
    const candidates = [...new Set([preferredCode, ...instruments.items.map(item => item.code)])]
      .filter(code => typeof code === 'string' && /^(sh|sz|bj)\.[0-9]{6}$/.test(code)).slice(0, 6);
    let selected;
    for (const code of candidates) {
      const latest = await call('latest_bar_date', { code });
      if (latest.exists) { selected = code; break; }
    }
    if (selected) {
      const descriptor = await call('query_kline', { code: selected, limit: 120 });
      assert.match(descriptor.file_id || '', /^[0-9a-f]{64}$/, 'query_kline must return an owned-file descriptor');
      assert.ok(descriptor.count > 0 && descriptor.count <= 120, 'unexpected K-line count');
      const page = await call('read_kline_file', { file_id: descriptor.file_id, limit: 5 });
      assert.equal(page.total, descriptor.count, 'file count differs from query result');
      assert.ok(Array.isArray(page.rows) && page.rows.length <= 5, 'file paging is unbounded');
      const analysis = await call('analyze_kline', { file_id: descriptor.file_id, threshold_pct: 4, comparison: 'gte' });
      assert.equal(analysis.file_id, descriptor.file_id, 'analysis used a different source file');
      assert.equal(analysis.bar_count, descriptor.count, 'analysis lost source bars');
      report.kline = { code: selected, count: descriptor.count, page_count: page.rows.length, matched_count: analysis.matched_count };
    } else {
      report.kline = { skipped: 'no local bars among the bounded instrument candidates' };
    }

    const session = transport.sessionId;
    await transport.terminateSession();
    report.terminated = true;
    assert.equal(transport.sessionId, undefined, 'SDK did not clear the terminated session');
    // Verify the actual DELETE revoked the server-owned session, beyond local close().
    const revoked = await fetchChecked(url, {
      method: 'POST', headers: {
        authorization: `Bearer ${token}`, 'content-type': 'application/json', accept: 'application/json',
        'Mcp-Session-Id': session,
      },
      body: JSON.stringify({ jsonrpc: '2.0', id: 'verify-closed', method: 'ping' }),
    });
    await revoked.body?.cancel();
    assert.equal(revoked.status, 404, 'terminated session remained valid');
    report.revocation_verified = true;
  } catch (error) {
    primaryError = error;
  } finally {
    if (transport.sessionId) {
      try { await transport.terminateSession(); report.terminated = true; }
      catch (error) { report.cleanup_error = safeError(error); }
    }
    try { await client.close(); report.closed = true; }
    catch (error) { report.close_error = safeError(error); primaryError ||= error; }
  }
  if (primaryError) throw primaryError;
  report.ok = true;
}

try { await main(); }
catch (error) { report.error = safeError(error); process.exitCode = 1; }
process.stdout.write(`${JSON.stringify(report, null, 2)}\n`);
