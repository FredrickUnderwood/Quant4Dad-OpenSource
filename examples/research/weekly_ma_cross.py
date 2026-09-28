"""Count completed-week closes crossing below SMA(60) from owned daily data.

Calendar-Friday labels; missing exchange sessions and price adjustment remain
unverified. The first partial calendar week and unfinished tail are excluded.
"""
import datetime as dt
import json
from decimal import Decimal

data = json.load(open('/data/input.json'))
assert len(data['datasets']) == 1, 'one instrument is required'
dataset = data['datasets'][0]
assert dataset['period'] == '1d', 'daily input is required'
bars = [dict(zip(dataset['columns'], row)) for row in dataset['rows']]
cutoff = min(data['end_date'] or bars[-1]['date'], bars[-1]['date'])
weeks = {}
for bar in bars:
    day = dt.date.fromisoformat(bar['date'])
    friday = (day + dt.timedelta(days=4-day.weekday())).isoformat()
    if friday <= cutoff:
        weeks[friday] = Decimal(str(bar['close']))
first_day = dt.date.fromisoformat(bars[0]['date'])
partial_first = (first_day + dt.timedelta(days=4-first_day.weekday())).isoformat()
excluded_first = first_day.weekday() != 0 and partial_first in weeks
if excluded_first:
    del weeks[partial_first]
dates = sorted(weeks)
closes = [weeks[date] for date in dates]
averages = [None if i < 59 else sum(closes[i-59:i+1])/Decimal(60) for i in range(len(closes))]
matches, points = [], []
eligible, warmup = 0, 0
for i, date in enumerate(dates):
    if data['start_date'] and date < data['start_date']:
        continue
    points.append([date, float(averages[i]) if averages[i] is not None else None])
    if i < 60:
        warmup += 1
        continue
    eligible += 1
    if closes[i-1] >= averages[i-1] and closes[i] < averages[i]:
        matches.append([date, float(closes[i]), float(averages[i]), dates[i-1], float(closes[i-1]), float(averages[i-1])])
result = {
    'summary': '按周五标记已结束周；前周收盘 >= 前周60周均线且本周收盘 < 本周60周均线。均线包含各自当周收盘，Decimal计算后再显示。',
    'metrics': {'count': len(matches), 'eligible_weeks': eligible, 'warmup_excluded_in_range': warmup,
                'completed_weeks_with_warmup': len(dates), 'excluded_partial_first_week': excluded_first,
                'last_completed_week': dates[-1] if dates else None, 'cutoff': cutoff},
    'table': {'columns': ['date', 'close', 'ma60', 'previous_week', 'previous_close', 'previous_ma60'], 'rows': matches},
    'chart': {'lines': [{'name': 'MA60', 'points': points}], 'markers': [row[0] for row in matches]},
}
print(json.dumps(result, ensure_ascii=False, allow_nan=False))
