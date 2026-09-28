"""Daily SMA down-crosses; execute_python(chart_period='1d') supplies candles."""
import json
from decimal import Decimal, getcontext

getcontext().prec = 60
data = json.load(open('/data/input.json'))
assert len(data['datasets']) == 1 and data['datasets'][0]['period'] == '1d'
rows = data['datasets'][0]['rows']
dates = [r[0] for r in rows]
close = [Decimal(str(r[4])) for r in rows]
start, end = data['start_date'] or dates[0], data['end_date'] or dates[-1]
metrics, events, lines, groups = {}, [], [], []
for n in (5, 10, 20, 60):
    sums = [None if i < n-1 else sum(close[i-n+1:i+1]) for i in range(len(rows))]
    hits, points, eligible = [], [], 0
    for i, date in enumerate(dates):
        if not start <= date <= end:
            continue
        if sums[i] is not None:
            points.append([date, float(round(sums[i] / n, 6))])
        if i == 0 or sums[i-1] is None:
            continue
        eligible += 1
        if close[i-1] * n >= sums[i-1] and close[i] * n < sums[i]:
            hits.append(date)
            events.append([date, 'MA' + str(n), float(close[i-1]),
                           float(round(sums[i-1] / n, 6)), float(close[i]),
                           float(round(sums[i] / n, 6))])
    name = 'MA' + str(n)
    metrics[name + '_crosses'] = len(hits)
    metrics[name + '_eligible'] = eligible
    lines.append({'name': name, 'points': points})
    groups.append({'name': '下穿 ' + name, 'line_name': name, 'dates': hits})
events.sort()
print(json.dumps({
    'summary': '下穿：前收盘>=前SMA且当前收盘<当前SMA；均线含当日，原始Decimal精度判断后才格式化。',
    'metrics': metrics,
    'table': {'columns': ['date', 'ma', 'previous_close', 'previous_ma', 'close', 'ma_value'], 'rows': events},
    'chart': {'lines': lines, 'marker_groups': groups},
}, ensure_ascii=False, allow_nan=False, separators=(',', ':')))
