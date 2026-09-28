import { useEffect, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { api } from '../api';
import type { IndicatorMeta, RuleSpec, StrategyBody, BarPeriod, FillAt, StrategyMode } from '../types';
import IndicatorList from '../components/IndicatorList';
import ConditionEditor from '../components/ConditionEditor';
import ActionEditor from '../components/ActionEditor';
import Select from '../components/Select';
import StockPicker from '../components/StockPicker';
import CodeEditor from '../components/CodeEditor';
import { condToJSON, defaultCond, jsonToCond } from '../components/condCodec';

interface FormState {
  name: string;
  description: string;
  universe: string[];        // the instrument codes
  period: BarPeriod;
  fill_at: FillAt;
  mode: StrategyMode;
  code: string;              // the script mode's source
  body: StrategyBody;
}

// The starting template for a new script-mode strategy, which doubles as documentation of the
// available ctx interface in its comments.
const SCRIPT_TEMPLATE = `# on_bar(ctx) 每根 K 线调用一次，返回 buy(...) / sell(...) / None
# ctx 字段: index / date / open / high / low / close / volume
#          has_position / entry_price / days_held
#          history(field, n) -> 最近 n 个值（含当前，最旧在前）
#          pnl_pct() -> 浮动盈亏率（无持仓返回 None）
# 下单: buy("all") / buy(pct_of_cash=0.5) / buy(shares=100) / buy(fixed_cash=10000)
#       sell("all") / sell(pct_of_position=0.5)

def on_bar(ctx):
    closes = ctx.history("close", 20)
    if len(closes) < 20:
        return None
    ma20 = sum(closes) / len(closes)
    if not ctx.has_position and ctx.close > ma20:
        return buy(pct_of_cash=0.5)
    if ctx.has_position and ctx.close < ma20:
        return sell("all")
    return None
`;

const EMPTY: FormState = {
  name: '', description: '', universe: [], period: '1d', fill_at: 'next_open',
  mode: 'config', code: SCRIPT_TEMPLATE,
  body: { indicators: [], rules: [], execution: { fill_at: 'next_open' } },
};

// Initial sample rule for first-time users.
const SAMPLE_RULE: RuleSpec = {
  name: '示例：金叉买入',
  when: { kind: 'all', children: [
    { kind: 'cross', dir: 'cross_up', a: { type: 'ref', name: '' }, b: { type: 'ref', name: '' } },
    { kind: 'not', child: { kind: 'call', op: 'has_position' } },
  ]},
  then: { action: 'buy', size: { pct_of_cash: 1.0 } },
};

export default function StrategyEditor() {
  const { id } = useParams();
  const nav = useNavigate();
  const editing = !!id;
  const [form, setForm] = useState<FormState>(EMPTY);
  const [metas, setMetas] = useState<IndicatorMeta[]>([]);
  const [err, setErr] = useState('');
  const [saving, setSaving] = useState(false);
  const [version, setVersion] = useState<number>();

  useEffect(() => {
    api.listIndicators().then(setMetas).catch(e => setErr(String(e.message || e)));
    if (!editing) return;
    api.getStrategy(Number(id)).then(s => {
      setVersion(s.version);
      const body: any = typeof s.body === 'string' ? JSON.parse(s.body) : s.body;
      const rules: RuleSpec[] = (body.rules || []).map((r: any) => ({
        name: r.name || '',
        when: jsonToCond(r.when),
        then: { action: r.then?.action || 'buy', size: r.then?.size ?? 'all' },
      }));
      setForm({
        name: s.name,
        description: s.description || '',
        universe: s.universe || [],
        period: s.period,
        fill_at: (body.execution && body.execution.fill_at) || 'next_open',
        mode: body.mode === 'script' ? 'script' : 'config',
        code: body.code || SCRIPT_TEMPLATE,
        body: { indicators: body.indicators || [], rules, execution: body.execution || { fill_at: 'next_open' } },
      });
    }).catch(e => setErr(String(e.message || e)));
  }, [id]);

  const setRule = (i: number, patch: Partial<RuleSpec>) => {
    const rules = form.body.rules.slice();
    rules[i] = { ...rules[i], ...patch };
    setForm({ ...form, body: { ...form.body, rules } });
  };

  const addRule = () => {
    const sample = form.body.rules.length === 0 ? SAMPLE_RULE : { name: '', when: defaultCond(), then: { action: 'buy' as const, size: 'all' as const } };
    setForm({ ...form, body: { ...form.body, rules: [...form.body.rules, sample] } });
  };

  const removeRule = (i: number) =>
    setForm({ ...form, body: { ...form.body, rules: form.body.rules.filter((_, j) => j !== i) } });

  const save = async () => {
    setErr(''); setSaving(true);
    try {
      const body = form.mode === 'script'
        ? {
            mode: 'script',
            lang: 'starlark',
            code: form.code,
            execution: { fill_at: form.fill_at },
          }
        : {
            indicators: form.body.indicators,
            // Convert the editor's CondExpr back to the server JSON shape per-rule.
            rules: form.body.rules.map(r => ({
              name: r.name,
              when: condToJSON(r.when),
              then: r.then,
            })),
            execution: { fill_at: form.fill_at },
          };
      const payload = {
        expected_version: editing ? version : undefined,
        name: form.name.trim(),
        description: form.description,
        universe: form.universe,
        period: form.period,
        body: body as any,
      };
      if (editing) await api.updateStrategy(Number(id), payload);
      else await api.createStrategy(payload);
      nav('/strategies');
    } catch (e: any) {
      setErr(e.message || String(e));
    } finally {
      setSaving(false);
    }
  };

  return (
    <>
      <section>
        <h2>{editing ? `编辑策略 #${id}` : '新建策略'}</h2>
        {err && <p className="error">{err}</p>}
        <div style={{ marginBottom: 12 }}>
          <label>编排模式</label>
          <Select
            value={form.mode}
            onChange={v => setForm({ ...form, mode: v as StrategyMode })}
            options={[
              { value: 'config', label: '配置模式（可视化规则）' },
              { value: 'script', label: '脚本模式（Starlark 代码）' },
            ]}
            minWidth={260}
          />
        </div>
        <div className="row">
          <div>
            <label>名称</label>
            <input value={form.name} onChange={e => setForm({ ...form, name: e.target.value })} placeholder="比如：MA金叉买入" style={{ width: '100%' }} />
          </div>
          <div>
            <label>周期</label>
            <Select
              value={form.period}
              onChange={v => setForm({ ...form, period: v as BarPeriod })}
              options={[
                { value: '1d',  label: '日线' },
                { value: '1w',  label: '周线' },
                { value: '1mo', label: '月线' },
              ]}
              style={{ width: '100%' }}
            />
          </div>
        </div>
        <div style={{ marginTop: 12 }}>
          <label>标的（搜索 A 股名称或代码，点选添加）</label>
          <StockPicker value={form.universe} onChange={universe => setForm({ ...form, universe })} />
        </div>
        <div style={{ marginTop: 12 }}>
          <label>描述</label>
          <textarea rows={2} style={{ width: '100%' }} value={form.description} onChange={e => setForm({ ...form, description: e.target.value })} />
        </div>
      </section>

      {form.mode === 'config' && <>
      <section>
        <h2>指标</h2>
        <p className="muted">先声明指标并起一个别名（alias），下面的条件里用这个别名来引用。</p>
        <IndicatorList
          value={form.body.indicators}
          onChange={indicators => setForm({ ...form, body: { ...form.body, indicators } })}
          metas={metas}
        />
      </section>

      <section>
        <h2>规则 <span className="muted">（按顺序匹配，命中即执行对应动作）</span></h2>
        {form.body.rules.length === 0 && <p className="muted">还没有规则。点下面「添加规则」开始可视化编排。</p>}
        {form.body.rules.map((r, i) => (
          <div key={i} className="rule-card">
            <div className="rule-header">
              <input
                value={r.name} placeholder={`规则 #${i + 1}`} style={{ width: 260, fontWeight: 600 }}
                onChange={e => setRule(i, { name: e.target.value })}
              />
              <button className="danger small" onClick={() => removeRule(i)}>删除规则</button>
            </div>

            <div className="rule-section">
              <h4>当 (WHEN)</h4>
              <ConditionEditor
                value={r.when}
                onChange={when => setRule(i, { when })}
                indicators={form.body.indicators} metas={metas}
              />
            </div>

            <div className="rule-section">
              <h4>则 (THEN)</h4>
              <ActionEditor value={r.then} onChange={then => setRule(i, { then })} />
            </div>
          </div>
        ))}
        <button onClick={addRule}>+ 添加规则</button>
      </section>
      </>}

      {form.mode === 'script' && (
        <section>
          <h2>策略脚本 <span className="muted">（Starlark，需定义 on_bar(ctx)）</span></h2>
          <p className="muted">脚本逐根 K 线调用 on_bar(ctx)，用 ctx.history 自行计算指标，返回 buy(...) / sell(...) / None。</p>
          <CodeEditor value={form.code} onChange={code => setForm({ ...form, code })} />
        </section>
      )}

      <section>
        <h2>撮合</h2>
        <label>撮合时机</label>
        <Select
          value={form.fill_at}
          onChange={v => setForm({ ...form, fill_at: v as FillAt })}
          options={[
            { value: 'next_open', label: '下一根 K 线开盘价' },
            { value: 'close',     label: '当根 K 线收盘价' },
          ]}
          minWidth={220}
        />
      </section>

      <section>
        <button disabled={saving} onClick={save}>{saving ? '保存中…' : '保存策略'}</button>
        {' '}
        <button className="secondary" onClick={() => nav('/strategies')}>取消</button>
      </section>
    </>
  );
}
