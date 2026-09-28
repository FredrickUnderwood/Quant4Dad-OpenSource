import {
  LlmAdapter,
  ReasoningEffortId,
  ToolCallId,
} from '@deepseek-ai/dsh-llm'
import { appendFileSync } from 'node:fs'

const provider = 'q4d-control-fixture'
const expectedTools = ['mcp__q4d__query_kline']

class Quant4DadControlAdapter extends LlmAdapter {
  providerInfo(candidate) {
    if (candidate !== provider) throw new Error(`unknown fixture provider: ${candidate}`)
    return { id: candidate, name: 'Q4D control fixture' }
  }

  listModels(candidate) {
    if (candidate !== provider) return Promise.resolve([])
    return Promise.resolve([
      { provider, id: 'alpha', name: 'Alpha', inputModalities: ['text'] },
    ])
  }

  resolveModel(candidate, model) {
    return Promise.resolve({
      provider: candidate,
      id: model,
      name: model,
      inputModalities: ['text'],
      context: { contextWindow: 2048 },
      reasoning: {
        efforts: [{ id: ReasoningEffortId('low'), name: 'Low' }],
        defaultEffort: ReasoningEffortId('low'),
      },
    })
  }

  async * stream(options) {
    // Test-only evidence of the messages actually delivered to the model.
    if (process.env.Q4D_T00_MODEL_CALL_LOG) {
      appendFileSync(process.env.Q4D_T00_MODEL_CALL_LOG,
        `${JSON.stringify({ messages: options.messages, purpose: options.purpose })}\n`, { mode: 0o600 })
    }
    const lastUserIndex = options.messages.findLastIndex(message => message.source.kind === 'user')
    const current = options.messages.slice(lastUserIndex)
    const userText = current.flatMap(message => message.content)
      .flatMap(block => block.type === 'text' ? [block.text] : [])
      .join('')
    const actualTools = (options.tools ?? []).map(tool => tool.name).sort()
    if (userText.includes('model-error')) throw new Error('Q4D_T00_MODEL_FAILURE')
    const allowedTools = userText.includes('no-tools') ? [] : expectedTools
    if (JSON.stringify(actualTools) !== JSON.stringify(allowedTools)) {
      throw new Error(`unexpected model-visible tools: ${JSON.stringify(actualTools)}`)
    }
    if (options.purpose === 'compaction') {
      const text = 'Q4D_T00_SUMMARY: queried 000001; tool returned close 100.'
      yield { type: 'block-start', index: 0, blockType: 'text' }
      yield { type: 'text-delta', index: 0, text }
      yield { type: 'block-end', index: 0, block: { type: 'text', text } }
      yield { type: 'usage', usage: { inputTokens: 300, outputTokens: 20 } }
      yield { type: 'finish', reason: { kind: 'stop' } }
      return
    }
    const resultCount = current.flatMap(message => message.content).filter(block => block.type === 'tool-result').length
    const hasToolResult = resultCount > 0

    const callId = ToolCallId(userText.includes('cancel') ? 'q4d-cancel-query' : 'q4d-query')
    const forged = userText.match(/forge-field:([a-z_]+)/)?.[1]
    const symbol = userText.includes('failure') ? 'FAIL'
      : userText.includes('gateway-approval') ? 'APPROVAL'
        : userText.includes('gateway-wait') ? 'WAIT' : '000001'
    const args = userText.includes('invalid-json') ? '{invalid json'
      : userText.includes('invalid-array') ? '[]'
        : JSON.stringify({ symbol, limit: 2, ...(forged ? { [forged]: 'forged' } : {}) })
    const toolName = userText.includes('unknown-tool') || userText.includes('mixed-tools') && resultCount === 0 ? 'shell' : expectedTools[0]
    const repeat = (userText.includes('mixed-tools') || userText.includes('repeat-tool')) && resultCount < 2
    if ((!hasToolResult || repeat) && allowedTools.length > 0) {
      yield { type: 'block-start', index: 0, blockType: 'tool-call' }
      yield {
        type: 'tool-call-delta',
        index: 0,
        id: callId,
        name: toolName,
        argumentsDelta: args,
      }
      yield {
        type: 'block-end',
        index: 0,
        block: {
          type: 'tool-call',
          id: callId,
          name: toolName,
          arguments: args,
        },
      }
      yield { type: 'usage', usage: { inputTokens: 8, outputTokens: 5 } }
      yield { type: 'finish', reason: { kind: 'tool-calls' } }
      return
    }

    if (userText.includes('cancel')) {
      await new Promise((_resolve, reject) => {
        if (options.signal?.aborted === true) {
          reject(new Error('cancelled'))
          return
        }
        options.signal?.addEventListener('abort', () => reject(new Error('cancelled')), { once: true })
      })
      return
    }

    const text = 'Q4D_T00_OK'
    yield { type: 'block-start', index: 0, blockType: 'text' }
    yield { type: 'text-delta', index: 0, text }
    yield { type: 'block-end', index: 0, block: { type: 'text', text } }
    yield { type: 'usage', usage: { inputTokens: 13, outputTokens: 4 } }
    yield { type: 'finish', reason: { kind: 'stop' } }
  }
}

export const name = 'q4d-control-fixture'
export const inject = ['llm']

export function apply(ctx) {
  ctx.llm.registerAdapter([provider], new Quant4DadControlAdapter())
}
