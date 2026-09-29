import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import {
  ReactFlow, ReactFlowProvider, Background, Controls,
  Handle, Position, addEdge, useNodesState, useEdgesState, useReactFlow,
  type Node, type Edge, type Connection, type NodeProps, type NodeTypes,
} from '@xyflow/react';
import '@xyflow/react/dist/style.css';

import { api } from '../api';
import type {
  NodeConfig, NodeConfigSchema, NodeTypeMeta, PipelineInput, PipelineStatus, DryRunResult, EdgeCondition,
} from '../types';
import { NEWS_SOURCE_LABEL } from '../types';
import NodeConfigForm from '../components/NodeConfigForm';
import Select from '../components/Select';
import { StatusTag } from './PipelinesPage';

// The React Flow node's data. A type alias rather than an interface, to satisfy v12's
// requirement that data be assignable to Record<string, unknown>.
type Q4DNodeData = {
  label: string;
  ntype: string;       // the node type id (keyword_filter / ai_analysis / ...)
  category: string;
  config: NodeConfig;
};
type Q4DNode = Node<Q4DNodeData>;

// The edge's data, carrying the optional routing condition.
type Q4DEdgeData = { condition?: EdgeCondition | null };
type Q4DEdge = Edge<Q4DEdgeData>;

const CATEGORY_LABEL: Record<string, string> = {
  filter: '过滤', ai: 'AI', output: '触达', general: '通用',
};

const COND_OPS: { value: EdgeCondition['op']; label: string }[] = [
  { value: 'eq', label: '等于' },
  { value: 'ne', label: '不等于' },
  { value: 'contains', label: '包含' },
  { value: 'gt', label: '大于' },
  { value: 'lt', label: '小于' },
  { value: 'exists', label: '字段存在' },
];

// condLabel compresses a condition into a short label for the edge on the canvas; an
// unconditional edge returns an empty string and shows no label.
function condLabel(c?: EdgeCondition | null): string {
  if (!c || !c.field) return '';
  if (c.op === 'exists') return `∃ ${c.field}`;
  const sym: Record<string, string> = { eq: '=', ne: '≠', contains: '⊇', gt: '>', lt: '<' };
  return `${c.field} ${sym[c.op] ?? c.op} ${c.value ?? ''}`;
}

// The node card's summary: the few config items that matter most for each node type, so a card
// is readable at a glance on the canvas.
function nodeSummary(ntype: string, cfg: NodeConfig): string {
  if (ntype === 'keyword_filter') {
    const kws = Array.isArray(cfg.keywords) ? (cfg.keywords as unknown[]) : [];
    const wl = cfg.list_type === 'whitelist';
    return `${kws.length} 个关键词 · ${wl ? '白名单(仅保留命中)' : '黑名单(命中拦截)'}`;
  }
  if (ntype === 'ai_analysis') {
    const provider = (cfg.provider as string) || '未选模型';
    const model = (cfg.model as string) || '默认模型';
    return `${provider} / ${model}`;
  }
  if (ntype === 'delivery') {
    const channel = cfg.channel === 'feishu' ? '飞书' : '邮箱';
    const to = (cfg.to as string)?.trim();
    return `${channel}${to ? ` · ${to.split(/[,;\n]/)[0]}…` : ''}`;
  }
  return '';
}

// The custom node view: a handle on each side, with the card showing the type, name and summary.
function PipelineNodeView({ data, selected }: NodeProps<Q4DNode>) {
  return (
    <div className={`pl-node cat-${data.category}${selected ? ' selected' : ''}`}>
      <Handle type="target" position={Position.Left} />
      <div className="pl-node-cat">{CATEGORY_LABEL[data.category] ?? data.category}</div>
      <div className="pl-node-title">{data.label}</div>
      <div className="pl-node-summary">{nodeSummary(data.ntype, data.config) || '未配置'}</div>
      <Handle type="source" position={Position.Right} />
    </div>
  );
}

const nodeTypes: NodeTypes = { q4d: PipelineNodeView };

// Build the initial config from the schema's default fields.
function defaultConfig(schema: NodeConfigSchema): NodeConfig {
  const cfg: NodeConfig = {};
  for (const [k, p] of Object.entries(schema.properties ?? {})) {
    if (p.default !== undefined) cfg[k] = p.default;
  }
  return cfg;
}

function EditorInner() {
  const { id } = useParams();
  const nav = useNavigate();
  const pipelineId = id ? Number(id) : null;

  const [metas, setMetas] = useState<NodeTypeMeta[]>([]);
  const [providers, setProviders] = useState<string[]>([]);
  const [channels, setChannels] = useState<{ value: string; label: string }[]>([]);
  const [name, setName] = useState('');
  const [desc, setDesc] = useState('');
  const [status, setStatus] = useState<PipelineStatus>('draft');
  const [version, setVersion] = useState<number>();
  const [sources, setSources] = useState<string[]>([]);
  // The subscribable news sources come from what the backend has registered, not from a
  // hardcoded list here.
  const [availableSources, setAvailableSources] = useState<string[]>([]);

  const [nodes, setNodes, onNodesChange] = useNodesState<Q4DNode>([]);
  const [edges, setEdges, onEdgesChange] = useEdgesState<Q4DEdge>([]);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [selectedEdgeId, setSelectedEdgeId] = useState<string | null>(null);

  const [err, setErr] = useState('');
  const [saving, setSaving] = useState(false);
  const [dirty, setDirty] = useState(false);
  const [tab, setTab] = useState<'config' | 'dryrun'>('config');

  const { screenToFlowPosition } = useReactFlow();
  const keySeq = useRef(1);

  const metaByType = useMemo(() => {
    const m: Record<string, NodeTypeMeta> = {};
    metas.forEach((x) => { m[x.type] = x; });
    return m;
  }, [metas]);

  // Load the node type metadata and the existing pipeline.
  useEffect(() => {
    api.nodeTypes().then(setMetas).catch((e) => setErr(String(e.message || e)));
  }, []);

  // Load the news sources the backend has registered, for the subscription checkboxes. The list
  // is empty when no collection implementation is plugged in.
  useEffect(() => {
    api.getNewsSources()
      .then((r) => setAvailableSources((r.sources ?? []).map((s) => s.source)))
      .catch(() => { /* if it can't be fetched, treat it as no subscribable sources; the panel says so */ });
  }, []);

  // Load the configured LLM providers, for the AI node's provider dropdown.
  useEffect(() => {
    api.getLLMProviders()
      .then((r) => setProviders(Object.keys(r.providers ?? {})))
      .catch(() => { /* stay silent when nothing is configured; the dropdown points at Settings */ });
  }, []);

  // Load the configured delivery channels — only those that really have SMTP or a webhook set
  // up — for the delivery node's channel dropdown.
  useEffect(() => {
    Promise.allSettled([api.getNotifyEmail(), api.getNotifyFeishu()]).then(([e, f]) => {
      const out: { value: string; label: string }[] = [];
      if (e.status === 'fulfilled' && e.value.email.host) out.push({ value: 'email', label: '邮箱 (SMTP)' });
      if (f.status === 'fulfilled' && f.value.feishu.webhook_url) out.push({ value: 'feishu', label: '飞书' });
      setChannels(out);
    });
  }, []);

  // The dynamic enums injected into the config form: an x_enum_source field takes its runtime
  // options by key.
  const dynamicEnums = useMemo(
    () => ({ llm_providers: providers, notify_channels: channels }),
    [providers, channels],
  );

  useEffect(() => {
    if (pipelineId === null) return;
    api.getPipeline(pipelineId)
      .then((p) => {
        setName(p.name);
        setDesc(p.description ?? '');
        setStatus(p.status);
        setVersion(p.version);
        setSources(p.sources ?? []);
        const rfNodes: Q4DNode[] = (p.nodes ?? []).map((n) => ({
          id: n.node_key,
          type: 'q4d',
          position: { x: n.pos_x, y: n.pos_y },
          data: { label: n.name || n.node_key, ntype: n.type, category: 'general', config: n.config ?? {} },
        }));
        const rfEdges: Q4DEdge[] = (p.edges ?? []).map((e) => ({
          id: `${e.from_node_key}->${e.to_node_key}`,
          source: e.from_node_key,
          target: e.to_node_key,
          data: { condition: e.condition ?? null },
          label: condLabel(e.condition),
        }));
        setNodes(rfNodes);
        setEdges(rfEdges);
        setDirty(false);
      })
      .catch((e) => setErr(String(e.message || e)));
  }, [pipelineId, setNodes, setEdges]);

  // Backfill each node's category once the metadata has arrived, since the type's grouping is
  // unknown at load time.
  useEffect(() => {
    if (metas.length === 0) return;
    setNodes((ns) => ns.map((n) => {
      const cat = metaByType[n.data.ntype]?.category ?? 'general';
      return cat === n.data.category ? n : { ...n, data: { ...n.data, category: cat } };
    }));
  }, [metas, metaByType, setNodes]);

  const markDirty = useCallback(() => setDirty(true), []);

  const onConnect = useCallback((c: Connection) => {
    if (c.source === c.target) return; // no self-loops
    setEdges((es) => addEdge({ ...c, id: `${c.source}->${c.target}`, data: { condition: null } }, es));
    markDirty();
  }, [setEdges, markDirty]);

  const uniqueKey = useCallback((ntype: string): string => {
    const existing = new Set(nodes.map((n) => n.id));
    let k = `${ntype}_${keySeq.current}`;
    while (existing.has(k)) { keySeq.current += 1; k = `${ntype}_${keySeq.current}`; }
    keySeq.current += 1;
    return k;
  }, [nodes]);

  const addNode = useCallback((ntype: string, position: { x: number; y: number }) => {
    const meta = metaByType[ntype];
    if (!meta) return;
    const key = uniqueKey(ntype);
    const node: Q4DNode = {
      id: key,
      type: 'q4d',
      position,
      data: { label: meta.name, ntype, category: meta.category, config: defaultConfig(meta.config_schema) },
    };
    setNodes((ns) => [...ns, node]);
    setSelectedId(key);
    setTab('config');
    markDirty();
  }, [metaByType, uniqueKey, setNodes, markDirty]);

  const onDragOver = useCallback((e: React.DragEvent) => {
    e.preventDefault();
    e.dataTransfer.dropEffect = 'move';
  }, []);

  const onDrop = useCallback((e: React.DragEvent) => {
    e.preventDefault();
    const ntype = e.dataTransfer.getData('application/q4d-node');
    if (!ntype) return;
    const position = screenToFlowPosition({ x: e.clientX, y: e.clientY });
    addNode(ntype, position);
  }, [screenToFlowPosition, addNode]);

  const selected = nodes.find((n) => n.id === selectedId) ?? null;
  const selectedSchema = selected ? metaByType[selected.data.ntype]?.config_schema : undefined;

  const updateSelectedConfig = (config: NodeConfig) => {
    if (!selected) return;
    setNodes((ns) => ns.map((n) => (n.id === selected.id ? { ...n, data: { ...n.data, config } } : n)));
    markDirty();
  };
  const renameSelected = (label: string) => {
    if (!selected) return;
    setNodes((ns) => ns.map((n) => (n.id === selected.id ? { ...n, data: { ...n.data, label } } : n)));
    markDirty();
  };
  const deleteSelected = () => {
    if (!selected) return;
    setNodes((ns) => ns.filter((n) => n.id !== selected.id));
    setEdges((es) => es.filter((e) => e.source !== selected.id && e.target !== selected.id));
    setSelectedId(null);
    markDirty();
  };

  const selectedEdge = edges.find((e) => e.id === selectedEdgeId) ?? null;
  const updateEdgeCondition = (cond: EdgeCondition | null) => {
    if (!selectedEdge) return;
    setEdges((es) => es.map((e) => (
      e.id === selectedEdge.id ? { ...e, data: { ...e.data, condition: cond }, label: condLabel(cond) } : e
    )));
    markDirty();
  };
  const deleteSelectedEdge = () => {
    if (!selectedEdge) return;
    setEdges((es) => es.filter((e) => e.id !== selectedEdge.id));
    setSelectedEdgeId(null);
    markDirty();
  };

  const buildInput = (st: PipelineStatus): PipelineInput => ({
    expected_version: pipelineId === null ? undefined : version,
    name: name.trim(),
    description: desc.trim(),
    status: st,
    nodes: nodes.map((n) => ({
      node_key: n.id,
      type: n.data.ntype,
      name: n.data.label,
      config: n.data.config,
      pos_x: Math.round(n.position.x),
      pos_y: Math.round(n.position.y),
    })),
    edges: edges.map((e) => ({
      from_node_key: e.source,
      to_node_key: e.target,
      condition: e.data?.condition ?? null,
    })),
    sources,
  });

  const toggleSource = (src: string) => {
    setSources((ss) => (ss.includes(src) ? ss.filter((s) => s !== src) : [...ss, src]));
    markDirty();
  };

  const save = async (): Promise<number | null> => {
    setErr('');
    if (!name.trim()) { setErr('请填写流水线名称'); return null; }
    if (nodes.length === 0) { setErr('至少需要一个节点'); return null; }
    setSaving(true);
    try {
      const input = buildInput(status);
      if (pipelineId === null) {
        const created = await api.createPipeline(input);
        setDirty(false);
        nav(`/pipelines/${created.id}/edit`, { replace: true });
        return created.id;
      }
      const updated = await api.updatePipeline(pipelineId, input);
      setVersion(updated.version);
      setDirty(false);
      return pipelineId;
    } catch (e) {
      setErr(String((e as Error).message || e));
      return null;
    } finally {
      setSaving(false);
    }
  };

  return (
    <section className="pl-editor">
      <div className="pl-toolbar">
        <button className="ghost" onClick={() => nav('/pipelines')}>← 返回</button>
        <input
          className="pl-name"
          value={name}
          placeholder="流水线名称"
          onChange={(e) => { setName(e.target.value); markDirty(); }}
        />
        <input
          className="pl-desc"
          value={desc}
          placeholder="描述（可选）"
          onChange={(e) => { setDesc(e.target.value); markDirty(); }}
        />
        <span className="inline" style={{ marginLeft: 'auto', gap: 10 }}>
          <StatusTag s={status} />
          {dirty && <span className="muted">● 未保存</span>}
          <button disabled={saving} onClick={() => save()}>{saving ? '保存中…' : '保存'}</button>
        </span>
      </div>

      {err && <div className="error" style={{ margin: '8px 0' }}>{err}</div>}

      <div className="pl-workbench">
        {/* Left: the node palette */}
        <aside className="pl-palette">
          <h4>节点</h4>
          <div className="muted" style={{ marginBottom: 10 }}>拖到画布，或点击添加</div>
          {metas.map((m) => (
            <div
              key={m.type}
              className={`pl-palette-item cat-${m.category}`}
              draggable
              onDragStart={(e) => {
                e.dataTransfer.setData('application/q4d-node', m.type);
                e.dataTransfer.effectAllowed = 'move';
              }}
              onClick={() => addNode(m.type, { x: 80 + nodes.length * 40, y: 80 + nodes.length * 30 })}
              title="拖到画布或点击添加"
            >
              <div className="pl-palette-cat">{CATEGORY_LABEL[m.category] ?? m.category}</div>
              <div className="pl-palette-name">{m.name}</div>
            </div>
          ))}

          <h4 style={{ marginTop: 20 }}>资讯源订阅</h4>
          <div className="muted" style={{ marginBottom: 10 }}>勾选的源有新资讯时自动触发本流水线</div>
          {availableSources.length === 0 ? (
            <div className="muted">暂无可订阅的资讯源（后端未接入采集实现）</div>
          ) : availableSources.map((src) => (
            <label key={src} className="pl-source-item">
              <input
                type="checkbox"
                checked={sources.includes(src)}
                onChange={() => toggleSource(src)}
              />
              <span>{NEWS_SOURCE_LABEL[src] ?? src}</span>
            </label>
          ))}
        </aside>

        {/* Middle: the canvas */}
        <div className="pl-canvas" onDrop={onDrop} onDragOver={onDragOver}>
          <ReactFlow
            nodes={nodes}
            edges={edges}
            onNodesChange={(c) => {
              onNodesChange(c);
              // Mark unsaved on adding or removing a node, or on a position change once a drag
              // has finished (dragging=false). Don't wrongly mark dirty on the dimensions change
              // from the initial mount, or on a bare selection.
              const meaningful = c.some((x) =>
                x.type === 'add' || x.type === 'remove' || x.type === 'replace' ||
                (x.type === 'position' && x.dragging === false));
              if (meaningful) markDirty();
            }}
            onEdgesChange={(c) => { onEdgesChange(c); if (c.some((x) => x.type === 'remove' || x.type === 'add')) markDirty(); }}
            onConnect={onConnect}
            onNodeClick={(_, n) => { setSelectedId(n.id); setSelectedEdgeId(null); setTab('config'); }}
            onEdgeClick={(_, e) => { setSelectedEdgeId(e.id); setSelectedId(null); setTab('config'); }}
            onPaneClick={() => { setSelectedId(null); setSelectedEdgeId(null); }}
            nodeTypes={nodeTypes}
            fitView
            proOptions={{ hideAttribution: true }}
          >
            <Background gap={18} />
            <Controls showInteractive={false} />
          </ReactFlow>
          {nodes.length === 0 && (
            <div className="pl-canvas-hint">从左侧拖入节点，连线后保存</div>
          )}
        </div>

        {/* Right: config and dry run */}
        <aside className="pl-panel">
          <div className="pl-panel-tabs">
            <button className={tab === 'config' ? '' : 'secondary'} onClick={() => setTab('config')}>节点配置</button>
            <button className={tab === 'dryrun' ? '' : 'secondary'} onClick={() => setTab('dryrun')}>试运行</button>
          </div>

          {tab === 'config' ? (
            selectedEdge ? (
              <EdgeConditionEditor
                key={selectedEdge.id}
                from={selectedEdge.source}
                to={selectedEdge.target}
                condition={selectedEdge.data?.condition ?? null}
                onChange={updateEdgeCondition}
                onDelete={deleteSelectedEdge}
              />
            ) : !selected ? (
              <p className="muted">点击画布上的节点编辑配置，或点击连线设置分支路由条件。</p>
            ) : (
              <div>
                <h4>{metaByType[selected.data.ntype]?.name ?? selected.data.ntype}</h4>
                <div className="node-form-row">
                  <label>节点名称</label>
                  <input value={selected.data.label} onChange={(e) => renameSelected(e.target.value)} />
                </div>
                <div className="muted" style={{ marginBottom: 12 }}>
                  key: <code>{selected.id}</code>
                </div>
                {selectedSchema
                  ? <NodeConfigForm key={selected.id} schema={selectedSchema} value={selected.data.config} onChange={updateSelectedConfig} dynamicEnums={dynamicEnums} />
                  : <p className="muted">无 schema</p>}
                <button className="danger small" style={{ marginTop: 16 }} onClick={deleteSelected}>删除此节点</button>
              </div>
            )
          ) : (
            <DryRunPanel input={() => buildInput(status)} />
          )}
        </aside>
      </div>
    </section>
  );
}

// EdgeConditionEditor edits one edge's routing condition. Off means unconditional, so an
// upstream Pass flows straight downstream; on decides whether the branch activates from the
// field, operator and value. The field a condition names is one the upstream node wrote into the
// payload, such as the sentiment or score an AI node writes back.
function EdgeConditionEditor({
  from, to, condition, onChange, onDelete,
}: {
  from: string;
  to: string;
  condition: EdgeCondition | null;
  onChange: (c: EdgeCondition | null) => void;
  onDelete: () => void;
}) {
  const enabled = !!condition;
  const c: EdgeCondition = condition ?? { field: '', op: 'eq', value: '' };
  const needsValue = c.op !== 'exists';

  const patch = (p: Partial<EdgeCondition>) => onChange({ ...c, ...p });

  return (
    <div>
      <h4>连线路由条件</h4>
      <div className="muted" style={{ marginBottom: 12 }}>
        <code>{from}</code> → <code>{to}</code>
      </div>
      <label className="pl-source-item" style={{ marginBottom: 12 }}>
        <input
          type="checkbox"
          checked={enabled}
          onChange={(e) => onChange(e.target.checked ? { field: '', op: 'eq', value: '' } : null)}
        />
        <span>启用条件路由（关闭则无条件，始终流向下游）</span>
      </label>

      {enabled && (
        <>
          <div className="node-form-row">
            <label>字段</label>
            <input
              value={c.field}
              placeholder="上游写入的字段，如 sentiment"
              onChange={(e) => patch({ field: e.target.value })}
            />
          </div>
          <div className="node-form-row">
            <label>算子</label>
            <Select
              value={c.op}
              options={COND_OPS}
              onChange={(v) => patch({ op: v as EdgeCondition['op'] })}
            />
          </div>
          {needsValue && (
            <div className="node-form-row">
              <label>比较值</label>
              <input
                value={c.value ?? ''}
                placeholder="gt/lt 需为数字"
                onChange={(e) => patch({ value: e.target.value })}
              />
            </div>
          )}
          <div className="muted" style={{ marginTop: 8 }}>
            预览：<code>{condLabel(c) || '（条件不完整）'}</code>
          </div>
        </>
      )}

      <button className="danger small" style={{ marginTop: 16 }} onClick={onDelete}>删除此连线</button>
    </div>
  );
}

function DryRunPanel({ input }: { input: () => PipelineInput }) {
  const [sample, setSample] = useState('{\n  "title": "示例标题",\n  "content": "正文内容"\n}');
  const [res, setRes] = useState<DryRunResult | null>(null);
  const [err, setErr] = useState('');
  const [busy, setBusy] = useState(false);

  const run = async () => {
    setErr('');
    setRes(null);
    let parsed: Record<string, unknown>;
    try {
      parsed = JSON.parse(sample);
      if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) throw new Error();
    } catch {
      setErr('样例事件不是合法 JSON');
      return;
    }
    setBusy(true);
    try {
      const r = await api.previewPipeline(input(), parsed);
      setRes(r);
    } catch (e) {
      setErr(String((e as Error).message || e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div>
      <h4>样例事件 (JSON)</h4>
      <textarea
        rows={7}
        value={sample}
        onChange={(e) => setSample(e.target.value)}
        style={{ fontFamily: 'JetBrains Mono, ui-monospace, monospace' }}
      />
      <button disabled={busy} style={{ marginTop: 10 }} onClick={run}>{busy ? '运行中…' : '试运行'}</button>
      <div className="muted" style={{ marginTop: 6 }}>预览当前编辑内容，不保存流水线或执行结果。邮件和飞书不会发送；AI 节点仍会调用配置的模型。</div>

      {err && <div className="error" style={{ marginTop: 10 }}>{err}</div>}

      {res && (
        <div style={{ marginTop: 14 }}>
          <div className="inline" style={{ marginBottom: 10 }}>
            <span>结果：</span>
            <span className={`tag ${res.status === 'passed' ? 'success' : res.status === 'dropped' ? 'failed' : 'running'}`}>
              {res.status === 'passed' ? '通过' : res.status === 'dropped' ? `在 ${res.dropped_at_node} 丢弃` : `在 ${res.failed_at_node} 失败`}
            </span>
          </div>
          <h4>逐节点</h4>
          <div className="table-scroll">
            <table>
              <thead><tr><th>节点</th><th>动作</th><th>耗时</th></tr></thead>
              <tbody>
                {res.traces.map((t, i) => (
                  <tr key={i}>
                    <td>{t.node_key}<div className="muted">{t.node_type}</div></td>
                    <td>
                      <span className={`tag ${t.action === 'drop' ? 'failed' : 'success'}`}>{t.action === 'drop' ? '丢弃' : '通过'}</span>
                      {t.error && <div className="error" style={{ marginTop: 4 }}>{t.error}</div>}
                      {t.delivery_preview && <details style={{ marginTop: 6 }}>
                        <summary>触达预览 · 未发送</summary>
                        <div>{t.delivery_preview.channel === 'email' ? '邮件' : '飞书'}</div>
                        {t.delivery_preview.channel === 'email' && <div>收件人：{t.delivery_preview.uses_default_recipients ? '使用通道默认收件人' : t.delivery_preview.recipients.join(', ')}</div>}
                        <strong>{t.delivery_preview.title}</strong>
                        <pre style={{ whiteSpace: 'pre-wrap' }}>{t.delivery_preview.body}</pre>
                      </details>}
                    </td>
                    <td className="mono">{t.latency_ms}ms</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <h4 style={{ marginTop: 14 }}>最终 payload</h4>
          <pre>{JSON.stringify(res.final_payload, null, 2)}</pre>
        </div>
      )}
    </div>
  );
}

// useReactFlow must be called inside a ReactFlowProvider, hence this wrapper.
export default function PipelineEditor() {
  return (
    <ReactFlowProvider>
      <EditorInner />
    </ReactFlowProvider>
  );
}
