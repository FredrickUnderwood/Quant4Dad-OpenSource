import { AgentAPIError, agentAPI } from './api.ts';
import { applyEvent, initialRun } from './events.ts';
import type { RunView } from './types.ts';

function pause(ms: number, signal: AbortSignal) {
  return new Promise<void>((resolve, reject) => {
    signal.throwIfAborted();
    const done = () => { clearTimeout(timer); signal.removeEventListener('abort', abort); resolve(); };
    const abort = () => { clearTimeout(timer); signal.removeEventListener('abort', abort); reject(signal.reason); };
    const timer = setTimeout(done, ms); signal.addEventListener('abort', abort, { once: true });
  });
}
/** Reconnect reads only. A disconnected/expired stream never resubmits a prompt
 * or cancels a Run. After 410, reload history and poll instead of resetting IDs. */
export async function watchRun(session: string, run: string, signal: AbortSignal, update: (view: RunView) => void,
  reload: () => Promise<void>, api: Pick<typeof agentAPI, 'events' | 'run'> = agentAPI, wait = pause) {
  let state = initialRun(session, run), failures = 0, historyOnly = false;
  update(state);
  while (!signal.aborted) {
    if (!historyOnly) {
      try {
        await api.events(session, run, state.cursor, event => { state = applyEvent(state, event); failures = 0; update(state); }, signal);
        if (state.terminal) { await reload(); state = { ...state, messages: state.status === 'completed' ? [] : state.messages.filter(message => message.streaming) }; update(state); return; }
      } catch (error) {
        signal.throwIfAborted();
        if (error instanceof AgentAPIError && [401, 403, 404].includes(error.status)) throw error;
        if (error instanceof AgentAPIError && error.status === 410) {
          historyOnly = true; state = { ...state, messages: [], notice: error.message }; update(state); await reload();
        }
      }
    }
    signal.throwIfAborted();
    try {
      const projection = await api.run(run, signal);
      if (projection.session_id !== session || projection.run_id !== run || !projection.durable) throw new Error('无法核对本次回答，请刷新会话。');
      state = { ...state, status: projection.state, terminal: projection.terminal,
        tools: projection.terminal ? state.tools.map(tool => ['proposed', 'waiting_approval', 'started'].includes(tool.status) ? { ...tool, status: 'unknown' as const } : tool) : state.tools }; update(state);
      if (projection.terminal) { await reload(); state = { ...state, messages: state.status === 'completed' ? [] : state.messages.filter(message => message.streaming) }; update(state); return; }
      if (historyOnly) await reload();
      if (historyOnly) failures = 0;
    } catch (error) {
      signal.throwIfAborted();
      if (error instanceof AgentAPIError && [401, 403, 404].includes(error.status)) throw error;
    }
    if (++failures > 8) throw new Error('实时连接暂时中断，可重新连接查看进度。');
    await wait(historyOnly ? 2000 : Math.min(500 * 2 ** (failures - 1), 4000), signal);
  }
}
