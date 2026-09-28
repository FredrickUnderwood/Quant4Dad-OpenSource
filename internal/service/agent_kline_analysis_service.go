package service

import (
	"context"
	"math"
	"math/big"
	"strconv"
	"time"

	"github.com/quant4dad/internal/domain"
)

type AnalyzeKlineInput struct {
	FileID       string   `json:"file_id"`
	StartDate    string   `json:"start_date"`
	EndDate      string   `json:"end_date"`
	ThresholdPct *float64 `json:"threshold_pct"`
	Comparison   string   `json:"comparison"`
	Metric       string   `json:"metric"`
}

type KlineAnalysisTable struct {
	Columns []string `json:"columns"`
	Rows    [][]any  `json:"rows"`
}
type KlineAnalysisChart struct {
	Kind string `json:"kind"`
	KlineAnalysisTable
}
type KlineAnalysis struct {
	Kind             string             `json:"kind"`
	FileID           string             `json:"file_id"`
	Code             string             `json:"code"`
	Period           string             `json:"period"`
	RequestedStart   string             `json:"requested_start"`
	RequestedEnd     string             `json:"requested_end"`
	FirstDate        string             `json:"first_date"`
	LastDate         string             `json:"last_date"`
	DataAsOf         string             `json:"data_as_of"`
	Truncated        bool               `json:"truncated"`
	CoverageVerified bool               `json:"coverage_verified"`
	PriceBasis       string             `json:"price_basis"`
	AdjustmentStatus string             `json:"adjustment_status"`
	VolumeUnit       string             `json:"volume_unit"`
	Metric           string             `json:"metric"`
	Comparison       string             `json:"comparison"`
	ThresholdPct     float64            `json:"threshold_pct"`
	BarCount         int                `json:"bar_count"`
	EligibleCount    int                `json:"eligible_count"`
	ExcludedCount    int                `json:"excluded_count"`
	MatchedCount     int                `json:"matched_count"`
	MatchRatePct     *float64           `json:"match_rate_pct"`
	Warnings         []string           `json:"warnings"`
	Matches          KlineAnalysisTable `json:"matches"`
	Chart            KlineAnalysisChart `json:"chart"`
}

// Analyze evaluates the entire bounded, owned snapshot once. Models cannot
// supply prices, formulas, rendering options, paths or computed event markers.
func (s *AgentKlineFileService) Analyze(ctx context.Context, in AnalyzeKlineInput) (KlineAnalysis, error) {
	var out KlineAnalysis
	threshold := 4.0
	if in.ThresholdPct != nil {
		threshold = *in.ThresholdPct
	}
	if in.Comparison == "" {
		in.Comparison = "gte"
	}
	if in.Metric == "" {
		in.Metric = "close_return"
	}
	if !finiteKlineNumber(threshold) || threshold < -100 || threshold > 1000 ||
		(in.Comparison != "gte" && in.Comparison != "gt" && in.Comparison != "lte" && in.Comparison != "lt") || (in.Metric != "close_return" && in.Metric != "high_return") {
		return out, ErrToolInput
	}
	file, err := s.load(ctx, in.FileID)
	if err != nil {
		return out, err
	}
	result := file.Result
	if in.StartDate == "" {
		in.StartDate = result.RequestedStart
	}
	if in.EndDate == "" {
		in.EndDate = result.RequestedEnd
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
	if !ValidInstrumentCode(result.Code) || (result.Period != "1d" && result.Period != "1w" && result.Period != "1mo") {
		return out, ErrToolUnavailable
	}
	out = KlineAnalysis{Kind: "kline_analysis_v1", FileID: in.FileID, Code: result.Code, Period: result.Period,
		RequestedStart: in.StartDate, RequestedEnd: in.EndDate, DataAsOf: result.DataAsOf, Truncated: result.Truncated,
		PriceBasis: "stored_ohlc", AdjustmentStatus: "unverified", VolumeUnit: "unspecified",
		Metric: in.Metric, Comparison: in.Comparison, ThresholdPct: threshold,
		Warnings: []string{"使用库中价格，本次未进行复权处理，也未使用官方除权参考前收价；adj_factor 为 1 不能证明没有除权除息。", "收益相对上一条可用 K 线收盘价；未核验交易日缺口，不能据此断言区间完整。", "成交量按库中原始值展示，单位未声明。"},
		Matches:  KlineAnalysisTable{Columns: []string{"date", "previous_date", "previous_close", "price", "return_pct"}, Rows: [][]any{}},
		Chart:    KlineAnalysisChart{Kind: "candlestick_v1", KlineAnalysisTable: KlineAnalysisTable{Columns: []string{"date", "open", "high", "low", "close", "volume"}, Rows: [][]any{}}}}
	if result.Truncated {
		out.Warnings = append(out.Warnings, "原始查询已截断到最新 500 根以内，本结果不能代表被截去的更早区间。")
	}
	previous := result.PreviousBar
	if previous != nil && !validAnalysisBar(previous) {
		return KlineAnalysis{}, ErrToolUnavailable
	}
	for _, bar := range result.Bars {
		if !validAnalysisBar(bar) || (previous != nil && bar.Date.Format("2006-01-02") <= previous.Date.Format("2006-01-02")) {
			return KlineAnalysis{}, ErrToolUnavailable
		}
		date := bar.Date.Format("2006-01-02")
		if (in.StartDate == "" || date >= in.StartDate) && (in.EndDate == "" || date <= in.EndDate) {
			if out.FirstDate == "" {
				out.FirstDate = date
			}
			out.LastDate = date
			out.BarCount++
			out.Chart.Rows = append(out.Chart.Rows, []any{date, bar.Open, bar.High, bar.Low, bar.Close, bar.Volume})
			if previous == nil {
				out.ExcludedCount++
			} else {
				price := bar.Close
				if in.Metric == "high_return" {
					price = bar.High
				}
				change := klineReturnPercent(price, previous.Close)
				pct, _ := change.Float64()
				if !finiteKlineNumber(pct) {
					return KlineAnalysis{}, ErrToolUnavailable
				}
				out.EligibleCount++
				cmp := change.Cmp(klineDecimal(threshold))
				matched := (in.Comparison == "gte" && cmp >= 0) || (in.Comparison == "gt" && cmp > 0) ||
					(in.Comparison == "lte" && cmp <= 0) || (in.Comparison == "lt" && cmp < 0)
				if matched {
					out.Matches.Rows = append(out.Matches.Rows, []any{date, previous.Date.Format("2006-01-02"), previous.Close, price, pct})
				}
			}
		}
		previous = bar
	}
	out.MatchedCount = len(out.Matches.Rows)
	if out.EligibleCount > 0 {
		rate := float64(out.MatchedCount) / float64(out.EligibleCount) * 100
		out.MatchRatePct = &rate
	}
	if out.ExcludedCount > 0 {
		out.Warnings = append(out.Warnings, "首根缺少此前收盘价，已从统计分母排除。")
	}
	if out.BarCount == 0 {
		out.Warnings = append(out.Warnings, "所选区间没有可用 K 线，不能解释为零涨跌。")
	}
	if in.StartDate != "" && out.FirstDate > in.StartDate {
		out.Warnings = append(out.Warnings, "实际首日晚于请求起日，可能有休市、停牌或数据缺失；区间覆盖未经核验。")
	}
	if out.BarCount > 0 && in.EndDate != "" && out.LastDate < in.EndDate {
		out.Warnings = append(out.Warnings, "实际末日早于请求截止日，不能视作截止日的最新行情；请核对交易日和数据覆盖。")
	}
	return out, nil
}

func finiteKlineNumber(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && math.Abs(v) <= 9007199254740991
}
func validAnalysisBar(bar *domain.Bar) bool {
	if bar == nil || bar.Date.IsZero() {
		return false
	}
	for _, price := range []float64{bar.Open, bar.High, bar.Low, bar.Close} {
		if !finiteKlineNumber(price) || price <= 0 {
			return false
		}
	}
	return finiteKlineNumber(bar.Volume) && bar.Volume >= 0 && bar.Low <= math.Min(bar.Open, bar.Close) && bar.High >= math.Max(bar.Open, bar.Close)
}
func klineDecimal(value float64) *big.Rat {
	r, _ := new(big.Rat).SetString(strconv.FormatFloat(value, 'f', -1, 64))
	return r
}

// Compare exact decimal prices before rounding display values; 104/100 is
// exactly 4%, so it matches >=4 and must not accidentally match >4.
func klineReturnPercent(price, previous float64) *big.Rat {
	p, prior := klineDecimal(price), klineDecimal(previous)
	return p.Mul(p.Quo(p.Sub(p, prior), prior), big.NewRat(100, 1))
}
