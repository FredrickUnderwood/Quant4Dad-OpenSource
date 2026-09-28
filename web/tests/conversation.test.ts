import test from 'node:test';
import assert from 'node:assert/strict';
import { applyEvent, initialRun } from '../src/agent/events.ts';
import { conversationRows, runActivity } from '../src/agent/conversation.ts';
import type { AgentEvent, RunView, Transcript, TranscriptItem } from '../src/agent/types.ts';

const call = '01M00000000000000000000000';
function event(id: number, type: string, data: Record<string, unknown> = {}): AgentEvent {
  return { id: String(id), type, data, session_id: 'session', run_id: 'run', schema_version: 1 };
}
function run() {
  let state = initialRun('session', 'run');
  for (const e of [
    event(1, 'message.completed', { message_id: 'intro', text: '先检查行情。' }),
    event(2, 'tool.proposed', { tool_call_id: call, name: 'query_kline', arguments: { code: 'sh.600519' } }),
    event(3, 'tool.started', { tool_call_id: call }),
    event(4, 'tool.completed', { tool_call_id: call }),
    event(5, 'message.delta', { message_id: 'answer', text: '## 结论' }),
    event(6, 'message.completed', { message_id: 'answer', text: '## 结论\n查询已完成。' }),
  ]) state = applyEvent(state, e);
  return state;
}
function item(id: string, role: string, content: TranscriptItem['content'], runID = 'run'): TranscriptItem {
  return { message_id: id, role, content, run_id: runID, seq: '1', occurred_at: '', omitted_content: false };
}
function history(items: TranscriptItem[]): Transcript {
  return { session_id: 'session', snapshot_seq: '10', items, has_more: false, next_before_seq: null };
}
const ids = (view: ReturnType<typeof conversationRows>) => view.map(row => row.id);

test('Live tool calls sit between the corresponding messages and stay ordered after history reload', () => {
  const state = run(), user = item('user', 'user', [{ type: 'text', text: '查一下行情' }]);
  const expected = ['user', 'intro', call, 'answer'];
  assert.deepEqual(ids(conversationRows(history([user]), state)), expected);
  const persisted = history([user, item('intro', 'assistant', [{ type: 'text', text: '先检查行情。' }]), item('answer', 'assistant', [{ type: 'text', text: '## 结论\n查询已完成。' }])]);
  assert.deepEqual(ids(conversationRows(persisted, state)), expected);
  assert.deepEqual(ids(conversationRows(persisted, { ...state, terminal: true, messages: [] })), expected);
});

test('Archival tool records remain read-only, before the answer, including pagination around other runs', () => {
  const state = run();
  const persisted = history([
    item('old', 'assistant', [{ type: 'text', text: '更早的回复' }], 'old-run'),
    item('user', 'user', [{ type: 'text', text: '查行情' }]),
    item('intro', 'assistant', [{ type: 'text', text: '先检查行情。' }]),
    item('request', 'assistant', [{ type: 'tool_request', name: 'query_kline', arguments: '{"code":"sh.600519"}' }]),
    item('result', 'tool', [{ type: 'tool_result', content: [{ type: 'text', text: '<script>untrusted</script>' }] }]),
    item('answer', 'assistant', [{ type: 'text', text: '结果' }]),
    item('next-user', 'user', [{ type: 'text', text: '再问一个问题' }], 'next-run'),
  ]);
  const rows = conversationRows(persisted, { ...state, messages: [], terminal: true });
  assert.deepEqual(ids(rows), ['old', 'user', 'intro', call, 'request-0', 'result-0', 'answer', 'next-user']);
  assert.equal(rows.find(row => row.id === 'request-0')?.kind, 'archive');
  assert.equal(rows.find(row => row.id === 'result-0')?.kind, 'archive');
  assert.equal(conversationRows(persisted, null).some(row => row.kind === 'tool'), false);
});

test('Streaming completion replaces partial Markdown without duplicating a message', () => {
  let state = initialRun('session', 'run');
  state = applyEvent(state, event(1, 'message.delta', { message_id: 'answer', text: '## 标' }));
  assert.equal(state.messages[0].streaming, true);
  state = applyEvent(state, event(2, 'message.delta', { message_id: 'answer', text: '题' }));
  assert.equal(state.messages[0].text, '## 标题');
  state = applyEvent(state, event(3, 'message.completed', { message_id: 'answer', text: '## 标题\n完整正文' }));
  const rows = conversationRows(null, state);
  assert.equal(rows.length, 1);
  assert.equal(state.messages[0].streaming, false);
  assert.equal(state.order.length, 1);
});

test('Activity reflects waiting, tools, output, approval, disconnect and actual terminal outcomes', () => {
  const state = initialRun('session', 'run');
  const activity = (patch: Partial<RunView> = {}, active = true) => runActivity({ ...state, ...patch }, false, active, false);
  assert.equal(activity(), '正在思考');
  assert.equal(runActivity(null, true, false, false), '正在发送');
  assert.equal(activity({ tools: [{ id: call, name: 'query_kline', status: 'started' }] }), '正在执行工具');
  assert.equal(activity({ messages: [{ id: 'answer', text: '内容', omitted: false, streaming: true }] }), '正在生成回答');
  assert.equal(activity({ status: 'waiting_approval' }), '等待你的审批');
  assert.equal(activity({ status: 'cancelling' }), '正在停止');
  assert.equal(activity({ terminal: true, status: 'cancelled' }, false), '已停止');
  assert.equal(activity({ terminal: true, status: 'failed' }, false), '回答未完成');
  assert.equal(runActivity(state, false, true, true), '连接已断开');
});


test('Budget failures preserve precise reasons through event replay', () => {
  for (const [dimension, label] of Object.entries({ output_per_call: '单次生成达到输出上限（含思考）', tool_calls: '本轮工具调用次数已达上限', input_tokens: '本轮输入预算不足' })) {
    const state = applyEvent(initialRun('session', 'run'), event(1, 'run.failed', { code: 'agent_model_limit', retryable: false, budget: { dimension, used: 16, limit: 16, requested: 1 } }));
    assert.equal(runActivity(state, false, false, false), label);
    assert.equal(state.failure?.budget?.dimension, dimension);
    assert.equal(applyEvent(state, event(1, 'run.failed', {})), state);
  }
});
