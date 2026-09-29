import { useMemo } from 'react';
import * as echarts from 'echarts';
import ReactECharts from 'echarts-for-react';
import { useChartPalette } from '../charts/useChartPalette';
import { pythonChartOption, pythonTable, pythonTableCSV, pythonPriceBasis } from './python-analysis';
import type { PythonAnalysis as Analysis } from './python-analysis';

const status = { succeeded: '执行完成', python_error: '脚本报错', runtime_error: '执行失败', timed_out: '执行超时', output_limit: '输出超出限额', invalid_output: '输出格式无效' };
function download(body: string, name: string, type: string) {
  const url = URL.createObjectURL(new Blob([body], { type })); const link = document.createElement('a'); link.href = url; link.download = name; link.click(); setTimeout(() => URL.revokeObjectURL(url), 1000);
}
export function PythonAnalysis({ analysis: a }: { analysis: Analysis }) {
  const palette = useChartPalette();
  const option = useMemo(() => a.chart ? pythonChartOption(a, palette) : undefined, [a, palette]);
  const table = pythonTable(a), filename = `python-research-${a.source_sha256.slice(0, 12)}`;
  const metrics = a.status === 'succeeded' && a.result.metrics && typeof a.result.metrics === 'object' && !Array.isArray(a.result.metrics) ?
    Object.entries(a.result.metrics).filter(([, v]) => v === null || ['string', 'number', 'boolean'].includes(typeof v)).slice(0, 40) : [];
  const savePNG = () => {
    if (!option) return;
    const instance = echarts.init(document.createElement('div'), undefined, { width: 1440, height: 680 });
    try { instance.setOption(option); instance.getZr().flush(); const link = document.createElement('a'); link.href = instance.getDataURL({ type: 'png', pixelRatio: 2, backgroundColor: palette.surface }); link.download = `${filename}.png`; link.click(); }
    finally { instance.dispose(); }
  };
  return <section className="assistant-kline-analysis" aria-label="Python 行情研究结果">
    <div className="assistant-kline-summary"><strong>{a.title || 'Python 行情研究'}</strong><span>{status[a.status]} · {a.input_count.toLocaleString()} 根输入 · {a.duration_ms} ms</span></div>
    <p className="muted">{pythonPriceBasis(a)}。{a.price_basis !== 'qfq' && '图名和脚本标签不代表已完成前复权或分红调整。'}</p>
    {a.status === 'succeeded' && typeof a.result.summary === 'string' && <p>{a.result.summary.slice(0, 4000)}</p>}
    {option && <><ReactECharts option={option} style={{ height: 'clamp(520px, 56dvh, 800px)' }} notMerge /><p className="muted">均线与对应事件同色；同日多个事件错位标注。点击图例可筛选，下载总图保留完整区间和全部图例。</p></>}
    <div className="assistant-actions">
      {option && <button className="secondary" type="button" onClick={savePNG}>下载总图</button>}
      {table && <button className="secondary" type="button" onClick={() => download('\uFEFF' + pythonTableCSV(a), `${filename}.csv`, 'text/csv;charset=utf-8')}>下载统计表</button>}
      <button className="secondary" type="button" onClick={() => download(a.source, `${filename}.py`, 'text/x-python;charset=utf-8')}>下载 Python 源码</button>
      <button className="secondary" type="button" onClick={() => download(JSON.stringify(a, null, 2), `${filename}.json`, 'application/json')}>下载执行结果</button>
    </div>
    {metrics.length > 0 && <details open={!option}><summary>统计摘要</summary><div className="assistant-kline-scroll"><table><tbody>{metrics.map(([key, value]) => <tr key={key}><th>{key}</th><td>{String(value ?? '—')}</td></tr>)}</tbody></table></div></details>}
    {table && <details open={table.rows.length <= 20}><summary>统计明细（{table.rows.length} 条）</summary><div className="assistant-kline-scroll"><table><thead><tr>{table.columns.map((c, i) => <th key={i}>{c}</th>)}</tr></thead><tbody>{table.rows.map((r, i) => <tr key={i}>{r.map((v, j) => <td key={j}>{String(v ?? '—')}</td>)}</tr>)}</tbody></table></div></details>}
    {(a.stderr || a.stdout) && <details open={a.status !== 'succeeded'}><summary>执行输出</summary><pre>{a.stderr}{a.stdout}</pre></details>}
    <details><summary>查看 Python 源码与数据来源</summary><pre>{a.source}</pre><p className="muted">{a.runtime} · 源码 SHA256：{a.source_sha256}<br />输入 SHA256：{a.input_sha256}</p><ul>{a.sources.map(s => <li key={s.file_id}>{s.code} · {s.period} · {s.count} 根 · {s.requested_start} — {s.requested_end}{s.truncated ? ' · 已截断' : ''}<br /><code>{s.file_id}</code></li>)}</ul></details>
    <details className="assistant-kline-notes"><summary>计算口径与数据限制</summary><ul>{a.warnings.map((w, i) => <li key={i}>{w}</li>)}</ul></details>
  </section>;
}
