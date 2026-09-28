package domain

import "time"

type DataSyncMode string

const (
	DataSyncModeFull        DataSyncMode = "full"
	DataSyncModeIncremental DataSyncMode = "incremental"
)

type DataSyncStatus string

const (
	DataSyncStatusPending DataSyncStatus = "pending"
	DataSyncStatusRunning DataSyncStatus = "running"
	DataSyncStatusSucceed DataSyncStatus = "succeed"
	DataSyncStatusFailed  DataSyncStatus = "failed"
)

type DataSyncTask struct {
	ID         int64          `json:"id"          gorm:"primaryKey;autoIncrement"`
	Mode       DataSyncMode   `json:"mode"        gorm:"size:16"`
	Period     BarPeriod      `json:"period"      gorm:"size:8"`
	Codes      StringSlice    `json:"codes"       gorm:"type:text"` // empty = all instruments
	StartDate  time.Time      `json:"start_date"`
	EndDate    time.Time      `json:"end_date"`
	Status     DataSyncStatus `json:"status"      gorm:"size:16;index"`
	Total      int            `json:"total"`
	Done       int            `json:"done"`
	Failed     int            `json:"failed"`
	ErrorMsg   string         `json:"error_msg"   gorm:"type:text"`
	StartedAt  *time.Time     `json:"started_at"`
	FinishedAt *time.Time     `json:"finished_at"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
}

func (DataSyncTask) TableName() string { return "data_sync_task" }

// DataSyncFailure captures a single per-code failure during a sync run, keeping
// the raw provider response so failed cases can be analysed from the frontend.
type DataSyncFailure struct {
	ID         int64     `json:"id"          gorm:"primaryKey;autoIncrement"`
	TaskID     int64     `json:"task_id"     gorm:"index"`
	Code       string    `json:"code"        gorm:"size:32"`
	Reason     string    `json:"reason"      gorm:"type:text"` // error message
	HTTPStatus int       `json:"http_status"`                  // 0 when the request never completed
	Response   string    `json:"response"    gorm:"type:text"` // raw response body (truncated)
	CreatedAt  time.Time `json:"created_at"`
}

func (DataSyncFailure) TableName() string { return "data_sync_failure" }
