package domain

import "time"

// CoverageStatus grades how complete the data is for one instrument and period.
type CoverageStatus string

const (
	CoverageStatusComplete CoverageStatus = "complete" // has an up-to-date bar
	CoverageStatusStale    CoverageStatus = "stale"    // newest bar lags too far behind today
	CoverageStatusEmpty    CoverageStatus = "empty"    // no bars at all
)

// CoverageScanStatus reflects the state of the scan itself, so the UI can tell a
// static result apart from a run in progress.
type CoverageScanStatus string

const (
	CoverageScanIdle    CoverageScanStatus = "idle"
	CoverageScanRunning CoverageScanStatus = "running"
)

// DataCoverageSummary holds one row per period, written after each scheduled
// scan; this is the table the UI reads.
type DataCoverageSummary struct {
	ID               int64              `json:"id"                 gorm:"primaryKey;autoIncrement"`
	Period           BarPeriod          `json:"period"             gorm:"size:8;uniqueIndex"`
	TotalInstruments int                `json:"total_instruments"`
	CompleteCount    int                `json:"complete_count"`
	StaleCount       int                `json:"stale_count"`
	EmptyCount       int                `json:"empty_count"`
	LatestBarDate    *time.Time         `json:"latest_bar_date"`
	EarliestBarDate  *time.Time         `json:"earliest_bar_date"`
	ScannedAt        *time.Time         `json:"scanned_at"`
	ScanDurationMs   int64              `json:"scan_duration_ms"`
	ScanStatus       CoverageScanStatus `json:"scan_status"         gorm:"size:16"`
	LastError        string             `json:"last_error"          gorm:"type:text"`
	UpdatedAt        time.Time          `json:"updated_at"`
}

func (DataCoverageSummary) TableName() string { return "data_coverage_summary" }
