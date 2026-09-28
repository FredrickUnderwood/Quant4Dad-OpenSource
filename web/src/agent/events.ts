import type { AgentEvent, RunView } from './types.ts';

export const validCursor = (value: unknown): value is string => typeof value === 'string' && /^(0|[1-9][0-9]{0,1023})(?![\s\S])/.test(value);
export const terminalType = (value: string) => /^run\.(completed|failed|cancelled|interrupted)$/.test(value);
const types = new Set(['run.started', 'run.progress', 'message.delta', 'message.completed', 'tool.proposed', 'approval.required', 'tool.started', 'tool.completed', 'tool.failed', 'usage.updated', 'context.compacted', 'run.completed', 'run.failed', 'run.cancelled', 'run.interrupted', 'heartbeat']);
const progressNotices: Record<string, string> = {
  compacting: '正在整理较早的对话，为后续分析腾出空间…',
  compaction_failed: '本次对话整理未完成，正在检查能否继续…',
  context_recovery: '对话已整理，正在继续分析…',
  output_recovery: '本次生成被截断，正在调整后重试…',
  finalizing: '正在整理已有结果和未完成事项…',
};
export class EventProtocolError extends Error { constructor() { super('事件连接异常，请重新连接。'); } }

/** One bounded connection. Cursor advances only after the consumer accepts a
 * complete validated frame. EOF is not a successful Run outcome. */
export async function consumeSSE(body: ReadableStream<Uint8Array>, session: string, run: string, cursor: string,
  accept: (event: AgentEvent) => void, signal: AbortSignal): Promise<string> {
  if (!validCursor(cursor)) throw new EventProtocolError();
  const reader = body.getReader(), decoder = new TextDecoder('utf-8', { fatal: true });
  let text = '', size = 0, id = '', type = '', data = '', done = false;
  const abort = () => { void reader.cancel().catch(() => {}); };
  signal.addEventListener('abort', abort, { once: true });
  try {
    while (!done) {
      signal.throwIfAborted();
      const chunk = await reader.read();
      signal.throwIfAborted();
      text += decoder.decode(chunk.value, { stream: !chunk.done });
      let end: number;
      while ((end = text.indexOf('\n')) >= 0) {
        const raw = text.slice(0, end); text = text.slice(end + 1);
        const line = raw.endsWith('\r') ? raw.slice(0, -1) : raw;
        size += new TextEncoder().encode(raw).length + 1;
        if (size > 1024 * 1024) throw new EventProtocolError();
        if (line === '') {
          if (data) {
            let event: AgentEvent;
            try { event = JSON.parse(data); } catch { throw new EventProtocolError(); }
            if (!validCursor(id) || id === '0' || !types.has(type) || !event || event.id !== id || event.type !== type ||
                event.run_id !== run || event.session_id !== session || event.schema_version !== 1 || !event.data ||
                typeof event.data !== 'object' || Array.isArray(event.data)) throw new EventProtocolError();
            if (type.startsWith('message.') && (typeof event.data.message_id !== 'string' || typeof event.data.text !== 'string')) throw new EventProtocolError();
            if (BigInt(id) > BigInt(cursor)) {
              if (BigInt(id) !== BigInt(cursor) + 1n) throw new EventProtocolError();
              accept(event); cursor = id;
              if (terminalType(type)) { done = true; break; }
            }
          } else if (id || type) throw new EventProtocolError();
          size = 0; id = ''; type = ''; data = '';
        } else if (!line.startsWith(':')) {
          const split = line.indexOf(':');
          const field = split < 0 ? line : line.slice(0, split);
          let value = split < 0 ? '' : line.slice(split + 1); if (value.startsWith(' ')) value = value.slice(1);
          if (field === 'id') { if (id || !validCursor(value)) throw new EventProtocolError(); id = value; }
          if (field === 'event') { if (type) throw new EventProtocolError(); type = value; }
          if (field === 'data') data += value + '\n';
        }
      }
      if (text.length > 1024 * 1024) throw new EventProtocolError();
      if (chunk.done) { if (text || data || id || type) throw new EventProtocolError(); break; }
    }
    return cursor;
  } finally {
    signal.removeEventListener('abort', abort);
    await reader.cancel().catch(() => {}); reader.releaseLock();
  }
}

export function initialRun(sessionID: string, runID: string): RunView {
  return { sessionID, runID, cursor: '0', status: 'running', terminal: false, messages: [], tools: [], order: [], notice: '' };
}
export function applyEvent(state: RunView, event: AgentEvent): RunView {
  if (event.run_id !== state.runID || event.session_id !== state.sessionID || !validCursor(event.id)) throw new EventProtocolError();
  if (BigInt(event.id) <= BigInt(state.cursor)) return state;
  if (BigInt(event.id) !== BigInt(state.cursor) + 1n || state.terminal) throw new EventProtocolError();
  const next = { ...state, cursor: event.id };
  if (Object.values(progressNotices).includes(state.notice) &&
      (event.type.startsWith('message.') || event.type === 'tool.started' || terminalType(event.type))) next.notice = '';
  if (event.type.startsWith('tool.')) {
    const id = event.data.tool_call_id;
    if (typeof id !== 'string' || !/^[0-7][0-9A-HJKMNP-TV-Z]{25}$/.test(id)) throw new EventProtocolError();
    const previous = state.tools.find(tool => tool.id === id);
    if (event.type === 'tool.proposed') {
      if (previous || typeof event.data.name !== 'string' || event.data.name.length > 256) throw new EventProtocolError();
      const raw = event.data.arguments && typeof event.data.arguments === 'object' && !Array.isArray(event.data.arguments) ? JSON.stringify(event.data.arguments, null, 2) : undefined;
      next.tools = [...state.tools, { id, name: event.data.name, status: 'proposed' as const, arguments: raw?.slice(0, 131072), argumentsOmitted: event.data.arguments_omitted === true || raw === undefined || raw.length > 131072 }].slice(-64);
      next.order = [...state.order, { kind: 'tool' as const, id }];
    } else {
      const status = event.type.slice(5);
      if (!['started', 'completed', 'failed'].includes(status)) throw new EventProtocolError();
      // Older cards may have been evicted from the bounded display. Their
      // events still advance the replay cursor without recreating a call.
      if (previous) {
        if (['completed', 'failed'].includes(previous.status) || (status === 'completed' && previous.status !== 'started')) throw new EventProtocolError();
        next.tools = state.tools.map(tool => tool.id === id ? { ...tool, status: status as 'started' | 'completed' | 'failed', code: typeof event.data.code === 'string' ? event.data.code : undefined } : tool);
        if (state.status === 'waiting_approval') next.status = 'running';
      }
    }
  }
  if (event.type === 'approval.required') {
    const d = event.data, previous = state.tools.find(tool => tool.id === d.tool_call_id);
    if (typeof d.approval_id !== 'string' || !/^[0-9a-f]{64}$/.test(d.approval_id) || typeof d.arguments_hash !== 'string' || !/^sha256:[0-9a-f]{64}$/.test(d.arguments_hash) || !['R2', 'R3'].includes(String(d.risk)) || typeof d.expires_at !== 'string' || !Number.isFinite(Date.parse(d.expires_at))) throw new EventProtocolError();
    if (previous) {
      if (previous.status !== 'proposed' || d.name !== previous.name) throw new EventProtocolError();
      next.tools = state.tools.map(tool => tool.id === d.tool_call_id ? { ...tool, status: 'waiting_approval', approval: { id: String(d.approval_id), hash: String(d.arguments_hash), risk: String(d.risk), expires: String(d.expires_at) } } : tool);
    }
    next.status = 'waiting_approval';
  }
  if (event.type.startsWith('message.')) {
    if (typeof event.data.message_id !== 'string' || typeof event.data.text !== 'string') throw new EventProtocolError();
    const previous = state.messages.find(message => message.id === event.data.message_id);
    const text = event.type === 'message.completed' ? event.data.text : (previous?.text ?? '') + event.data.text;
    const message = { id: event.data.message_id, text: text.slice(0, 262144), omitted: text.length > 262144 || !!previous?.omitted, streaming: event.type === 'message.delta' };
    next.messages = [...state.messages.filter(value => value.id !== message.id), message].slice(-64);
    if (!previous) next.order = [...state.order, { kind: 'message' as const, id: message.id }];
  }
  if (terminalType(event.type)) {
    next.status = event.type.slice(4); next.terminal = true;
    if (event.type === 'run.failed') {
      const budget = event.data.budget as NonNullable<RunView['failure']>['budget'];
      if (typeof event.data.code !== 'string' || (budget && (typeof budget.dimension !== 'string' || [budget.used, budget.limit, budget.requested].some(n => !Number.isSafeInteger(n) || n < 0)))) throw new EventProtocolError();
      next.failure = { code: event.data.code, ...(budget ? { budget } : {}) };
    }
  }
  if (event.type === 'context.compacted') next.notice = '较早的对话已整理，完整记录可在历史中查看。';
  if (event.type === 'run.progress') {
    if (typeof event.data.stage !== 'string' || !Object.prototype.hasOwnProperty.call(progressNotices, event.data.stage)) throw new EventProtocolError();
    next.notice = progressNotices[event.data.stage];
  }
  const messages = new Set(next.messages.map(message => message.id)), tools = new Set(next.tools.map(tool => tool.id));
  next.order = next.order.filter(entry => (entry.kind === 'message' ? messages : tools).has(entry.id));
  return next;
}
