package domain

import "time"

type BarPeriod string

const (
	Bar1d  BarPeriod = "1d"
	Bar1w  BarPeriod = "1w"
	Bar1mo BarPeriod = "1mo"
	// Only a named, validated provider response establishes factor provenance.
	AdjFactorTushareFund = "tushare.fund_adj"
)

// Bar is one OHLCV candle. Uniquely keyed by (code, period, date).
type Bar struct {
	Code      string    `json:"code"       gorm:"primaryKey;size:32"`
	Period    BarPeriod `json:"period"     gorm:"primaryKey;size:8"`
	Date      time.Time `json:"date"       gorm:"primaryKey;index"`
	Open      float64   `json:"open"`
	High      float64   `json:"high"`
	Low       float64   `json:"low"`
	Close     float64   `json:"close"`
	Volume    float64   `json:"volume"`
	Amount    float64   `json:"amount"`
	AdjFactor float64   `json:"adj_factor"`
	AdjSource string    `json:"adj_source,omitempty" gorm:"size:32;not null;default:''"`
}

func (Bar) TableName() string { return "bar" }
