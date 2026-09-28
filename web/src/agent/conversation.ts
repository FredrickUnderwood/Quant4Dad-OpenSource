import type { LiveTool, RunView, Transcript } from './types.ts';

export type ConversationRow =
  | { kind: 'message'; id: string; role: string; text: string; omitted: boolean; streaming?: boolean }
  | { kind: 'archive'; id: string; name?: string; text: string; failed: boolean }
  | { kind: 'tool'; id: string; tool: LiveTool };

/** History owns persisted content; replay owns live ordering and actionable calls.
 * Archival tool requests have no authorized call ID, so remain read-only. */
export function conversationRows(history: Transcript | null, view: RunView | null): ConversationRow[] {
  const rows: ConversationRow[] = [];
  const messages = new Map(view?.messages.map(message => [message.id, message]));
  const currentRows = new Map<string, ConversationRow[]>();
  const messageOrder = new Map(view?.order.flatMap((entry, index) => entry.kind === 'message' ? [[entry.id, index] as const] : []));
  const archived: Array<{ rank: number; rows: ConversationRow[] }> = [];
  let unplaced: ConversationRow[] = [];
  let insertAt = -1;
  for (const item of history?.items ?? []) {
    const blocks: ConversationRow[] = [];
    const live = messages.get(item.message_id);
    const text = live?.text ?? item.content.filter(block => block.type === 'text').map(block => block.text ?? '').join('\n');
    if (text || item.omitted_content) blocks.push({ kind: 'message', id: item.message_id, role: item.role, text, omitted: live?.omitted ?? item.omitted_content, streaming: live?.streaming });
    item.content.forEach((block, index) => {
      if (block.type === 'tool_request' || block.type === 'tool_result') blocks.push({
        kind: 'archive', id: `${item.message_id}-${index}`, name: block.type === 'tool_request' ? block.name : undefined,
        text: block.type === 'tool_request' ? block.arguments ?? '' : block.content?.map(part => part.text ?? '').join('\n') ?? '', failed: !!block.is_error,
      });
    });
    if (view && item.run_id === view.runID && item.role !== 'user') {
      const rank = messageOrder.get(item.message_id);
      if (rank !== undefined) {
        if (unplaced.length) archived.push({ rank: rank - .5, rows: unplaced });
        unplaced = []; currentRows.set(item.message_id, blocks);
      } else unplaced.push(...blocks);
      if (insertAt < 0) insertAt = rows.length;
    } else {
      rows.push(...blocks);
      if (view && item.run_id === view.runID && item.role === 'user') insertAt = rows.length;
    }
  }
  if (!view) return rows;
  if (unplaced.length) archived.push({ rank: view.order.length, rows: unplaced });
  const tools = new Map(view.tools.map(tool => [tool.id, tool]));
  const replay: Array<{ rank: number; rows: ConversationRow[] }> = [...archived];
  for (const [rank, entry] of view.order.entries()) {
    if (entry.kind === 'tool') {
      const tool = tools.get(entry.id);
      if (tool) replay.push({ rank, rows: [{ kind: 'tool', id: tool.id, tool }] });
    } else {
      const persisted = currentRows.get(entry.id), live = messages.get(entry.id);
      if (persisted) replay.push({ rank, rows: persisted });
      else if (live) replay.push({ rank, rows: [{ kind: 'message', ...live, role: 'assistant' }] });
    }
  }
  rows.splice(insertAt < 0 ? rows.length : insertAt, 0, ...replay.sort((a, b) => a.rank - b.rank).flatMap(entry => entry.rows));
  return rows;
}

export function runActivity(view: RunView | null, posting: boolean, active: boolean, disconnected: boolean) {
  if (posting) return '正在发送';
  if (disconnected) return '连接已断开';
  if (!active && view?.status === 'failed' && view.failure) {
    const dimension = view.failure.budget?.dimension;
    const labels: Record<string, string> = { output_per_call: '单次生成达到输出上限（含思考）', model_calls: '本轮模型调用次数已达上限', tool_calls: '本轮工具调用次数已达上限', input_tokens: '本轮输入预算不足', output_tokens: '本轮输出预算已用尽', context_window: '当前对话超过模型上下文上限' };
    if (dimension && labels[dimension]) return labels[dimension];
    return ({ agent_model_error: '模型请求失败', agent_model_limit: '运行达到模型或预算限制', agent_run_blocked: '运行被中止，请检查模型配置与运行状态' } as Record<string, string>)[view.failure.code] ?? '回答未完成';
  }
  if (!active) return ({ completed: '回答完成', cancelled: '已停止', failed: '回答未完成', interrupted: '运行已中断' } as Record<string, string>)[view?.status ?? ''] ?? '';
  const status = ({ accepted: '已接收，准备思考', queued: '等待运行', waiting_approval: '等待你的审批', recovering: '等待恢复核对', cancelling: '正在停止' } as Record<string, string>)[view?.status ?? ''];
  if (status) return status;
  if (view?.tools.some(tool => tool.status === 'started' || tool.status === 'proposed')) return '正在执行工具';
  if (view?.messages.some(message => message.streaming)) return '正在生成回答';
  return '正在思考';
}
