package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bytedance/sonic"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/pythonexec"
)

type PythonAnalysisInput struct {
	FileIDs     []string `json:"file_ids"`
	Code        string   `json:"code"`
	Title       string   `json:"title"`
	StartDate   string   `json:"start_date"`
	EndDate     string   `json:"end_date"`
	ChartPeriod string   `json:"chart_period"`
	Adjustment  string   `json:"adjustment"`
}
type PythonSource struct {
	FileID         string `json:"file_id"`
	Code           string `json:"code"`
	Period         string `json:"period"`
	Count          int    `json:"count"`
	RequestedStart string `json:"requested_start"`
	RequestedEnd   string `json:"requested_end"`
	Truncated      bool   `json:"truncated"`
	DataAsOf       string `json:"data_as_of"`
}
type PythonDataset struct {
	Code             string            `json:"code"`
	Period           string            `json:"period"`
	Columns          []string          `json:"columns"`
	Rows             [][]any           `json:"rows"`
	PriceBasis       string            `json:"price_basis"`
	AdjustmentStatus string            `json:"adjustment_status"`
	Adjustment       *PythonAdjustment `json:"adjustment,omitempty"`
}
type PythonChartLine struct {
	Name   string  `json:"name"`
	Points [][]any `json:"points"`
}
type PythonMarkerGroup struct {
	Name     string   `json:"name"`
	LineName string   `json:"line_name,omitempty"`
	Dates    []string `json:"dates"`
}
type PythonChart struct {
	Code         string              `json:"code"`
	Period       string              `json:"period"`
	Rows         [][]any             `json:"rows"`
	Lines        []PythonChartLine   `json:"lines"`
	Markers      []string            `json:"markers"`
	MarkerGroups []PythonMarkerGroup `json:"marker_groups,omitempty"`
}
type PythonAnalysis struct {
	Kind             string             `json:"kind"`
	Title            string             `json:"title"`
	Runtime          string             `json:"runtime"`
	Source           string             `json:"source"`
	SourceSHA256     string             `json:"source_sha256"`
	InputSHA256      string             `json:"input_sha256"`
	Sources          []PythonSource     `json:"sources"`
	InputCount       int                `json:"input_count"`
	StartDate        string             `json:"start_date"`
	EndDate          string             `json:"end_date"`
	Status           string             `json:"status"`
	DurationMS       int64              `json:"duration_ms"`
	Stdout           string             `json:"stdout"`
	Stderr           string             `json:"stderr"`
	Result           map[string]any     `json:"result"`
	Chart            *PythonChart       `json:"chart"`
	Warnings         []string           `json:"warnings"`
	PriceBasis       string             `json:"price_basis"`
	AdjustmentStatus string             `json:"adjustment_status"`
	Adjustments      []PythonAdjustment `json:"adjustments"`
}

func pythonSHA(body []byte) string { sum := sha256.Sum256(body); return hex.EncodeToString(sum[:]) }

// ExecutePython resolves session-owned snapshots before entering the VM. Model
// input cannot supply prices, credentials, a host path or a network destination.
func (s *AgentKlineFileService) ExecutePython(ctx context.Context, in PythonAnalysisInput) (PythonAnalysis, error) {
	var out PythonAnalysis
	if s.python == nil {
		return out, ErrToolUnavailable
	}
	if len(in.FileIDs) < 1 || len(in.FileIDs) > 40 || len(in.Code) < 1 || len(in.Code) > 16<<10 || len(in.Title) > 360 {
		return out, ErrToolInput
	}
	for _, date := range []string{in.StartDate, in.EndDate} {
		if date != "" {
			if _, err := time.Parse("2006-01-02", date); err != nil {
				return out, ErrToolInput
			}
		}
	}
	if in.StartDate != "" && in.EndDate != "" && in.StartDate > in.EndDate {
		return out, ErrToolInput
	}
	if in.ChartPeriod != "" && in.ChartPeriod != "1d" && in.ChartPeriod != "1w" && in.ChartPeriod != "1mo" {
		return out, ErrToolInput
	}
	if in.Adjustment != "" && in.Adjustment != "none" && in.Adjustment != "qfq" {
		return out, ErrToolInput
	}
	out = PythonAnalysis{Kind: "python_analysis_v1", Title: in.Title, Runtime: pythonexec.Version, Source: in.Code, SourceSHA256: pythonSHA([]byte(in.Code)), Sources: []PythonSource{}, StartDate: in.StartDate, EndDate: in.EndDate, Result: map[string]any{}, Warnings: []string{"脚本在隔离 Python 中实际执行；执行成功只证明代码运行完成，公式与统计口径仍需审查。", "输入为库中原始行情，未进行复权或交易日缺口核验；不得将缺失数据解释为零。"}}
	out.PriceBasis, out.AdjustmentStatus, out.Adjustments = "stored_ohlc", "unverified", []PythonAdjustment{}
	groups := map[string]map[string]*domain.Bar{}
	seen := map[string]bool{}
	for _, id := range in.FileIDs {
		if seen[id] {
			return PythonAnalysis{}, ErrToolInput
		}
		seen[id] = true
		file, err := s.load(ctx, id)
		if err != nil {
			return PythonAnalysis{}, err
		}
		r := file.Result
		if !ValidInstrumentCode(r.Code) || (r.Period != "1d" && r.Period != "1w" && r.Period != "1mo") {
			return PythonAnalysis{}, ErrToolUnavailable
		}
		out.Sources = append(out.Sources, PythonSource{id, r.Code, r.Period, r.Count, r.RequestedStart, r.RequestedEnd, r.Truncated, r.DataAsOf})
		key := r.Code + "/" + r.Period
		if groups[key] == nil {
			groups[key] = map[string]*domain.Bar{}
		}
		previous := ""
		for _, bar := range r.Bars {
			if !validAnalysisBar(bar) {
				return PythonAnalysis{}, ErrToolUnavailable
			}
			date := bar.Date.Format("2006-01-02")
			if date <= previous {
				return PythonAnalysis{}, ErrToolUnavailable
			}
			previous = date
			if prior := groups[key][date]; prior != nil {
				if prior.Open != bar.Open || prior.High != bar.High || prior.Low != bar.Low || prior.Close != bar.Close || prior.Volume != bar.Volume || prior.Amount != bar.Amount || prior.AdjFactor != bar.AdjFactor || prior.AdjSource != bar.AdjSource {
					return PythonAnalysis{}, ErrToolInput
				}
			} else {
				groups[key][date] = bar
			}
		}
		if r.Truncated && !strings.Contains(strings.Join(out.Warnings, ""), "截断") {
			out.Warnings = append(out.Warnings, "有输入文件被截断；需要另取早期窗口才能代表完整区间。")
		}
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	datasets := []PythonDataset{}
	var chartBars []*domain.Bar
	allVerified := true
	for _, key := range keys {
		parts := strings.Split(key, "/")
		d := PythonDataset{Code: parts[0], Period: parts[1], Columns: []string{"date", "open", "high", "low", "close", "volume", "amount", "adj_factor"}, Rows: [][]any{}}
		dates := make([]string, 0, len(groups[key]))
		for date := range groups[key] {
			dates = append(dates, date)
		}
		sort.Strings(dates)
		bars := make([]*domain.Bar, 0, len(dates))
		for _, date := range dates {
			bars = append(bars, groups[key][date])
		}
		d.PriceBasis = "stored_ohlc"
		d.AdjustmentStatus, _ = factorProvenance(d.Period, bars)
		if in.Adjustment == "qfq" {
			adjusted, metadata, adjustmentErr := forwardAdjustedBars(d.Code, d.Period, in.EndDate, bars)
			if adjustmentErr != nil {
				// Keep a bounded, actionable failure artifact. No script has run,
				// and no raw result is passed off as the requested adjusted result.
				out.InputCount = 0
				for _, group := range groups {
					out.InputCount += len(group)
				}
				if out.InputCount == 0 {
					return PythonAnalysis{}, ErrToolInput
				}
				body, marshalErr := sonic.Marshal(map[string]any{"groups": groups, "adjustment": in.Adjustment, "end_date": in.EndDate})
				if marshalErr != nil {
					return PythonAnalysis{}, ErrToolUnavailable
				}
				out.InputSHA256, out.Status = pythonSHA(body), "invalid_output"
				out.Adjustments = []PythonAdjustment{}
				out.Warnings = []string{"前复权未执行，Python脚本未运行：" + d.Code + "：" + adjustmentErr.Error()}
				return out, nil
			}
			bars, d.PriceBasis, d.AdjustmentStatus, d.Adjustment = adjusted, "qfq", "verified", &metadata
			out.Adjustments = append(out.Adjustments, metadata)
		}
		allVerified = allVerified && d.AdjustmentStatus == "verified"
		for _, b := range bars {
			date := b.Date.Format("2006-01-02")
			d.Rows = append(d.Rows, []any{date, b.Open, b.High, b.Low, b.Close, b.Volume, b.Amount, b.AdjFactor})
			if len(keys) == 1 {
				chartBars = append(chartBars, b)
			}
		}
		out.InputCount += len(d.Rows)
		datasets = append(datasets, d)
	}
	if out.InputCount == 0 {
		return PythonAnalysis{}, ErrToolInput
	}
	if allVerified {
		out.AdjustmentStatus = "verified"
		out.Warnings[1] = "输入为未复权原始行情，因子来源已核验；交易日缺口未经核验，不得将缺失数据解释为零。"
	}
	if in.Adjustment == "qfq" {
		out.PriceBasis = "qfq"
		out.Warnings[1] = "日线OHLC已由服务端按Tushare fund_adj前复权，再聚合周/月图；成交量和金额未调整。基准为各标的不晚于end_date的最后输入交易日，见adjustments。不得重复复权；不等同于账户现金分红或净值总回报，交易日缺口未经核验。"
	}
	// Give scripts the actual renderer axis, including holiday Fridays and
	// unfinished-period exclusions. Scripts must not discover it by trial runs.
	chartContext := map[string]any{"requested": in.ChartPeriod != "", "available": false, "dates": []string{}, "period": in.ChartPeriod, "price_basis": out.PriceBasis, "adjustment_status": out.AdjustmentStatus, "adjustments": out.Adjustments}
	if in.ChartPeriod != "" {
		if len(datasets) != 1 || datasets[0].Period != "1d" {
			chartContext["reason"] = "single_instrument_daily_input_required"
		} else if len(chartBars) == 0 {
			chartContext["reason"] = "no_chart_rows"
		} else if axis, axisErr := pythonChart(datasets[0].Code, chartBars, in, nil); axisErr != nil {
			chartContext["reason"] = axisErr.Error()
		} else {
			dates := make([]string, 0, len(axis.Rows))
			for _, row := range axis.Rows {
				dates = append(dates, row[0].(string))
			}
			chartContext["available"], chartContext["dates"] = true, dates
		}
	}
	input, err := sonic.Marshal(map[string]any{"datasets": datasets, "sources": out.Sources, "start_date": in.StartDate, "end_date": in.EndDate, "price_basis": out.PriceBasis, "adjustment_status": out.AdjustmentStatus, "adjustments": out.Adjustments, "coverage_verified": false, "chart_context": chartContext})
	if err != nil {
		return PythonAnalysis{}, ErrToolUnavailable
	}
	out.InputSHA256 = pythonSHA(input)
	executed, err := s.python.Execute(ctx, in.Code, input)
	if err != nil {
		return PythonAnalysis{}, ErrToolUnavailable
	}
	out.Status, out.DurationMS, out.Stderr = executed.Status, executed.DurationMS, executed.Stderr
	if executed.Status != "succeeded" {
		out.Stdout = executed.Stdout
		if executed.Status == "output_limit" {
			out.Warnings = append(out.Warnings, "Python 输出触及限制（stdout64KiB/stderr4KiB）：使用紧凑JSON、减少重复字段；不得删掉用户要求的曲线或合并事件类别来规避限额。")
		}
		return out, nil
	}
	// Preserve plain-text output on JSON errors so the agent can repair its code;
	// non-finite numbers, duplicate keys and extra documents are not accepted.
	if err := decodePythonResult([]byte(executed.Stdout), &out.Result); err != nil {
		out.Status = "invalid_output"
		out.Stdout = executed.Stdout
		out.Result = map[string]any{}
		out.Warnings = append(out.Warnings, "请只输出一个 JSON 对象，使用 print(json.dumps(result, allow_nan=False))。")
		return out, nil
	}
	if in.ChartPeriod != "" {
		if len(datasets) != 1 || datasets[0].Period != "1d" {
			out.Warnings = append(out.Warnings, "图表需要同一标的的日线输入，本次仅保留脚本结果。")
		} else {
			out.Chart, err = pythonChart(datasets[0].Code, chartBars, in, out.Result)
			if err != nil {
				out.Status = "invalid_output"
				out.Warnings = append(out.Warnings, "图表未生成："+err.Error())
			} else {
				// Keep overlays once, together with canonical OHLC. Repeating them
				// in result can needlessly exceed the bounded gateway artifact.
				delete(out.Result, "chart")
			}
		}
	}
	// Keep the complete artifact inside the gateway's existing 128KiB bound,
	// including canonical candles, source and provenance. Never silently drop
	// requested overlays or let a size error erase the execution record.
	encoded, err := json.Marshal(out)
	if err != nil {
		return PythonAnalysis{}, ErrToolUnavailable
	}
	if len(encoded) > 127<<10 {
		out.Status, out.Chart, out.Result = "output_limit", nil, map[string]any{}
		out.Warnings = append(out.Warnings, "完整结果超过128KiB：减少重复明细字段或输出小数位，再执行；不能悄悄省略曲线、事件或统计区间。")
	}
	return out, nil
}

func decodePythonResult(body []byte, result *map[string]any) error {
	if !utf8.Valid(body) {
		return fmt.Errorf("output must be UTF-8")
	}
	// Bound recursion and reject duplicate object keys without trusting Python's
	// encoder (scripts may write any bytes to stdout).
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var walk func(int) error
	walk = func(depth int) error {
		if depth > 16 {
			return fmt.Errorf("JSON nesting limit")
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		if delim != '{' && delim != '[' {
			return fmt.Errorf("invalid JSON")
		}
		seen := map[string]bool{}
		for decoder.More() {
			if delim == '{' {
				key, err := decoder.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return fmt.Errorf("duplicate JSON key")
				}
				seen[name] = true
			}
			if err := walk(depth + 1); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	}
	if err := walk(0); err != nil {
		return err
	}
	if decoder.More() {
		return fmt.Errorf("extra JSON document")
	}
	if err := json.Unmarshal(body, result); err != nil || *result == nil {
		return fmt.Errorf("expected JSON object")
	}
	return nil
}

// OHLCV always comes from the owned input. Python may only add finite overlays
// and date markers to this fixed renderer, never executable chart options.
func pythonChart(code string, bars []*domain.Bar, in PythonAnalysisInput, result map[string]any) (*PythonChart, error) {
	chart := &PythonChart{Code: code, Period: in.ChartPeriod, Rows: [][]any{}, Lines: []PythonChartLine{}, Markers: []string{}}
	allRows := [][]any{}
	end := bars[len(bars)-1].Date.Format("2006-01-02")
	if in.EndDate != "" && in.EndDate < end {
		end = in.EndDate
	}
	for _, b := range bars {
		date := b.Date
		if in.ChartPeriod == "1w" {
			date = date.AddDate(0, 0, (5-int(date.Weekday())+7)%7)
		}
		if in.ChartPeriod == "1mo" {
			date = time.Date(date.Year(), date.Month()+1, 0, 0, 0, 0, 0, date.Location())
		}
		label := date.Format("2006-01-02")
		if label > end {
			continue
		}
		last := len(allRows) - 1
		if last >= 0 && allRows[last][0] == label {
			r := allRows[last]
			r[2] = max(r[2].(float64), b.High)
			r[3] = min(r[3].(float64), b.Low)
			r[4] = b.Close
			r[5] = r[5].(float64) + b.Volume
		} else {
			allRows = append(allRows, []any{label, b.Open, b.High, b.Low, b.Close, b.Volume})
		}
	}
	for _, row := range allRows {
		if in.StartDate == "" || row[0].(string) >= in.StartDate {
			chart.Rows = append(chart.Rows, row)
		}
	}
	if len(chart.Rows) > 600 {
		return nil, fmt.Errorf("图表超过600根，请缩短展示区间或使用周/月聚合")
	}
	if len(chart.Rows) == 0 {
		return nil, fmt.Errorf("所选范围没有已结束周期")
	}
	dates := map[string]bool{}
	for _, r := range chart.Rows {
		dates[r[0].(string)] = true
	}
	if raw, ok := result["chart"]; ok {
		encoded, err := json.Marshal(raw)
		if err != nil {
			return nil, fmt.Errorf("图表标记格式错误")
		}
		var overlays struct {
			Lines        []PythonChartLine   `json:"lines"`
			Markers      []string            `json:"markers"`
			MarkerGroups []PythonMarkerGroup `json:"marker_groups"`
		}
		if json.Unmarshal(encoded, &overlays) != nil || len(overlays.Lines) > 4 || len(overlays.Markers) > 600 || len(overlays.MarkerGroups) > 4 || (len(overlays.MarkerGroups) > 0 && len(overlays.Markers) > 0) {
			return nil, fmt.Errorf("图表标记格式错误")
		}
		lineNames := map[string]bool{}
		for _, line := range overlays.Lines {
			if len(strings.TrimSpace(line.Name)) == 0 || len(line.Name) > 120 || lineNames[line.Name] || len(line.Points) > 600 {
				return nil, fmt.Errorf("均线格式错误")
			}
			lineNames[line.Name] = true
			seen := map[string]bool{}
			for _, point := range line.Points {
				if len(point) != 2 {
					return nil, fmt.Errorf("均线格式错误")
				}
				date, ok := point[0].(string)
				if !ok || !dates[date] || seen[date] {
					return nil, fmt.Errorf("均线日期不在图表范围内")
				}
				seen[date] = true
				if point[1] != nil {
					n, ok := point[1].(float64)
					if !ok || !finiteKlineNumber(n) {
						return nil, fmt.Errorf("均线数值错误")
					}
				}
			}
			if err := validatePythonSMA(line, allRows); err != nil {
				return nil, err
			}
		}
		groupNames := map[string]bool{}
		for i, group := range overlays.MarkerGroups {
			if len(strings.TrimSpace(group.Name)) == 0 || len(group.Name) > 120 || groupNames[group.Name] || lineNames[group.Name] || len(group.Dates) > 600 || (group.LineName != "" && !lineNames[group.LineName]) {
				return nil, errors.New("事件分组格式错误：名称须唯一，line_name须对应已有曲线，每组最多600个日期")
			}
			groupNames[group.Name] = true
			seen := map[string]bool{}
			for _, date := range group.Dates {
				if !dates[date] || seen[date] {
					return nil, errors.New("事件分组日期重复或不在图表范围内")
				}
				seen[date] = true
			}
			if group.Dates == nil {
				overlays.MarkerGroups[i].Dates = []string{}
			}
		}
		chart.MarkerGroups = overlays.MarkerGroups
		seen := map[string]bool{}
		for _, date := range overlays.Markers {
			if !dates[date] || seen[date] {
				return nil, fmt.Errorf("命中日期不在图表范围内")
			}
			seen[date] = true
		}
		chart.Lines, chart.Markers = overlays.Lines, overlays.Markers
		if chart.Lines == nil {
			chart.Lines = []PythonChartLine{}
		}
		if chart.Markers == nil {
			chart.Markers = []string{}
		}
	}
	return chart, nil
}

var pythonSMAName = regexp.MustCompile(`^(?:SMA|MA)[ _-]?([0-9]+)$`)

// Conventional MA<n>/SMA<n> names mean a simple average of canonical closes,
// including the current period. Keep prewarm periods for this independent
// check. This does not verify arbitrary indicators, events or table formulas.
func validatePythonSMA(line PythonChartLine, rows [][]any) error {
	match := pythonSMAName.FindStringSubmatch(strings.ToUpper(strings.TrimSpace(line.Name)))
	if match == nil {
		return nil
	}
	n, err := strconv.Atoi(match[1])
	if err != nil || n < 1 || n > 20000 {
		return fmt.Errorf("%s 的SMA周期须在1至20000之间", line.Name)
	}
	means := map[string]float64{}
	sum := 0.0
	for i, row := range rows {
		sum += row[4].(float64)
		if i >= n {
			sum -= rows[i-n][4].(float64)
		}
		if i >= n-1 {
			means[row[0].(string)] = sum / float64(n)
		}
	}
	for _, point := range line.Points {
		if point[1] == nil {
			continue
		}
		date, value := point[0].(string), point[1].(float64)
		want, ok := means[date]
		if !ok {
			return fmt.Errorf("%s 在%s没有足够预热数据，不能输出有效SMA值", line.Name, date)
		}
		// Permit rounding to four decimals, plus floating-point accumulation.
		if math.Abs(value-want) > 0.0000501+math.Abs(want)*1e-10 {
			return fmt.Errorf("%s 在%s数值%.9g与收盘SMA%d %.9g不符；比较可用close*n，图与表必须输出窗口和/n，至少保留4位小数后重新执行", line.Name, date, value, n, want)
		}
	}
	return nil
}
