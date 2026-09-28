package domain

import "time"

type AssetType string

const (
	AssetStock AssetType = "stock"
	AssetETF   AssetType = "etf"
)

// Instrument represents a stock or an exchange-traded fund. ETF metadata is
// kept beside the common identity so existing search and bar queries can use it.
type Instrument struct {
	Code          string     `json:"code"        gorm:"primaryKey;size:32"` // sh.600519
	Name          string     `json:"name"        gorm:"size:64;not null"`
	Industry      string     `json:"industry"    gorm:"size:64"`
	ListedDate    *time.Time `json:"listed_date"`
	Status        string     `json:"status"      gorm:"size:16;default:active"` // active / delisted / pending
	AssetType     AssetType  `json:"asset_type"  gorm:"size:16;not null;default:stock"`
	Exchange      string     `json:"exchange,omitempty" gorm:"size:8"`
	FullName      string     `json:"full_name,omitempty" gorm:"size:255"`
	IndexCode     string     `json:"index_code,omitempty" gorm:"size:32"`
	IndexName     string     `json:"index_name,omitempty" gorm:"size:255"`
	Manager       string     `json:"manager,omitempty" gorm:"size:128"`
	Custodian     string     `json:"custodian,omitempty" gorm:"size:128"`
	ETFType       string     `json:"etf_type,omitempty" gorm:"size:32"` // domestic / QDII, as supplied by Tushare
	ManagementFee *float64   `json:"management_fee,omitempty"`
	SetupDate     *time.Time `json:"setup_date,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

func (Instrument) TableName() string { return "instrument" }
