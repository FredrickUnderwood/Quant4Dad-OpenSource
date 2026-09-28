import type { AgentSession, AgentModel, Run, Submission, Transcript, AgentEvent, AgentApproval, AgentToolResult, AgentRuntimeStatus } from './types.ts';
import { consumeSSE } from './events.ts';

export class AgentAPIError extends Error {
  readonly status: number; readonly code: string; readonly runID: string;
  constructor(status: number, code: string, runID = '') { super(errorMessage(status, code)); this.status = status; this.code = code; this.runID = runID; }
}
function errorMessage(status: number, code: string) {
  const messages: Record<string, string> = {
    agent_tool_not_found: '未找到这次工具调用的记录。',
    agent_request_conflict: '这条请求的内容与原消息不同，请保留原消息重试。',
    agent_run_in_progress: '当前会话仍在生成，请等待结束后重试这条消息。',
    agent_model_not_ready: '模型尚未就绪，请先检查模型连接。',
    agent_session_not_active: '请先恢复或完成会话创建。',
    agent_capability_expired: '本次请求已过期，请检查会话历史后重新发送。',
    agent_capability_rejected: '本次请求的执行权限已失效，请检查模型配置。',
    agent_event_cursor_expired: '部分实时记录已过期，正在读取会话历史。',
    agent_run_delivery_unconfirmed: '消息是否接收尚未确认，请重试原消息。',
    agent_approval_delivery_unconfirmed: '决定是否送达尚未确认，可以重试相同决定。',
    agent_approval_rejected: '审批已过期、状态已变化或权限已失效，请检查当前状态。',
    agent_model_revision_stale: '模型配置已被其他操作更新，请重新加载后保存。',
  };
  return messages[code] ?? (status === 412 ? '模型配置已被其他操作更新，请重新加载后保存。' : status === 404 ? '助手尚未启用，或记录不存在。' : status === 401 ? '登录已过期，请重新登录。' : '暂时无法连接助手，请稍后重试。');
}
function login() {
  if (typeof window !== 'undefined' && window.location.pathname !== '/login') window.location.replace('/login?from=' + encodeURIComponent(window.location.pathname + window.location.search));
}
async function check(response: Response) {
  if (response.ok) return;
  if (response.status === 401) login();
  let code = '', runID = '';
  try {
    const body = await response.json();
    if (typeof body.message === 'string' && /^(agent|model)_[a-z_]+$/.test(body.message)) code = body.message;
    if (typeof body.run_id === 'string' && /^[0-7][0-9A-HJKMNP-TV-Z]{25}$/.test(body.run_id)) runID = body.run_id;
  } catch { /* Never display an arbitrary upstream error page. */ }
  throw new AgentAPIError(response.status, code, runID);
}
async function request<T>(path: string, body?: unknown, method = body === undefined ? 'GET' : 'POST', signal?: AbortSignal, headers = {}): Promise<T> {
  const response = await fetch('/api/v1/agent/' + path, { method, signal, credentials: 'same-origin', redirect: 'error', cache: 'no-store',
    headers: { 'Content-Type': 'application/json', ...headers }, body: body === undefined ? undefined : JSON.stringify(body) });
  await check(response); return response.json() as Promise<T>;
}
export const agentAPI = {
  async configureModel(provider: string, revision: string, patch: { agent?: Partial<{ enabled: boolean; protocol: string; context_window: number; max_output_tokens: number; reasoning_effort: string }>; api_key_update: { action: 'keep' | 'replace' | 'revoke'; value?: string } }) {
    const response = await fetch('/api/v1/settings/llm-providers/' + encodeURIComponent(provider), { method: 'PATCH', credentials: 'same-origin', redirect: 'error', cache: 'no-store', headers: { 'Content-Type': 'application/json', 'If-Match': '"' + revision + '"' }, body: JSON.stringify(patch) });
    await check(response);
  },
  options: (signal?: AbortSignal) => request<{ profiles: string[]; text_only: boolean }>('options', undefined, 'GET', signal),
  status: (signal?: AbortSignal) => request<AgentRuntimeStatus>('status', undefined, 'GET', signal),
  models: (signal?: AbortSignal) => request<{ models: AgentModel[] }>('models', undefined, 'GET', signal),
  probe: (provider: string) => request<{ status: string }>(`models/${encodeURIComponent(provider)}/probe`, { force: true }),
  sessions: (cursor = '', signal?: AbortSignal) => request<{ items: AgentSession[]; next_cursor: string | null }>('sessions?limit=30' + (cursor ? '&cursor=' + encodeURIComponent(cursor) : ''), undefined, 'GET', signal),
  create: (key: string, input: { provider: string; model: string; profile: string; title: string }) => request<AgentSession>('sessions', input, 'POST', undefined, { 'Idempotency-Key': key }),
  detail: (id: string, before = '', snapshot = '', signal?: AbortSignal) => request<{ session: AgentSession; transcript: Transcript | null }>(`sessions/${encodeURIComponent(id)}` + (before ? `?before_seq=${before}&snapshot_seq=${snapshot}` : ''), undefined, 'GET', signal),
  metadata: (id: string, signal?: AbortSignal) => request<AgentSession>(`sessions/${encodeURIComponent(id)}/metadata`, undefined, 'GET', signal),
  patch: (id: string, input: { title?: string; archived?: boolean }) => request<AgentSession>(`sessions/${encodeURIComponent(id)}`, input, 'PATCH'),
  reconcile: (id: string) => request<AgentSession>(`sessions/${encodeURIComponent(id)}/reconcile`, {}),
  send: (id: string, clientID: string, text: string) => request<Submission>(`sessions/${encodeURIComponent(id)}/messages`, { client_request_id: clientID, content: [{ type: 'text', text }] }),
  run: (id: string, signal?: AbortSignal) => request<Run>(`runs/${encodeURIComponent(id)}`, undefined, 'GET', signal),
  cancel: (id: string) => request<Run>(`runs/${encodeURIComponent(id)}/cancel`, {}),
  reconcileRun: (id: string) => request<Run>(`runs/${encodeURIComponent(id)}/reconcile`, {}),
  approval: (id: string, signal?: AbortSignal) => request<AgentApproval>(`approvals/${encodeURIComponent(id)}`, undefined, 'GET', signal),
  toolResult: (run: string, call: string, signal?: AbortSignal) => request<AgentToolResult>(`runs/${encodeURIComponent(run)}/tools/${encodeURIComponent(call)}`, undefined, 'GET', signal),
  decide: (id: string, decision: 'allow_once' | 'reject') => request<{ approval: AgentApproval; delivered: boolean }>(`approvals/${encodeURIComponent(id)}/decision`, { decision }),
  async events(session: string, run: string, cursor: string, accept: (event: AgentEvent) => void, signal: AbortSignal) {
    const response = await fetch(`/api/v1/agent/runs/${encodeURIComponent(run)}/events`, { signal, credentials: 'same-origin', redirect: 'error', cache: 'no-store', headers: { Accept: 'text/event-stream', 'Last-Event-ID': cursor } });
    await check(response);
    if (!response.body || !/^text\/event-stream(?:;|$)/i.test(response.headers.get('content-type') ?? '')) throw new AgentAPIError(503, '');
    return consumeSSE(response.body, session, run, cursor, accept, signal);
  },
};
