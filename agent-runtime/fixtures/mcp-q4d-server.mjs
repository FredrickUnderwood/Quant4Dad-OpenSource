import { appendFile } from 'node:fs/promises'
import { McpServer } from '@modelcontextprotocol/sdk/server/mcp.js'
import { StdioServerTransport } from '@modelcontextprotocol/sdk/server/stdio.js'
import { z } from 'zod'

const server = new McpServer(
  { name: 'q4d-t00-fixture', version: '1.0.0' },
  { capabilities: { tools: {} } },
)

server.registerTool('query_kline', {
  title: 'Query K-line',
  description: 'Returns a bounded deterministic K-line fixture.',
  inputSchema: {
    symbol: z.string(),
    limit: z.number().int().min(1).max(500),
  },
}, async ({ symbol, limit }) => {
  if (process.env.Q4D_T00_MCP_CALL_LOG) {
    await appendFile(
      process.env.Q4D_T00_MCP_CALL_LOG,
      `${JSON.stringify({ symbol, limit })}\n`,
      { encoding: 'utf8', mode: 0o600 },
    )
  }
  if (symbol === 'FAIL') {
    return { isError: true, content: [{ type: 'text', text: 'Q4D_T00_TOOL_FAILURE' }] }
  }
  return {
    content: [{
      type: 'text',
      text: JSON.stringify({ symbol, bars: [{ date: '2026-09-04', close: 100 }] }),
    }],
  }
})

await server.connect(new StdioServerTransport())
