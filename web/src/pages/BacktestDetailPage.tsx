import { useEffect, useMemo, useState } from 'react';
import { useParams } from 'react-router-dom';
import ReactECharts from 'echarts-for-react';
import { useChartPalette } from '../charts/useChartPalette';
import type { ChartPalette } from '../charts/palette';
import { api } from '../api';
import type { Bar, BacktestJob, BacktestResult, EquityPoint, Trade } from '../types';

function StatusTag({ s }: { s: string }) {
  const cls = s === 'succeed' ? 'success' : s === 'failed' ? 'failed' : 'running';
  return <span className={`tag ${cls}`}>{s}</span>;
}

const fmtPct = (v: number) => (v * 100).toFixed(2) + '%';
const fmtNum = (v: number) => (Math.round(v * 100) / 100).toLocaleString();
const signCls = (v?: number) => (v == null ? '' : v > 0 ? 'up' : v < 0 ? 'down' : '');

function CandleChart({ bars, trades, palette }: { bars: Bar[]; trades: Trade[]; palette: ChartPalette }) {
  const p = palette;
  const dates = bars.map(b => (b.date || '').slice(0, 10));
  const candles = bars.map(b => [b.open, b.close, b.low, b.high]);
  const volumes = bars.map(b => ({
    value: b.volume,
    itemStyle: { color: b.close >= b.open ? p.up : p.down, opacity: 0.55 },
  }));

  const markData = trades.map(t => {
    const isBuy = t.side === 'buy';
    return {
      name: isBuy ? 'B' : 'S',
      value: isBuy ? 'B' : 'S',
      coord: [(t.time || '').slice(0, 10), t.price],
      symbol: 'triangle',
      symbolSize: [10, 12],
      symbolRotate: isBuy ? 0 : 180,
      symbolOffset: [0, isBuy ? 14 : -14],
      itemStyle: { color: isBuy ? p.up : p.down, borderColor: p.surface, borderWidth: 1 },
      label: {
        show: true,
        position: isBuy ? 'bottom' : 'top',
        color: isBuy ? p.up : p.down,
        fontFamily: 'JetBrains Mono, monospace',
        fontSize: 10,
        formatter: isBuy ? 'B' : 'S',
      },
    };
  });

  const opt = {
    backgroundColor: 'transparent',
    textStyle: { color: p.ink, fontFamily: 'Manrope, sans-serif' },
    tooltip: {
      trigger: 'axis' as const,
      axisPointer: { type: 'shadow' as const },
      backgroundColor: p.surface,
      borderColor: p.hair,
      textStyle: { color: p.ink, fontFamily: 'JetBrains Mono, monospace', fontSize: 11 },
    },
    grid: [
      { left: 64, right: 24, top: 16, height: 220, borderColor: p.hair },
      { left: 64, right: 24, top: 260, height: 60, borderColor: p.hair },
    ],
    xAxis: [
      {
        type: 'category' as const, data: dates, gridIndex: 0,
        axisLabel: { show: false },
        axisLine: { lineStyle: { color: p.rule } },
        axisTick: { lineStyle: { color: p.rule } },
      },
      {
        type: 'category' as const, data: dates, gridIndex: 1,
        axisLine: { lineStyle: { color: p.rule } },
        axisTick: { lineStyle: { color: p.rule } },
        axisLabel: { color: p.muted, fontFamily: 'JetBrains Mono, monospace', fontSize: 10 },
      },
    ],
    yAxis: [
      {
        gridIndex: 0, scale: true,
        axisLine: { show: false }, axisTick: { show: false },
        axisLabel: { color: p.muted, fontFamily: 'JetBrains Mono, monospace', fontSize: 10 },
        splitLine: { lineStyle: { color: p.hair, type: 'dashed' as const } },
      },
      {
        gridIndex: 1, splitNumber: 2,
        axisLine: { show: false }, axisTick: { show: false },
        axisLabel: { color: p.muted, fontFamily: 'JetBrains Mono, monospace', fontSize: 9 },
        splitLine: { show: false },
      },
    ],
    dataZoom: [
      { type: 'inside' as const, xAxisIndex: [0, 1], start: 0, end: 100 },
    ],
    series: [
      {
        name: 'K',
        type: 'candlestick' as const,
        xAxisIndex: 0,
        yAxisIndex: 0,
        data: candles,
        itemStyle: {
          color: p.up, color0: p.down,
          borderColor: p.up, borderColor0: p.down,
        },
        markPoint: markData.length > 0
          ? { data: markData, animation: false, silent: false }
          : undefined,
      },
      {
        name: '成交量',
        type: 'bar' as const,
        xAxisIndex: 1,
        yAxisIndex: 1,
        data: volumes,
        barWidth: '60%',
      },
    ],
  };

  return <ReactECharts option={opt} style={{ height: 360 }} notMerge={true} />;
}

export default function BacktestDetailPage() {
  const { id } = useParams();
  const jobId = Number(id);
  const [job, setJob] = useState<BacktestJob | null>(null);
  const [result, setResult] = useState<BacktestResult | null>(null);
  const [equity, setEquity] = useState<EquityPoint[]>([]);
  const [trades, setTrades] = useState<Trade[]>([]);
  const [bars, setBars] = useState<Record<string, Bar[]>>({});

  useEffect(() => {
    let stop = false;
    const tick = async () => {
      try {
        const j = await api.getBacktest(jobId);
        if (stop) return;
        setJob(j);
        if (j.status === 'succeed') {
          const [res, eq, tr] = await Promise.all([
            api.getBacktestResult(jobId).catch(() => null),
            api.getBacktestEquity(jobId).catch(() => []),
            api.getBacktestTrades(jobId).catch(() => []),
          ]);
          if (stop) return;
          if (res) setResult(res);
          setEquity(eq || []);
          setTrades(tr || []);
          return; // done polling
        }
        if (j.status !== 'failed') setTimeout(tick, 2000);
      } catch {
        if (!stop) setTimeout(tick, 2000);
      }
    };
    tick();
    return () => { stop = true; };
  }, [jobId]);

  // The instruments that were traded, ordered by first fill, which keeps the render order stable.
  const codes = useMemo(() => {
    const first = new Map<string, string>();
    for (const t of trades) {
      if (!first.has(t.code)) first.set(t.code, t.time || '');
    }
    return [...first.keys()].sort((a, b) => (first.get(a) || '').localeCompare(first.get(b) || ''));
  }, [trades]);

  const tradesByCode = useMemo(() => {
    const m = new Map<string, Trade[]>();
    for (const t of trades) {
      const arr = m.get(t.code) || [];
      arr.push(t);
      m.set(t.code, arr);
    }
    return m;
  }, [trades]);

  // Fetch daily bars per instrument, using the backtest's start and end dates as the range.
  useEffect(() => {
    if (!job || codes.length === 0) return;
    let cancelled = false;
    (async () => {
      const start = (job.start_date || '').slice(0, 10);
      const end = (job.end_date || '').slice(0, 10);
      const missing = codes.filter(c => !bars[c]);
      if (missing.length === 0) return;
      const results = await Promise.all(
        missing.map(c => api.getInstrumentBars(c, '1d', start, end).catch(() => [] as Bar[])),
      );
      if (cancelled) return;
      setBars(prev => {
        const next = { ...prev };
        missing.forEach((c, i) => { next[c] = results[i]; });
        return next;
      });
    })();
    return () => { cancelled = true; };
  }, [job, codes, bars]);

  const ruleStats = (() => {
    if (!result?.rule_stats) return null;
    return typeof result.rule_stats === 'string' ? JSON.parse(result.rule_stats) : result.rule_stats;
  })();

  const p = useChartPalette();

  const chartOpt = {
    backgroundColor: 'transparent',
    textStyle: { color: p.ink, fontFamily: 'Manrope, sans-serif' },
    tooltip: {
      trigger: 'axis' as const,
      backgroundColor: p.surface,
      borderColor: p.hair,
      textStyle: { color: p.ink, fontFamily: 'JetBrains Mono, monospace', fontSize: 11 },
    },
    legend: {
      data: ['总资产', '回撤(%)'],
      // ECharts 6 defaults to the bottom, where it overlaps the date axis.
      top: 0, bottom: 'auto', left: 'center',
      textStyle: { color: p.muted, fontFamily: 'JetBrains Mono, monospace', fontSize: 11 },
      icon: 'rect', itemWidth: 10, itemHeight: 2,
    },
    grid: { left: 56, right: 64, top: 64, bottom: 40, borderColor: p.hair },
    xAxis: {
      type: 'category' as const,
      data: equity.map(pt => (pt.date || '').slice(0, 10)),
      axisLine: { lineStyle: { color: p.rule } },
      axisLabel: { color: p.muted, fontFamily: 'JetBrains Mono, monospace', fontSize: 10 },
      axisTick: { lineStyle: { color: p.rule } },
    },
    yAxis: [
      {
        type: 'value' as const, name: '总资产', position: 'left' as const,
        nameTextStyle: { color: p.muted, fontFamily: 'JetBrains Mono, monospace', fontSize: 10 },
        axisLine: { show: false }, axisTick: { show: false },
        axisLabel: { color: p.muted, fontFamily: 'JetBrains Mono, monospace', fontSize: 10 },
        splitLine: { lineStyle: { color: p.hair, type: 'dashed' as const } },
      },
      {
        type: 'value' as const, name: '回撤(%)', position: 'right' as const, max: 0,
        nameTextStyle: { color: p.muted, fontFamily: 'JetBrains Mono, monospace', fontSize: 10 },
        axisLine: { show: false }, axisTick: { show: false },
        axisLabel: { color: p.muted, fontFamily: 'JetBrains Mono, monospace', fontSize: 10 },
        splitLine: { show: false },
      },
    ],
    series: [
      { name: '总资产', type: 'line' as const, data: equity.map(pt => pt.total_value), smooth: true, showSymbol: false, color: p.up, lineStyle: { width: 1.8 } },
      { name: '回撤(%)', type: 'line' as const, yAxisIndex: 1, data: equity.map(pt => -(pt.drawdown || 0) * 100), areaStyle: { opacity: 0.18, color: p.down }, color: p.down, lineStyle: { width: 1 }, showSymbol: false },
    ],
  };

  return (
    <>
      <section>
        <h2>回测概览 {job && <StatusTag s={job.status} />}</h2>
        {job?.status === 'failed' && job.error_msg && (
          <p className="error">回测失败：{job.error_msg}</p>
        )}
        <div style={{ display: 'flex', flexWrap: 'wrap', rowGap: 16 }}>
          <span className={`metric ${result ? signCls(result.total_return) : ''}`}><strong>{result ? fmtPct(result.total_return) : '—'}</strong><span>总收益率</span></span>
          <span className={`metric ${result ? signCls(result.annualized_return) : ''}`}><strong>{result ? fmtPct(result.annualized_return) : '—'}</strong><span>年化收益</span></span>
          <span className="metric down"><strong>{result ? fmtPct(result.max_drawdown) : '—'}</strong><span>最大回撤</span></span>
          <span className="metric"><strong>{result ? result.sharpe.toFixed(2) : '—'}</strong><span>夏普比</span></span>
          <span className="metric"><strong>{result ? fmtPct(result.win_rate) : '—'}</strong><span>胜率</span></span>
          <span className="metric"><strong>{result ? result.trade_count : '—'}</strong><span>成交笔数</span></span>
        </div>
      </section>

      <section>
        <h2>资金曲线 & 回撤</h2>
        {equity.length === 0 ? <p className="muted">等待回测完成…</p> : <ReactECharts option={chartOpt} style={{ height: 360 }} />}
      </section>

      {codes.length > 0 && (
        <section>
          <h2>K 线 & 买卖点</h2>
          {codes.map(code => {
            const codeBars = bars[code];
            const codeTrades = tradesByCode.get(code) || [];
            return (
              <div key={code} style={{ marginBottom: 24 }}>
                <h3 style={{ margin: '8px 0' }}>
                  {code}
                  <span className="muted" style={{ marginLeft: 12, fontWeight: 'normal', fontSize: 12 }}>
                    {codeTrades.length} 笔成交
                  </span>
                </h3>
                {codeBars == null ? (
                  <p className="muted">加载 K 线中…</p>
                ) : codeBars.length === 0 ? (
                  <p className="muted">无 K 线数据</p>
                ) : (
                  <CandleChart bars={codeBars} trades={codeTrades} palette={p} />
                )}
              </div>
            );
          })}
        </section>
      )}

      {ruleStats && (
        <section>
          <h2>按规则归因</h2>
          <table>
            <thead><tr><th>规则</th><th>触发次数</th><th>盈利笔数</th><th>累计盈亏</th></tr></thead>
            <tbody>
              {Object.keys(ruleStats).map(name => {
                const s = ruleStats[name];
                return (
                  <tr key={name}>
                    <td>{name}</td>
                    <td>{s.trigger_count}</td>
                    <td>{s.win_count || 0}</td>
                    <td>{fmtNum(s.total_pnl || 0)}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </section>
      )}

      <section>
        <h2>成交记录</h2>
        <table>
          <thead><tr><th>时间</th><th>标的</th><th>方向</th><th>价格</th><th>数量</th><th>金额</th><th>佣金</th><th>印花税</th><th>已实现盈亏</th><th>触发规则</th></tr></thead>
          <tbody>
            {trades.map((t, i) => (
              <tr key={i}>
                <td>{(t.time || '').slice(0, 10)}</td>
                <td>{t.code}</td>
                <td><span className={`tag ${t.side === 'buy' ? '' : 'success'}`}>{t.side}</span></td>
                <td>{fmtNum(t.price)}</td>
                <td>{t.qty}</td>
                <td>{fmtNum(t.notional)}</td>
                <td>{fmtNum(t.commission)}</td>
                <td>{fmtNum(t.stamp_duty)}</td>
                <td>{t.realized_pnl != null ? fmtNum(t.realized_pnl) : '-'}</td>
                <td>{t.triggered_rule || ''}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </section>
    </>
  );
}
