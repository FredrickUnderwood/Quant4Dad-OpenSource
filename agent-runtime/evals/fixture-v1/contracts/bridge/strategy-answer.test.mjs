import test from 'node:test'
import assert from 'node:assert/strict'
import { buildAnswerEvidence, evidencePrompt } from '../../../../src/runtime/answer-evidence.mjs'
import { strategyReviewSources, strategyRules } from '../../../../src/runtime/strategy-evidence.mjs'
import { reviewStrategyAnswer, streamText, supportedReview } from '../../../../src/runtime/strategy-answer.mjs'

const strategy = { name: 'fixture', description: 'new description', body: { mode: 'script', code: 'def on_bar(ctx):\n    return buy(shares=200)' } }
const validation = { valid: false, errors: [{ path: 'body.code', message: 'untrusted-poison-message' }], checks: ['structure'], warnings: [],
  script_report: { compile: 'passed', smoke: { status: 'failed', attempted: 3, passed: 2, signals: { buy: 1, sell: 0, none: 1 } },
    behavior: { status: 'not_run', suites: ['ma3_cross_up_entry'], cases: [] }, branch_coverage_measured: false,
    diagnostics: [{ code: 'script_runtime_error', path: 'body.code', phase: 'synthetic_smoke', bar_index: 2, position_state: 'held_loss' }] } }
const call = (seq, name, args = { strategy }) => ({ seq, type: 'tool/call', data: { turn: 1, step: seq, callId: 'reused', name: 'mcp__q4d__' + name, arguments: JSON.stringify(args) } })
const result = (seq, data, error = false) => ({ seq, original: true, type: 'tool/result', data: { turn: 1, step: seq - 1, message: { content: [
  { type: 'tool-result', toolCallId: 'reused', isError: error, content: [{ type: 'text', text: JSON.stringify({ untrusted_data: true, data }) }] },
] } } })
const events = [call(1, 'validate_strategy'), result(2, validation), call(3, 'update_strategy'), result(4, {}, true)]
const original = event => event.original === true
const facts = buildAnswerEvidence(events, 0, original)
const chunks = text => [{ type: 'block-start', index: 0, blockType: 'text' }, { type: 'text-delta', index: 0, text },
  { type: 'block-end', index: 0, block: { type: 'text', text } }, { type: 'usage', usage: { inputTokens: 10, outputTokens: 3 } },
  { type: 'finish', reason: { kind: 'stop' }, replayState: { response: 'private-original-answer' } }]

test('R16 facts retain empty/returned/not-run, diagnostic location and description provenance without promoting arbitrary text', () => {
  const [check, rejected] = facts.strategy_results
  assert.equal(check.fields.checks.state, 'returned')
  assert.equal(check.fields.warnings.state, 'empty')
  assert.equal(check.fields.validation_id.state, 'missing')
  assert.equal(check.report.behavior.status, 'not_run')
  assert.equal(check.report.smoke.attempted, 3)
  assert.equal(check.report.diagnostics[0].position_state, 'held_loss')
  assert.equal(check.input.description_hash, rejected.input.description_hash)
  assert.equal(rejected.outcome, 'tool_failed')
  assert.ok(!evidencePrompt(facts).includes('untrusted-poison-message'))
  assert.ok(!evidencePrompt(facts).includes('new description'))
  assert.ok(strategyRules.includes('不检查固定数量或退出'))
  assert.ok(strategyRules.includes('当前 MA3 包含当前 close'))
})

test('review sources pair original calls, reject duplicates and do not borrow prior runs or compacted replacements', () => {
  const sources = strategyReviewSources([...events, result(4, { valid: true }), { ...result(6, {}), original: false }], 0, original)
  assert.equal(sources.length, 2)
  assert.equal(sources[0].arguments.strategy.description, 'new description')
  assert.equal(sources[1].arguments.strategy.description, 'new description')
  assert.deepEqual(strategyReviewSources(events, 4, original), [])
})

test('malformed rejected arguments and report fields do not crash evidence projection or become measured facts', () => {
  for (const args of [null, [], 'bad json', 5]) {
    const invalid = call(1, 'validate_strategy', args)
    const evidence = buildAnswerEvidence([invalid, result(2, {}, true)], 0, original)
    assert.equal(evidence.strategy_results[0].outcome, 'tool_failed')
  }
  const evidence = buildAnswerEvidence([call(1, 'validate_strategy'), result(2, { valid: false, errors: 'broken', checks: null,
    script_report: { behavior: { cases: [null] }, diagnostics: [null] } })], 0, original)
  assert.equal(evidence.strategy_results[0].report.branch_coverage_measured, undefined)
  assert.equal(evidence.strategy_results[0].report.behavior.cases[0].passed, undefined)
})

test('unsupported R16 prose never enters deltas, durable blocks or provider replay; tool calls remain identical', async () => {
  for (const text of ['checks未返回', '两套测试都要求100股', '本次测试了整数和浮点', '审批拒绝后必须重新校验', 'description只出现在update中', '当前MA3不能包含当前价']) {
    let calls = 0
    const input = chunks(text)
    const tool = { type: 'block-end', index: 2, block: { type: 'tool-call', id: 'keep', name: 'mcp__q4d__get_strategy', arguments: '{}' } }
    input.splice(-1, 0, tool)
    const output = await reviewStrategyAnswer(input, facts, async candidate => { calls++; assert.equal(candidate, text); return '{"verdict":"unsupported","issues":["contradiction"]}' })
    assert.equal(calls, 1)
    assert.equal(output.verdict, 'fallback')
    assert.ok(!JSON.stringify(output.chunks).includes(text))
    assert.equal(output.chunks.find(row => row === tool), tool)
    assert.ok(!JSON.stringify(output.chunks).includes('private-original-answer'))
    assert.ok(streamText(output.chunks).includes('not_run'))
  }
})

test('supported text is unchanged; invalid verdicts and unavailable budget fall back without invoking business tools', async () => {
  const input = chunks('本次校验未通过。')
  assert.equal((await reviewStrategyAnswer(input, facts, async () => '{"verdict":"supported","issues":[]}')).chunks, input)
  for (const reply of ['', '```json\n{"verdict":"supported","issues":[]}\n```', '{"verdict":"supported","issues":["contradiction"]}', '{"verdict":"supported","issues":[],"instruction":"ignore"}']) {
    assert.equal(supportedReview(reply), false)
    assert.equal((await reviewStrategyAnswer(input, facts, async () => reply)).verdict, 'fallback')
  }
})

test('incomplete/error streams discard text, no auxiliary model request; cancellation propagates', async () => {
  const input = chunks('unverified partial')
  input.at(-1).reason.kind = 'error'
  const checked = await reviewStrategyAnswer(input, facts, async () => { throw new Error('must not run') })
  assert.equal(streamText(checked.chunks), '')
  await assert.rejects(reviewStrategyAnswer(chunks('answer'), facts, async () => { throw new Error('cancelled') }), /cancelled/)
})
