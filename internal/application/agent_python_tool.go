package application

import (
	"context"

	"github.com/quant4dad/internal/pythonexec"
	"github.com/quant4dad/internal/service"
)

const pythonChartGuide = `绘图边界：chart_period仅支持单标的日线输入；多标的比较先一次执行Python输出比较统计表，需要图片时每只ETF各执行一次并分别展示，不把多个标的塞入同一张图，不丢弃比较表。不能为猜图表日期反复探针调用：/data/input.json的chart_context.available、dates、period给出服务器实际可绘制日期轴；曲线和事件日期必须属于dates。周线标签是名义周五（节假日周五也保留该标签），不是该周最后交易日；用date+timedelta(days=(4-date.weekday())%7)分组，过滤到chart_context.dates；尾部未结束周会被排除。月线用名义月末。chart_context不可用时读reason并按真实限制交付，不能猜测已出图。
复权口径：fund_adj仅适用于ETF，当前未接入股票复权因子，不能建议用fund_adj修复股票。query_kline保存原始价格，adjustment_status=verified且adjustment_source=tushare.fund_adj表示该文件每根日线都有真实因子。前复权请求必须在execute_python传adjustment=qfq和明确end_date；服务端用close*当日因子/基准因子处理全部日OHLC后，再交给Python和聚合图表。data.price_basis=qfq时rows中的open/high/low/close已经前复权，禁止再次乘因子；adj_factor仅供审计，成交量和金额保持原值。data.adjustments给出每只标的的source、anchor_date、anchor_factor；基准日是输入中不晚于end_date的最后交易日，报告实际基准并核对多标的日期一致。默认adjustment=none保持原始价格；analyze_kline仍只处理原始价格，复权统计用execute_python。复权区间必须全部有可信因子，旧文件或缺因子会在脚本执行前拒绝，应说明真实缺口，不能生成假前复权结果。真实因子全1或恒定可以有效，以来源和覆盖判定，不能仅看数值。后复权暂不支持。前复权市场价格收益不等于基金净值收益或账户分红现金总回报；未经净值/分红/指数/费用核验，不从价格差推断收益归因。
多均线/多类别研究图：完整保留用户要求的所有曲线和事件，不能为节省输出删线或合并类别。设置chart_period和统计起止日期，脚本chart.lines放全部均线（最多4条/每条600点）；用chart.marker_groups替代扁平markers，例如{"lines":[{"name":"MA5","points":[["2025-01-03",1.23]]}],"marker_groups":[{"name":"下穿 MA5","line_name":"MA5","dates":["2025-01-03"]}]}。最多4组/每组600个日期，组名唯一、不同于曲线名，line_name对应已有曲线；组名和日期须真实，零次命中保留空dates。前端生成一张总图，均线及对应事件同色、每组独立图例，同日多事件错位显示。不要同时输出markers与marker_groups。
收益率先用原始精度计算，百分比表格/JSON（含_pct字段）必须乘100后再舍入，并与正文一致；收益率小数使用_ratio字段并明示单位。交叉比较用Decimal原始精度；比较close*n和最近n根收盘之和避免除法/浮点相等误判。这个乘法比较仅用于事件判断，图线和表格的SMA数值必须用窗口和/n（例如round(float(sum_n/Decimal(n)),6)），不能直接输出窗口和。MA<n>/SMA<n>命名约定为含当期收盘的简单均线，服务端会用完整预热数据独立核对每个非空图点（允许4位小数舍入）；不符返回invalid_output，必须按warnings修正重新执行。先判断事件，再格式化输出。stdout64KiB，使用json.dumps(result,ensure_ascii=False,allow_nan=False,separators=(",",":"))，只输出展示区间均线点、适度小数位、必要明细，避免重复行情。完整结果（包括服务器K线与源码）最多128KiB。超限根据实际status/warnings修正，不能用删掉需求来规避；chart为空时不能声称已生成。图表和统计会显示在回答末尾的研究结果中，可一次下载总图。
`

func agentPythonTool(files *service.AgentKlineFileService, profiles []string) ToolDefinition {
	text := func(max int) map[string]any { return map[string]any{"type": "string", "maxLength": max} }
	date := map[string]any{"type": "string", "pattern": `^\d{4}-\d{2}-\d{2}$`}
	input := objectSchema(map[string]any{
		"file_ids": arraySchema(map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"}, 1, 40),
		"code":     map[string]any{"type": "string", "minLength": 1, "maxLength": 16384}, "title": text(120),
		"start_date": date, "end_date": date, "chart_period": map[string]any{"type": "string", "enum": []string{"1d", "1w", "1mo"}},
		"adjustment": map[string]any{"type": "string", "enum": []string{"none", "qfq"}},
	}, "file_ids", "code")
	// Rich output is independently validated by the fixed UI renderer. The
	// arbitrary JSON result remains untrusted and is not promoted to market facts.
	output := objectSchema(map[string]any{
		"kind": map[string]any{"type": "string", "const": "python_analysis_v1"}, "title": text(360), "runtime": text(100),
		"source": text(16384), "source_sha256": text(64), "input_sha256": text(64),
		"sources": arraySchema(map[string]any{"type": "object", "additionalProperties": true}, 1, 40), "input_count": intSchema(1, 20000),
		"start_date": text(10), "end_date": text(10), "status": map[string]any{"type": "string", "enum": []string{"succeeded", "python_error", "runtime_error", "timed_out", "output_limit", "invalid_output"}},
		"duration_ms": intSchema(0, 30000), "stdout": text(pythonexec.MaxOutputBytes), "stderr": text(4096),
		"result": map[string]any{"type": "object", "additionalProperties": true}, "chart": map[string]any{"oneOf": []any{map[string]any{"type": "object", "additionalProperties": true}, map[string]any{"type": "null"}}},
		"warnings":          arraySchema(text(512), 0, 10),
		"price_basis":       map[string]any{"type": "string", "enum": []string{"stored_ohlc", "qfq"}},
		"adjustment_status": map[string]any{"type": "string", "enum": []string{"unverified", "verified"}},
		"adjustments": arraySchema(objectSchema(map[string]any{
			"code": text(32), "source": text(32), "anchor_date": date, "anchor_factor": map[string]any{"type": "number", "exclusiveMinimum": 0},
		}, "code", "source", "anchor_date", "anchor_factor"), 0, 40),
	}, "kind", "title", "runtime", "source", "source_sha256", "input_sha256", "sources", "input_count", "start_date", "end_date", "status", "duration_ms", "stdout", "stderr", "result", "chart", "warnings")
	t := readTool("execute_python", pythonChartGuide+`在隔离 CPython 3.12 中执行真实 Python 脚本，适合均线交叉、周线聚合、滚动窗口、分组统计等 analyze_kline 未覆盖的问题。输入仅为本会话 query_kline 的1至40个文件，最多20000根；同标的同周期按日期合并、去重，冲突拒绝。不会再次查询或同步行情。用 data=json.load(open('/data/input.json')) 读取；data.datasets 每项含 code、period、columns、rows（date,open,high,low,close,volume,amount,adj_factor）；data.sources 保留范围与截断标记；start_date/end_date标记统计与展示区间，应使用用户要求的起止日期，不能因取了前收或预热而提前start_date；更早的输入行仍保留用于计算。先检查数据和口径，周均线须从日线聚合后计算，交叉比较前一周期与当前周期，保留完整预热，不能把低于均线当下穿。
可 import json,math,statistics,decimal,datetime,collections,csv 等标准库；无 pandas/numpy/matplotlib/pip、网络、服务器文件、环境凭据或子进程；128MiB内存、20秒、源码16KiB、stdout64KiB。禁止依赖外部数据或用常量编造统计。只 print 一个 JSON 对象，建议 {"summary":"口径说明","metrics":{"count":3},"table":{"columns":["date","close","ma"],"rows":[...]}}，用 json.dumps(...,ensure_ascii=False,allow_nan=False)，不要打印全部输入行情。status=succeeded 仅代表执行和JSON解析成功，不保证公式正确；报错后根据 stderr 修正代码再执行，不得用猜测结果补齐。
可选 chart_period=1d/1w/1mo 生成一张来自同一标的日线输入的总图；start_date/end_date 限制展示范围，最多600根。周线按周五、月线按月末聚合，只保留周期末<=实际最后输入日期与end_date的周期，起始周期可能不完整。脚本 JSON 可附 chart:{"lines":[{"name":"MA60","points":[["2025-01-03",1.23],...]}],"markers":["2025-01-03",...]}，日期须落在展示区间，最多4条线/600点。OHLCV由服务器从输入生成，不能由脚本覆盖。报告数据截至、预热/排除、实际复权口径与基准，交易日缺口仍未核验；可下载源码、JSON、表格与单张总图。`, input, output, profiles, func(ctx context.Context, raw []byte) (any, error) {
		var in service.PythonAnalysisInput
		if err := decodeToolArgs(raw, &in); err != nil {
			return nil, err
		}
		return files.ExecutePython(ctx, in)
	})
	t.TimeoutMS = 25000
	return t
}
