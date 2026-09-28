import { useEffect, useLayoutEffect, useRef, useState } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { AgentAPIError, agentAPI } from '../agent/api';
import { watchRun } from '../agent/watch';
import { ToolCard, toolLabels } from '../agent/ToolCard';
import { ResearchKlineOverview } from '../agent/ResearchKlineOverview';
import { ResearchPythonResults } from '../agent/ResearchPythonResults';
import { RuntimeStatus } from '../agent/RuntimeStatus';
import { Markdown } from '../agent/Markdown';
import { RunActivity } from '../agent/RunActivity';
import { conversationRows, runActivity } from '../agent/conversation';
import { mergeSession } from '../agent/session';
import type { AgentModel, AgentSession, RunView, Transcript } from '../agent/types';
import './AssistantPage.css';

const isID = (id: string | null): id is string => !!id && /^[0-7][0-9A-HJKMNP-TV-Z]{25}$/.test(id);
const profileLabels: Record<string, string> = { text_only: '文本助手', research: '行情研究', strategy_lab: '策略实验室', pipeline_builder: '流水线助手' };
type Pending = { id: string; text: string; runID?: string };
function remember(session: string, run: string) {
  try { sessionStorage.setItem('q4d-agent-run-' + session, run); } catch { /* URLs still support refresh. */ }
}
function remembered(session: string) {
  try { const id = sessionStorage.getItem('q4d-agent-run-' + session); return isID(id) ? id : ''; } catch { return ''; }
}

export default function AssistantPage() {
  const [params, setParams] = useSearchParams();
  const sessionID = isID(params.get('session')) ? params.get('session')! : '';
  const runID = isID(params.get('run')) ? params.get('run')! : '';
  const [enabled, setEnabled] = useState<boolean | null>(null);
  const [profiles, setProfiles] = useState<string[]>([]), [models, setModels] = useState<AgentModel[]>([]);
  const [items, setItems] = useState<AgentSession[]>([]), [next, setNext] = useState<string | null>(null);
  const [session, setSession] = useState<AgentSession | null>(null), [history, setHistory] = useState<Transcript | null>(null);
  const [view, setView] = useState<RunView | null>(null), [error, setError] = useState('');
  const [draft, setDraft] = useState(''), [pending, setPending] = useState<Pending | null>(null);
  const [posting, setPosting] = useState(false), [busy, setBusy] = useState(false), [loading, setLoading] = useState(false);
  const [creating, setCreating] = useState(false), [provider, setProvider] = useState(''), [profile, setProfile] = useState('');
  const [title, setTitle] = useState(''), [editTitle, setEditTitle] = useState(false), [showArchived, setShowArchived] = useState(false);
  const [connection, setConnection] = useState(''), [retry, setRetry] = useState(0);
  const [stopping, setStopping] = useState(''), [showLatest, setShowLatest] = useState(false);
  const submitting = useRef(false), cancelling = useRef(false), composing = useRef(false);
  const textarea = useRef<HTMLTextAreaElement>(null);
  const [copied, setCopied] = useState('');
  const pendingBySession = useRef(new Map<string, Pending>()), current = useRef(sessionID), alive = useRef(true);
  const creation = useRef<{ key: string; input: { provider: string; model: string; profile: string; title: string } } | null>(null);
  const messages = useRef<HTMLDivElement>(null), nearBottom = useRef(true);
  const thread = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const content = thread.current, container = messages.current;
    if (!content || !container || typeof ResizeObserver === 'undefined') return;
    // Result cards finish loading after the transcript. Keep users who are
    // following the answer at its end, without moving readers of older text.
    const observer = new ResizeObserver(() => {
      if (nearBottom.current) container.scrollTop = container.scrollHeight;
    });
    observer.observe(content);
    return () => observer.disconnect();
  }, [enabled, sessionID, creating]);
  current.current = sessionID;
  useEffect(() => { alive.current = true; return () => { alive.current = false; }; }, []);

  function applySession(value: AgentSession) {
    if (!alive.current) return;
    if (current.current === value.session_id) setSession(old => mergeSession(old, value));
    setItems(old => old.map(item => item.session_id === value.session_id ? mergeSession(item, value) : item));
  }
  async function list(cursor = '') {
    const page = await agentAPI.sessions(cursor);
    if (!alive.current) return;
    setItems(old => cursor ? [...old, ...page.items.filter(item => !old.some(value => value.session_id === item.session_id))] : page.items.map(item => mergeSession(old.find(value => value.session_id === item.session_id) ?? null, item)));
    setNext(page.next_cursor);
  }
  useEffect(() => {
    const abort = new AbortController();
    void Promise.all([agentAPI.options(abort.signal), agentAPI.models(abort.signal), agentAPI.sessions('', abort.signal)])
      .then(([options, catalog, page]) => {
        if (abort.signal.aborted) return;
        setEnabled(true); setProfiles(options.profiles); setProfile(options.profiles[0] ?? '');
        setModels(catalog.models); setProvider(catalog.models.find(model => model.status === 'ready')?.provider ?? catalog.models[0]?.provider ?? '');
        setItems(page.items); setNext(page.next_cursor);
      }).catch(e => { if (!abort.signal.aborted) { setEnabled(e instanceof AgentAPIError && e.status === 404 ? false : true); setError(e.message); } });
    return () => abort.abort();
  }, []);

  useEffect(() => {
    const abort = new AbortController();
    setSession(null); setHistory(null); setView(null); setError(''); setConnection(''); setEditTitle(false);
    const prior = pendingBySession.current.get(sessionID) ?? null; setPending(prior); setDraft(prior?.text ?? '');
    nearBottom.current = true; setShowLatest(false);
    if (!sessionID) return () => abort.abort();
    setLoading(true);
    void agentAPI.detail(sessionID, '', '', abort.signal).then(detail => {
      if (abort.signal.aborted) return;
      applySession(detail.session); setHistory(detail.transcript); setTitle(detail.session.title);
      // Recover only an existing Run; navigation/refresh never submits a message.
      const last = detail.transcript?.items.filter(item => item.role === 'user' && isID(item.run_id)).slice(-1)[0]?.run_id;
      const restore = runID || remembered(sessionID) || last;
      if (restore && !runID) setParams({ session: sessionID, run: restore }, { replace: true });
    }).catch(e => { if (!abort.signal.aborted) setError(e.message); }).finally(() => { if (!abort.signal.aborted) setLoading(false); });
    return () => abort.abort();
  }, [sessionID]);

  useEffect(() => {
    if (!runID || !sessionID || session?.session_id !== sessionID) return;
    const abort = new AbortController(); setConnection('连接中');
    void watchRun(sessionID, runID, abort.signal, value => {
      if (abort.signal.aborted) return;
      setView(value); setConnection(value.terminal ? '' : '已连接');
      if (value.terminal && pendingBySession.current.get(sessionID)?.runID === value.runID) {
        pendingBySession.current.delete(sessionID); setPending(null); setDraft(''); setError('');
      }
    }, async () => {
      const detail = await agentAPI.detail(sessionID, '', '', abort.signal);
      if (!abort.signal.aborted) { setHistory(detail.transcript); applySession(detail.session); }
    }).catch(e => { if (!abort.signal.aborted) { setConnection('连接已断开'); setError(e.message); } });
    return () => abort.abort();
  }, [sessionID, runID, session?.session_id, retry]);

  const pendingTitles = [...new Set([...items.filter(item => item.title_pending).map(item => item.session_id),
    ...(session?.title_pending ? [session.session_id] : [])])].sort().join(',');
  useEffect(() => {
    if (!pendingTitles) return;
    const abort = new AbortController();
    let timer: ReturnType<typeof setTimeout>;
    async function refresh() {
      await Promise.all(pendingTitles.split(',').map(async id => {
        try {
          const value = await agentAPI.metadata(id, abort.signal);
          if (!abort.signal.aborted) applySession(value);
        } catch { /* Metadata failure must not interrupt the conversation. */ }
      }));
      if (!abort.signal.aborted) timer = setTimeout(() => void refresh(), 1500);
    }
    timer = setTimeout(() => void refresh(), 500);
    return () => { abort.abort(); clearTimeout(timer); };
  }, [pendingTitles]);

  useEffect(() => {
    if (nearBottom.current && messages.current) messages.current.scrollTop = messages.current.scrollHeight;
  }, [view, history, posting]);
  useLayoutEffect(() => {
    const el = textarea.current;
    if (el) { el.style.height = 'auto'; el.style.height = Math.min(el.scrollHeight, 200) + 'px'; }
  }, [draft, sessionID, creating, loading]);
  useEffect(() => {
    if (!posting && !loading && !creating && session?.status === 'active') textarea.current?.focus();
  }, [posting, loading, creating, session?.session_id]);

  async function operation(fn: () => Promise<void>) {
    setBusy(true); setError('');
    try { await fn(); } catch (e) { if (alive.current) setError(e instanceof Error ? e.message : '操作未完成，请重试。'); }
    finally { if (alive.current) setBusy(false); }
  }
  function select(id: string) {
    setCreating(false); const run = remembered(id);
    setParams(run ? { session: id, run } : { session: id });
  }
  async function create() {
    await operation(async () => {
      const model = models.find(value => value.provider === provider);
      if (!model || !profile) return;
      creation.current ??= { key: crypto.randomUUID(), input: { provider, model: model.model, profile, title: '' } };
      const value = await agentAPI.create(creation.current.key, creation.current.input);
      if (!alive.current) return;
      creation.current = null; await list(); select(value.session_id);
    });
  }
  async function send() {
    if (!session || session.session_id !== sessionID || session.status !== 'active' || submitting.current || posting || loading || (!pending && (active || !draft.trim() || [...draft].length > 32000))) return;
    submitting.current = true; nearBottom.current = true; setShowLatest(false);
    const selected = session.session_id;
    const message = pendingBySession.current.get(selected) ?? { id: crypto.randomUUID(), text: draft };
    pendingBySession.current.set(selected, message); setPending(message); setPosting(true); setError('');
    try {
      const result = await agentAPI.send(selected, message.id, message.text);
      remember(selected, result.run_id); pendingBySession.current.delete(selected);
      // Refresh metadata even if the user has switched sessions meanwhile.
      // A failed refresh cannot turn an accepted message into a send failure.
      void agentAPI.metadata(selected).then(applySession).catch(() => {});
      if (!alive.current || current.current !== selected) return;
      setPending(null); setDraft(''); setParams({ session: selected, run: result.run_id });
      void agentAPI.detail(selected).then(detail => {
        if (alive.current && current.current === selected) { setHistory(detail.transcript); applySession(detail.session); }
      }).catch(() => {});
    } catch (e) {
      if (!alive.current || current.current !== selected) return;
      const rejected = e instanceof AgentAPIError && [400, 403, 422].includes(e.status);
      if (rejected) {
        // A definitive admission rejection can be edited and submitted with a
        // new identity. Ambiguous delivery keeps the original body and key.
        pendingBySession.current.delete(selected); setPending(null);
        if (runID && runID === message.runID) {
          try { sessionStorage.removeItem('q4d-agent-run-' + selected); } catch { /* optional storage */ }
          setView(null); setParams({ session: selected });
        }
      }
      if (e instanceof AgentAPIError && e.runID && !rejected) {
        message.runID = e.runID;
        if (e.code === 'agent_run_in_progress') {
          const detail = await agentAPI.detail(selected).catch(() => null);
          const existing = detail?.transcript?.items.filter(item => item.role === 'user' && isID(item.run_id)).slice(-1)[0]?.run_id;
          if (existing && alive.current && current.current === selected) { remember(selected, existing); setParams({ session: selected, run: existing }); }
        } else { remember(selected, e.runID); setParams({ session: selected, run: e.runID }); }
      }
      if (alive.current && current.current === selected) setError(e instanceof Error ? e.message : '发送结果尚未确认，请重试原消息。');
    } finally { submitting.current = false; if (alive.current) setPosting(false); }
  }
  async function stop() {
    if (!runID || !active || cancelling.current) return;
    const selected = sessionID;
    cancelling.current = true; setStopping(runID);
    try {
      await operation(async () => {
        const result = await agentAPI.cancel(runID);
        if (alive.current && current.current === selected) {
          setView(old => old?.runID === runID ? { ...old, terminal: result.terminal, status: result.state } : old);
          setRetry(value => value + 1);
        }
      });
    } finally { cancelling.current = false; if (alive.current) setStopping(''); }
  }
  async function copyMessage(id: string, text: string) {
    try { await navigator.clipboard.writeText(text); setCopied(id); }
    catch { setError('复制未完成，请选中回答手动复制。'); }
  }
  async function older() {
    if (!history?.next_before_seq) return;
    const selected = sessionID, before = history.next_before_seq, snapshot = history.snapshot_seq;
    await operation(async () => {
      const detail = await agentAPI.detail(selected, before, snapshot);
      if (current.current !== selected || !detail.transcript) return;
      const page = detail.transcript;
      setHistory(previous => previous ? { ...page, items: [...page.items, ...previous.items.filter(item => !page.items.some(v => v.message_id === item.message_id))] } : page);
    });
  }

  const currentView = view?.runID === runID && view.sessionID === sessionID ? view : null;
  const active = !!runID && !currentView?.terminal;
  const rows = conversationRows(history, currentView);
  const analyses = currentView?.tools.filter(tool => tool.name.replace(/^mcp__q4d__/, '') === 'analyze_kline' && tool.status === 'completed') ?? [];
  const analysisIDs = new Set(analyses.map(tool => tool.id));
  const pythonTools = currentView?.tools.filter(tool => tool.name.replace(/^mcp__q4d__/, '') === 'execute_python' && tool.status === 'completed') ?? [];
  const pythonIDs = new Set(pythonTools.map(tool => tool.id));
  const overviewAt = rows.find(row => row.kind === 'tool' && analysisIDs.has(row.id))?.id;
  const disconnected = connection === '连接已断开';
  const status = stopping === runID && stopping ? '正在停止' : runActivity(currentView, posting, active, disconnected);
  const animating = (posting || active) && !disconnected && !['waiting_approval', 'recovering'].includes(currentView?.status ?? '');
  const tooLong = [...draft].length > 32000;

  if (enabled === null) return <div className="empty">正在连接助手…</div>;
  if (!enabled) return <section className="empty"><h2>助手尚未启用</h2><p>配置完成后，可在这里开始对话。</p><Link to="/settings">打开设置</Link></section>;

  return <section className="assistant">
    <aside className="assistant-sidebar" aria-label="会话列表">
      <div className="assistant-sidebar-head"><h2><span className="assistant-brand" aria-hidden="true">✳</span> 助手</h2><button className="secondary" onClick={() => { setCreating(true); setError(''); }} disabled={posting}>新对话</button></div>
      <label className="assistant-archive-filter"><input type="checkbox" checked={showArchived} onChange={e => setShowArchived(e.target.checked)} />显示归档</label>
      <div className="assistant-session-list">
        {items.filter(item => showArchived || item.status !== 'archived').map(item => <button key={item.session_id} className={`assistant-session ${sessionID === item.session_id ? 'selected' : ''}`} aria-current={sessionID === item.session_id ? 'page' : undefined} disabled={posting} onClick={() => select(item.session_id)}>
          <strong>{item.title || '新对话'}</strong><span>{item.model}{item.status === 'archived' ? ' · 已归档' : item.status !== 'active' ? ' · 待恢复' : ''}</span>
        </button>)}
        {!items.length && <p className="muted">还没有对话</p>}
        {next && <button className="ghost" disabled={busy} onClick={() => operation(() => list(next))}>更多会话</button>}
      </div>
      <div className="assistant-sidebar-foot"><span className="assistant-brand" aria-hidden="true">✳</span><span>Quant4Dad<small>你的投研工作伙伴</small></span></div>
    </aside>
    <div className="assistant-main">
      {error && <div className="error assistant-error" role="alert">{error}</div>}
      {creating || !sessionID ? <div className="assistant-welcome">
        <span className="assistant-kicker">QUANT4DAD · ASSISTANT</span><h2>从一次对话开始</h2><p className="muted">选择已配置的模型，继续整理你的想法。</p>
        <div className="assistant-create">
          <label htmlFor="assistant-model">模型</label><select id="assistant-model" value={provider} disabled={busy || !!creation.current} onChange={e => setProvider(e.target.value)}>
            {models.map(model => <option key={model.provider} value={model.provider}>{model.model} · {model.provider}{model.status === 'ready' ? ' · 已就绪' : ' · 待检查'}</option>)}
          </select>
          <label htmlFor="assistant-profile">对话配置</label><select id="assistant-profile" value={profile} disabled={busy || !!creation.current} onChange={e => setProfile(e.target.value)}>{profiles.map(id => <option key={id} value={id}>{id === 'text_only' ? '文本助手' : id === 'research' ? '行情研究' : id === 'strategy_lab' ? '策略实验室' : id === 'pipeline_builder' ? '流水线助手' : id}</option>)}</select>
          <RuntimeStatus profile={profile} />
          <div className="assistant-actions"><button className="secondary" disabled={busy || !provider} onClick={() => operation(async () => { const result = await agentAPI.probe(provider); setModels((await agentAPI.models()).models); if (result.status !== 'ready') throw new Error('模型连接未通过，请检查设置后重试。'); })}>{busy ? '请稍候…' : '检查模型连接'}</button>
            <button disabled={busy || !profiles.length || !models.length || (!creation.current && models.find(model => model.provider === provider)?.status !== 'ready')} onClick={create}>{creation.current ? '重试创建' : '开始对话'}</button></div>
          {!models.length && <Link to="/settings">先添加一个模型</Link>}
        </div>
      </div> : <>
        <div className="assistant-conversation-head">
          <div>{editTitle ? <form onSubmit={e => { e.preventDefault(); operation(async () => { const value = await agentAPI.patch(sessionID, { title }); if (current.current === value.session_id) { setSession(value); setEditTitle(false); } await list(); }); }}><input aria-label="会话标题" value={title} maxLength={256} onChange={e => setTitle(e.target.value)} /><button disabled={busy}>保存</button><button type="button" className="ghost" onClick={() => setEditTitle(false)}>取消</button></form> : <h2>{session?.title || '新对话'}</h2>}<span className="muted">{session ? `${session.model} · ${profileLabels[session.profile] ?? session.profile}` : '正在读取会话…'}</span></div>
          <div className="assistant-actions"><button className="ghost" disabled={!session || busy} onClick={() => { setTitle(session?.title ?? ''); setEditTitle(true); }}>改名</button><button className="ghost" disabled={!session || busy || !['active', 'archived'].includes(session.status)} onClick={() => operation(async () => { const value = await agentAPI.patch(sessionID, { archived: session?.status !== 'archived' }); if (current.current === value.session_id) setSession(value); await list(); })}>{session?.status === 'archived' ? '恢复会话' : '归档'}</button></div>
        </div>
        <div ref={messages} className="assistant-messages" role="log" aria-label="对话记录" aria-live="polite" onScroll={() => { const el = messages.current!; nearBottom.current = el.scrollHeight - el.scrollTop - el.clientHeight < 100; setShowLatest(!nearBottom.current); }}>
          <div ref={thread} className="assistant-thread">
          {loading && <p className="muted">正在读取会话…</p>}
          {history?.has_more && <button className="ghost" disabled={busy} onClick={older}>加载更早的消息</button>}
          {!loading && session && !rows.length && !posting && <div className="assistant-empty"><span className="assistant-empty-mark" aria-hidden="true">✳</span><h3>今天想研究什么？</h3><p>从一个问题开始，一起把想法理清楚。</p><div className="assistant-suggestions">{(session.profile === 'research' ? ['看看本地行情的数据覆盖情况', '整理最近值得关注的市场资讯'] : session.profile === 'strategy_lab' ? ['有哪些策略可以用来回测？', '帮我梳理一个均线策略的思路'] : session.profile === 'pipeline_builder' ? ['查看当前有哪些流水线', '帮我设计一个行情分析流程'] : ['帮我梳理一个研究思路', '如何有条理地复盘一笔交易？']).map(text => <button type="button" className="secondary" key={text} onClick={() => { setDraft(text); textarea.current?.focus(); }}>{text}<span aria-hidden="true">↗</span></button>)}</div></div>}
          {rows.map(row => row.kind === 'tool' && pythonIDs.has(row.id) ? null : row.kind === 'tool' && analysisIDs.has(row.id) ? (row.id === overviewAt ? <ResearchKlineOverview key={runID} runID={runID} tools={analyses} /> : null) : row.kind === 'tool' ? <ToolCard key={`tool-${row.id}`} tool={row.tool} runID={runID} terminal={currentView?.terminal ?? true} /> : row.kind === 'archive' ? <details className="assistant-tool-archive" key={row.id}><summary>{row.name ? toolLabels[row.name.replace(/^mcp__q4d__/, '')] ?? row.name : row.failed ? '工具调用未完成' : '工具结果'}<span>会话记录</span></summary>{row.text && <pre>{row.text}</pre>}</details> : <article className={`assistant-message ${row.role === 'user' ? 'from-user' : 'from-assistant'}`} key={row.id}><div className="assistant-message-role">{row.role === 'user' ? '你' : <><span className="assistant-brand" aria-hidden="true">✳</span> Quant4Dad</>}</div><div className="assistant-message-text">{row.role === 'user' ? row.text : <Markdown text={row.text} />}</div>{row.streaming && active && <span className="assistant-stream-cursor" aria-label="正在输出" />}{row.omitted && <p className="muted">部分内容未展示。</p>}{row.role === 'assistant' && row.text && !(row.streaming && active) && <button className="ghost assistant-copy" type="button" onClick={() => copyMessage(row.id, row.text)}>{copied === row.id ? '已复制' : '复制回答'}</button>}</article>)}
          {pythonTools.length > 0 && <ResearchPythonResults key={`python-${runID}`} runID={runID} tools={pythonTools} />}
          {posting && pending && <article className="assistant-message from-user is-pending"><div className="assistant-message-role">你 · 发送中</div><div className="assistant-message-text">{pending.text}</div></article>}
          {currentView?.notice && <p className="muted">{currentView.notice}</p>}
          </div>
        </div>
        <div className="assistant-compose">
          {showLatest && <button type="button" className="secondary assistant-jump" onClick={() => { if (messages.current) messages.current.scrollTop = messages.current.scrollHeight; nearBottom.current = true; setShowLatest(false); }}>↓ 回到最新消息</button>}
          <div className="assistant-run-status">
            {status ? <RunActivity key={`${sessionID}-${runID}-${posting}`} label={status} running={animating} /> : <span className="muted">{session ? '准备就绪' : '正在连接'}</span>}
            {runID && <span className="assistant-actions">{currentView?.status === 'recovering' && <button className="secondary" disabled={busy} onClick={() => operation(async () => { await agentAPI.reconcileRun(runID); setRetry(value => value + 1); })}>核对恢复状态</button>}{disconnected && <button className="ghost" onClick={() => { setError(''); setRetry(value => value + 1); }}>重新连接</button>}</span>}
          </div>
          {session && !['active', 'archived'].includes(session.status) ? <button disabled={busy} onClick={() => operation(async () => { const value = await agentAPI.reconcile(sessionID); if (current.current === value.session_id) setSession(value); await list(); })}>恢复会话创建</button> : session?.status === 'archived' ? <p className="muted">恢复会话后可继续发送消息。</p> : <form className="assistant-input-box" onSubmit={e => { e.preventDefault(); void send(); }}>
            <textarea ref={textarea} aria-label="消息" aria-describedby="assistant-input-hint" aria-invalid={tooLong} placeholder={active ? '可以先写下一条消息…' : '发消息，开始你的研究…'} rows={2} value={draft} disabled={posting || !!pending || loading || !session} onChange={e => setDraft(e.target.value)} onCompositionStart={() => { composing.current = true; }} onCompositionEnd={() => { composing.current = false; }} onKeyDown={e => { if (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing && !composing.current && e.nativeEvent.keyCode !== 229) { e.preventDefault(); if (!e.repeat) void send(); } }} />
            <div className="assistant-compose-footer"><span className="assistant-profile-chip">{profileLabels[session?.profile ?? ''] ?? '助手'}</span>{active ? <button type="button" className="assistant-stop" aria-label="停止生成" title="暂停本次回答，停止当前运行" disabled={stopping === runID} onClick={() => void stop()}><span className="assistant-stop-square" aria-hidden="true" />{stopping === runID ? '正在停止' : '停止生成'}</button> : <button type="submit" className="assistant-send" aria-label={pending ? '重试原消息' : '发送'} title={pending ? '重试原消息' : '发送消息'} disabled={posting || loading || !session || (!pending && (!draft.trim() || tooLong))}>{posting ? '发送中…' : pending ? '重试原消息' : <svg viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d="M12 19V5m-6 6 6-6 6 6" /></svg>}</button>}</div>
          </form>}
          <div id="assistant-input-hint" className={`assistant-input-hint ${tooLong ? 'is-error' : ''}`}>{tooLong ? `消息最多 32,000 字，当前 ${[...draft].length.toLocaleString()} 字` : pending ? '发送结果待确认，可重试原消息' : <><span>Enter 发送 · Shift + Enter 换行</span><span>支持 Markdown</span></>}</div>
        </div>
      </>}
    </div>
  </section>;
}
