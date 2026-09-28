package domain

import "time"

// Cost is a reusable fee model. Picked per BacktestJob.
type Cost struct {
	ID             int64     `json:"id"               gorm:"primaryKey;autoIncrement"`
	Name           string    `json:"name"             gorm:"uniqueIndex;size:64;not null"`
	IsDefault      bool      `json:"is_default"       gorm:"index"`
	CommissionRate float64   `json:"commission_rate"`
	MinCommission  float64   `json:"min_commission"`
	StampDutyRate  float64   `json:"stamp_duty_rate"` // sell-side only
	SlippageBps    float64   `json:"slippage_bps"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func (Cost) TableName() string { return "cost" }

// DefaultAShareCost returns the seed entry used when the table is empty.
func DefaultAShareCost() *Cost {
	return &Cost{
		Name:           "A股通用",
		IsDefault:      true,
		CommissionRate: 0.0003,
		MinCommission:  5,
		StampDutyRate:  0.001,
		SlippageBps:    5,
	}
}
