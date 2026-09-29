import { useMemo } from 'react';
import * as echarts from 'echarts';
import { useChartPalette } from '../charts/useChartPalette';
import type { KlineDisplay } from './kline-overview';
import ReactECharts from 'echarts-for-react';
import { comparisonSymbol, klineChartOption, klineMatchesCSV } from './kline-analysis.ts';
import type { KlineAnalysis as Analysis } from './kline-analysis.ts';

function download(url: string, name: string) {
  const link = document.createElement('a'); link.href = url; link.download = name; link.click();
}

export function KlineAnalysis({ analysis: a, segments = [] }: { analysis: KlineDisplay; segments?: Analysis[] }) {
  const palette = useChartPalette();
  const option = useMemo(() => klineChartOption(a, palette), [a, palette]);
  const filename = `${a.code}-${a.first_date || a.requested_start}-${a.last_date || a.requested_end}-${a.metric}-${a.comparison}${a.threshold_pct}`;
  const condition = `${a.metric === 'high_return' ? '最高价' : '收盘价'}相对前一可用收盘价 ${comparisonSymbol[a.comparison]} ${a.threshold_pct}%`;
  const saveCSV = () => {
    const url = URL.createObjectURL(new Blob(['\uFEFF' + klineMatchesCSV(a)], { type: 'text/csv;charset=utf-8' }));
    download(url, `${filename}-matches.csv`); setTimeout(() => URL.revokeObjectURL(url), 1000);
  };
  const savePNG = () => {
    // Render a separate full-range export, preserving the on-screen zoom and
    // excluding hover state. The same palette applies to the downloaded image.
    const container = document.createElement('div');
    const instance = echarts.init(container, undefined, { width: 1440, height: 680 });
    try {
      instance.setOption(klineChartOption(a, palette));
      instance.getZr().flush();
      download(instance.getDataURL({ type: 'png', pixelRatio: 2, backgroundColor: palette.surface }), `${filename}.png`);
    } finally { instance.dispose(); }
  };
  return <section className="assistant-kline-analysis" aria-label="行情统计和 K 线图">
    <div className="assistant-kline-summary"><strong>{a.code} · {a.period === '1d' ? '日 K' : a.period === '1w' ? '周 K' : '月 K'}</strong><span>{a.matched_count} 次命中 / {a.eligible_count} 根有效样本{a.match_rate_pct !== null ? ` · ${a.match_rate_pct.toFixed(2)}%` : ''}</span></div>
    <p>{condition}</p>
    <p className="muted">{a.first_date || '无数据'}{a.last_date ? ` — ${a.last_date}` : ''} · 数据截至 {a.data_as_of || '未知'} · 排除 {a.excluded_count} 根</p>
    {a.bar_count > 0 && <><ReactECharts option={option} style={{ height: 'clamp(440px, 52dvh, 760px)' }} notMerge />
      <p className="muted">三角标记为命中日期，可拖动底部滑块缩放；下载图片始终包含完整区间。成交量单位未声明。</p></>}
    <div className="assistant-actions"><button type="button" className="secondary" onClick={savePNG} disabled={!a.bar_count}>下载 K 线图</button><button type="button" className="secondary" onClick={saveCSV}>下载命中表</button></div>
    {segments.length > 1 && <div className="assistant-kline-segments"><p className="muted">已将 {segments.length} 个区间汇总为一张图，共 {a.bar_count.toLocaleString()} 根 K 线。</p><div className="assistant-kline-scroll"><table><thead><tr><th>统计区间</th><th>命中</th><th>有效样本</th><th>占比</th></tr></thead><tbody>{segments.map(segment => <tr key={`${segment.file_id}-${segment.requested_start}-${segment.requested_end}`}><td>{segment.first_date} — {segment.last_date}</td><td>{segment.matched_count}</td><td>{segment.eligible_count}</td><td>{segment.match_rate_pct === null ? '—' : `${segment.match_rate_pct.toFixed(2)}%`}</td></tr>)}</tbody></table></div></div>}
    <details className="assistant-kline-table" open={a.matched_count > 0 && a.matched_count <= 20}><summary>命中明细（{a.matched_count} 条）</summary>
      <div className="assistant-kline-scroll"><table><thead><tr><th>日期</th><th>前一日期</th><th>前收</th><th>{a.metric === 'high_return' ? '最高价' : '收盘价'}</th><th>涨跌幅</th></tr></thead><tbody>{a.matches.rows.map(r => <tr key={r[0]}><td>{r[0]}</td><td>{r[1]}</td><td>{r[2]}</td><td>{r[3]}</td><td>{r[4].toFixed(2)}%</td></tr>)}</tbody></table></div>
    </details>
    <details className="assistant-kline-notes" open={a.truncated || a.excluded_count > 0}><summary>统计口径与数据范围</summary><ul>{a.warnings.map((w, i) => <li key={i}>{w}</li>)}</ul></details>
  </section>;
}
