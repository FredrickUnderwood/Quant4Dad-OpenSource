package domain

import (
	"encoding/json"
	"time"
)

type BacktestStatus string

const (
	BacktestStatusPending BacktestStatus = "pending"
	BacktestStatusRunning BacktestStatus = "running"
	BacktestStatusSucceed BacktestStatus = "succeed"
	BacktestStatusFailed  BacktestStatus = "failed"
)

type BacktestJob struct {
	ID               int64          `json:"id"              gorm:"primaryKey;autoIncrement"`
	StrategyID       int64          `json:"strategy_id"     gorm:"index;not null"`
	CostID           int64          `json:"cost_id"         gorm:"index"`
	InitialCapital   float64        `json:"initial_capital" gorm:"not null"`
	StartDate        time.Time      `json:"start_date"`
	EndDate          time.Time      `json:"end_date"`
	Status           BacktestStatus `json:"status"          gorm:"size:16;index"`
	ErrorMsg         string         `json:"error_msg"       gorm:"type:text"`
	StartedAt        *time.Time     `json:"started_at"`
	FinishedAt       *time.Time     `json:"finished_at"`
	CreatedAt        time.Time      `json:"created_at"`
	UpdatedAt        time.Time      `json:"updated_at"`
	AgentRun         bool           `json:"agent_run,omitempty" gorm:"not null;default:false;index"`
	AgentSnapshot    string         `json:"-" gorm:"type:text"`
	AgentWorkerNonce string         `json:"-" gorm:"size:64"`
	AgentLeaseUntil  *time.Time     `json:"-" gorm:"index"`
}

type AgentBacktestSnapshot struct {
	Strategy Strategy `json:"strategy"`
	Cost     Cost     `json:"cost"`
}

func (BacktestJob) TableName() string { return "backtest_job" }

type BacktestResult struct {
	JobID            int64           `json:"job_id"            gorm:"primaryKey"`
	TotalReturn      float64         `json:"total_return"` // (final - initial) / initial
	AnnualizedReturn float64         `json:"annualized_return"`
	MaxDrawdown      float64         `json:"max_drawdown"`
	Sharpe           float64         `json:"sharpe"`
	WinRate          float64         `json:"win_rate"`
	TradeCount       int             `json:"trade_count"`
	RuleStats        json.RawMessage `json:"rule_stats"        gorm:"type:text"` // {ruleName: {trigger_count, win_count, total_pnl}}
	CreatedAt        time.Time       `json:"created_at"`
}

func (BacktestResult) TableName() string { return "backtest_result" }

type TradeSide string

const (
	TradeSideBuy  TradeSide = "buy"
	TradeSideSell TradeSide = "sell"
)

type Trade struct {
	ID            int64     `json:"id"               gorm:"primaryKey;autoIncrement"`
	JobID         int64     `json:"job_id"           gorm:"index;not null"`
	Code          string    `json:"code"             gorm:"size:32;index"`
	Side          TradeSide `json:"side"             gorm:"size:8"`
	Qty           int       `json:"qty"`
	Price         float64   `json:"price"`
	Notional      float64   `json:"notional"`
	Commission    float64   `json:"commission"`
	StampDuty     float64   `json:"stamp_duty"`
	Time          time.Time `json:"time"`
	TriggeredRule string    `json:"triggered_rule"   gorm:"size:128"`
	RealizedPnL   float64   `json:"realized_pnl"` // for sell trades
}

func (Trade) TableName() string { return "trade" }

type EquityPoint struct {
	JobID        int64     `json:"job_id"        gorm:"primaryKey"`
	Date         time.Time `json:"date"          gorm:"primaryKey"`
	TotalValue   float64   `json:"total_value"`
	Cash         float64   `json:"cash"`
	HoldingValue float64   `json:"holding_value"`
	Drawdown     float64   `json:"drawdown"`
}

func (EquityPoint) TableName() string { return "equity_point" }
