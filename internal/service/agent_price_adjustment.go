package service

import (
	"errors"

	"github.com/quant4dad/internal/domain"
)

type PythonAdjustment struct {
	Code         string  `json:"code"`
	Source       string  `json:"source"`
	AnchorDate   string  `json:"anchor_date"`
	AnchorFactor float64 `json:"anchor_factor"`
}

// Verified means every supplied daily factor has validated provider provenance,
// not that the trading calendar or the provider's corporate actions were audited.
func factorProvenance(period string, bars []*domain.Bar) (string, string) {
	if period != "1d" || len(bars) == 0 {
		return "unverified", ""
	}
	for _, b := range bars {
		if b == nil || b.AdjSource != domain.AdjFactorTushareFund || !finiteKlineNumber(b.AdjFactor) || b.AdjFactor <= 0 {
			return "unverified", ""
		}
	}
	return "verified", domain.AdjFactorTushareFund
}

// Apply to a copy of raw DAILY OHLC before Python and before chart aggregation.
// The caller supplies ascending bars. A fixed end_date prevents later dividends
// or future input rows from changing a historical request's anchor.
func forwardAdjustedBars(code, period, end string, bars []*domain.Bar) ([]*domain.Bar, PythonAdjustment, error) {
	var metadata PythonAdjustment
	if end == "" {
		return nil, metadata, errors.New("前复权需要明确 end_date 作为基准截止日")
	}
	eligible := bars[:0:0]
	for _, bar := range bars {
		if bar.Date.Format("2006-01-02") <= end {
			eligible = append(eligible, bar)
		}
	}
	status, source := factorProvenance(period, eligible)
	if status != "verified" {
		return nil, metadata, errors.New("该区间缺少可信日线因子；需完成对应资产的因子接入和同步后重新 query_kline，不能用旧文件或固定值1代替。当前仅接入ETF的fund_adj，股票因子尚未接入")
	}
	anchor := eligible[len(eligible)-1]
	metadata = PythonAdjustment{Code: code, Source: source, AnchorDate: anchor.Date.Format("2006-01-02"), AnchorFactor: anchor.AdjFactor}
	adjusted := make([]*domain.Bar, 0, len(eligible))
	for _, bar := range eligible {
		b := *bar
		ratio := b.AdjFactor / anchor.AdjFactor
		b.Open *= ratio
		b.High *= ratio
		b.Low *= ratio
		b.Close *= ratio
		if !validAnalysisBar(&b) {
			return nil, PythonAdjustment{}, errors.New("前复权计算产生无效价格，未执行脚本")
		}
		adjusted = append(adjusted, &b)
	}
	return adjusted, metadata, nil
}
