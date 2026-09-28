import test from 'node:test';
import assert from 'node:assert/strict';
import { consumeSSE, applyEvent, initialRun, validCursor } from '../src/agent/events.ts';
import { AgentAPIError, agentAPI } from '../src/agent/api.ts';
import { watchRun } from '../src/agent/watch.ts';
import type { AgentEvent, Run, RunView } from '../src/agent/types.ts';
import { resultFailureText, resultLink, resultText } from '../src/agent/tool-result.ts';

const event = (id: string, type = 'message.delta', data: Record<string, unknown> = { message_id: 'answer', text: '你好 🐉' }): AgentEvent => ({ id, type, data, session_id: 'session', run_id: 'run', schema_version: 1 });
const frame = (e: AgentEvent) => `id: ${e.id}\nevent: ${e.type}\ndata: ${JSON.stringify(e)}\n\n`;
function stream(text: string, fragmented = false) {
  const bytes = new TextEncoder().encode(text);
  return new ReadableStream<Uint8Array>({ start(c) { if (fragmented) for (const b of bytes) c.enqueue(new Uint8Array([b])); else c.enqueue(bytes); c.close(); } });
}
const projection = (terminal = false): Run => ({ run_id: 'run', session_id: 'session', message_id: 'user', state: terminal ? 'completed' : 'running', durable: true, terminal, last_event_id: terminal ? '2' : '1' });
test('SSE preserves fragmented Unicode, comment heartbeats and decimal IDs above Number precision', async () => {
  const seen: AgentEvent[] = [];
  const raw = ': keepalive\n\n' + frame(event('9007199254740993')) + frame(event('9007199254740994', 'run.completed', {}));
  const cursor = await consumeSSE(stream(raw.replaceAll('\n', '\r\n'), true), 'session', 'run', '9007199254740992', e => seen.push(e), new AbortController().signal);
  assert.equal(cursor, '9007199254740994'); assert.equal(seen[0].data.text, '你好 🐉'); assert.equal(seen.length, 2);
  for (const bad of ['01', '1\n', '1.0', '-1', '1'.repeat(1025)]) assert.equal(validCursor(bad), false);
});
test('SSE ignores replay duplicates and rejects identity, gaps, bad payloads and partial frames', async () => {
  const seen: AgentEvent[] = [];
  await consumeSSE(stream(frame(event('1')) + frame(event('1')) + frame(event('2', 'run.completed', {}))), 'session', 'run', '0', e => seen.push(e), new AbortController().signal);
  assert.equal(seen.length, 2);
  for (const raw of [frame(event('2')), frame({ ...event('1'), run_id: 'other' }), frame(event('1', 'message.delta', { text: 1 })), frame(event('1')).trimEnd(), 'data: ' + 'x'.repeat(1048577)]) {
    await assert.rejects(consumeSSE(stream(raw), 'session', 'run', '0', () => assert.fail('invalid event accepted'), new AbortController().signal));
  }
});
test('SSE consumer cancellation closes the read without requiring a Run cancel request', async () => {
  const abort = new AbortController(); let cancelled = false;
  const body = new ReadableStream<Uint8Array>({ cancel() { cancelled = true; } });
  const reading = consumeSSE(body, 'session', 'run', '0', () => {}, abort.signal);
  abort.abort(); await assert.rejects(reading); assert.equal(cancelled, true);
});
test('Reducer replaces completed text, deduplicates replay and preserves terminal state', () => {
  let state = applyEvent(initialRun('session', 'run'), event('1'));
  assert.equal(applyEvent(state, event('1')), state);
  state = applyEvent(state, event('2', 'message.completed', { message_id: 'answer', text: '完整回答' }));
  assert.equal(state.messages[0].text, '完整回答');
  state = applyEvent(state, event('3', 'run.cancelled', { reason: 'user' }));
  assert.equal(state.terminal, true); assert.equal(state.status, 'cancelled');
  assert.throws(() => applyEvent(state, event('4')));
});
test('Watcher reconnects from accepted cursor and reconciles EOF without inventing completion', async () => {
  const cursors: string[] = [], views: RunView[] = []; let queries = 0, reloads = 0;
  await watchRun('session', 'run', new AbortController().signal, v => views.push(v), async () => { reloads++; }, {
    events: async (_session, _run, cursor, accept) => { cursors.push(cursor); if (cursors.length === 1) { accept(event('1')); throw new AgentAPIError(503, ''); } accept(event('2', 'run.completed', {})); return '2'; },
    run: async () => { queries++; return projection(); },
  }, async () => {});
  assert.deepEqual(cursors, ['0', '1']); assert.equal(queries, 1); assert.equal(reloads, 1);
  assert.equal(views.at(-1)?.terminal, true); assert.deepEqual(views.at(-1)?.messages, []);
});
test('Expired replay reloads transcript then polls without resetting the event cursor', async () => {
  let streams = 0, queries = 0, reloads = 0;
  const views: RunView[] = [];
  await watchRun('session', 'run', new AbortController().signal, v => views.push(v), async () => { reloads++; }, {
    events: async (_s, _r, _c, accept) => { streams++; accept(event('1')); throw new AgentAPIError(410, 'agent_event_cursor_expired'); },
    run: async () => projection(++queries === 2),
  }, async () => {});
  assert.equal(streams, 1); assert.equal(queries, 2); assert.equal(reloads, 3);
  assert.equal(views.at(-1)?.cursor, '1'); assert.equal(views.at(-1)?.terminal, true); assert.deepEqual(views.at(-1)?.messages, []);
});
test('Watcher stops retrying on authentication failure and bounds unavailable reconnects', async () => {
  let calls = 0;
  await assert.rejects(watchRun('session', 'run', new AbortController().signal, () => {}, async () => {}, {
    events: async () => { calls++; throw new AgentAPIError(401, ''); }, run: async () => assert.fail(),
  }, async () => {}), AgentAPIError);
  assert.equal(calls, 1); calls = 0;
  await assert.rejects(watchRun('session', 'run', new AbortController().signal, () => {}, async () => {}, {
    events: async () => { calls++; throw new AgentAPIError(503, ''); }, run: async () => { throw new AgentAPIError(503, ''); },
  }, async () => {}));
  assert.equal(calls, 9);
});
test('API errors keep recovery IDs while hiding arbitrary upstream diagnostics', async t => {
  const original = globalThis.fetch; t.after(() => { globalThis.fetch = original; });
  globalThis.fetch = async () => new Response(JSON.stringify({ message: 'agent_run_delivery_unconfirmed', run_id: '01K00000000000000000000000' }), { status: 503 });
  await assert.rejects(agentAPI.run('run'), error => error instanceof AgentAPIError && error.runID === '01K00000000000000000000000');
  globalThis.fetch = async () => new Response('<private-proxy-error>', { status: 502 });
  await assert.rejects(agentAPI.run('run'), error => error instanceof AgentAPIError && !error.message.includes('private'));
});

test('Tool progress survives replay without treating a proposal or rejection as execution', () => {
  const id = '01M00000000000000000000000';
  let state = applyEvent(initialRun('session', 'run'), event('1', 'tool.proposed', { tool_call_id: id, name: 'query_kline' }));
  assert.equal(state.tools[0].status, 'proposed');
  assert.equal(applyEvent(state, event('1', 'tool.proposed', {})), state);
  assert.throws(() => applyEvent(state, event('2', 'tool.completed', { tool_call_id: id })));
  state = applyEvent(state, event('2', 'tool.started', { tool_call_id: id }));
  state = applyEvent(state, event('3', 'tool.completed', { tool_call_id: id }));
  assert.equal(state.tools[0].status, 'completed'); assert.equal(state.tools.length, 1);
  assert.throws(() => applyEvent(state, event('4', 'tool.started', { tool_call_id: id })));
  const rejected = applyEvent(applyEvent(initialRun('session', 'run'), event('1', 'tool.proposed', { tool_call_id: id, name: 'unknown' })), event('2', 'tool.failed', { tool_call_id: id }));
  assert.equal(rejected.tools[0].status, 'failed');
});
test('Watcher never leaves a query running or invents success when its outcome was lost before EOF', async () => {
  const views: RunView[] = [], id = '01M00000000000000000000000';
  await watchRun('session', 'run', new AbortController().signal, v => views.push(v), async () => {}, {
    events: async (_s, _r, _c, accept) => { accept(event('1', 'tool.proposed', { tool_call_id: id, name: 'query_kline' })); accept(event('2', 'tool.started', { tool_call_id: id })); return '2'; },
    run: async () => projection(true),
  }, async () => {});
  assert.equal(views.at(-1)?.tools[0].status, 'unknown'); assert.equal(views.at(-1)?.terminal, true);
});

test('Stopping a run preserves partial output that has not reached the transcript', async () => {
  const views: RunView[] = [];
  await watchRun('session', 'run', new AbortController().signal, value => views.push(value), async () => {}, {
    events: async (_s, _r, _c, accept) => { accept(event('1')); accept(event('2', 'run.cancelled', {})); return '2'; },
    run: async () => assert.fail('terminal event already received'),
  }, async () => {});
  assert.equal(views.at(-1)?.terminal, true);
  assert.equal(views.at(-1)?.messages[0].text, '你好 🐉');
});

test('Approval cards preserve exact parameters, require the matching proposed Tool and resume after rejection', () => {
  const id = '01M00000000000000000000000';
  let state = applyEvent(initialRun('session', 'run'), event('1', 'tool.proposed', { tool_call_id: id, name: 'create_pipeline', arguments: { pipeline: { name: '<script>test</script>', edges: [{ condition: null }] } } }));
  assert.equal(state.tools[0].argumentsOmitted, false); assert.ok(state.tools[0].arguments?.includes('null'));
  const data = { tool_call_id: id, name: 'create_pipeline', approval_id: 'a'.repeat(64), arguments_hash: 'sha256:' + 'b'.repeat(64), risk: 'R3', expires_at: new Date(Date.now() + 60000).toISOString() };
  assert.throws(() => applyEvent(state, event('2', 'approval.required', { ...data, name: 'other' })));
  state = applyEvent(state, event('2', 'approval.required', data));
  assert.equal(state.status, 'waiting_approval'); assert.equal(state.tools[0].status, 'waiting_approval');
  assert.throws(() => applyEvent(state, event('3', 'tool.completed', { tool_call_id: id })));
  state = applyEvent(state, event('3', 'tool.failed', { tool_call_id: id, code: 'agent_approval_denied' }));
  assert.equal(state.status, 'running'); assert.equal(state.tools[0].code, 'agent_approval_denied'); assert.equal(state.terminal, false);
});

test('Result links accept only successful fixed product routes and safe integer IDs', () => {
  const value = { run_id: 'run', tool_call_id: 'call', tool_name: 'create_strategy', status: 'succeeded', risk: 'R2', result: { data: { id: 3, url: 'javascript:alert(1)', html: '<script>test</script>' }, untrusted_data: true } };
  assert.deepEqual(resultLink(value), { href: '/strategies/3/edit', label: '查看策略' });
  for (const id of ['3', -1, 0, 1.5, Number.MAX_SAFE_INTEGER + 1, 'javascript:alert(1)']) assert.equal(resultLink({ ...value, result: { ...value.result, data: { id } } }), undefined);
  assert.equal(resultLink({ ...value, status: 'failed' }), undefined);
  assert.equal(resultLink({ ...value, tool_name: 'get_news' }), undefined);
  assert.ok(resultText(value).includes('<script>test</script>')); // The React caller renders this as text, never HTML.
  assert.ok(resultText({ ...value, result: { data: 'x'.repeat(150000), untrusted_data: true } }).length < 132000);
});

test('Recovery progress remains a live Run and is preserved on SSE replay', async () => {
  let state = initialRun('session', 'run');
  const raw = frame(event('1', 'run.progress', { stage: 'output_recovery' })) + frame(event('2', 'run.completed', {}));
  await consumeSSE(stream(raw), 'session', 'run', '0', e => {
    state = applyEvent(state, e);
    if (e.type === 'run.progress') { assert.equal(state.terminal, false); assert.match(state.notice!, /截断/); }
  }, new AbortController().signal);
  assert.equal(state.status, 'completed');
  assert.equal(state.notice, '');
  assert.throws(() => applyEvent(initialRun('session', 'run'), event('1', 'run.progress', { stage: 'private-provider-text' })));
});

test('Guard rejection has no business result or link and does not claim execution', () => {
  const value = { run_id: 'run', tool_call_id: 'call', tool_name: 'create_pipeline', status: 'failed', execution_stage: 'pre_dispatch' as const, error_code: 'agent_tool_context_or_arguments_rejected' };
  assert.equal(resultText(value), '');
  assert.equal(resultLink(value), undefined);
  assert.match(resultFailureText(value), /执行前校验未通过，工具未执行/);
  assert.match(resultFailureText({ ...value, error_code: 'agent_tool_forbidden' }), /执行前被拒绝，工具未执行/);
  assert.match(resultFailureText({ ...value, execution_stage: undefined, error_code: 'tool_outcome_unknown' }), /结果无法确认/);
  assert.doesNotMatch(resultFailureText({ ...value, execution_stage: undefined }), /工具未执行/);
  assert.match(new AgentAPIError(404, 'agent_tool_not_found').message, /工具调用/);
});
