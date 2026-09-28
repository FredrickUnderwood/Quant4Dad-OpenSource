package domain

import (
	"encoding/json"
	"time"
)

// Event status values, mirroring an event's lifecycle inside a pipeline.
const (
	EventStatusProcessing = "processing"
	EventStatusPassed     = "passed"  // flowed through every node
	EventStatusDropped    = "dropped" // discarded by some filter node
	EventStatusFailed     = "failed"  // a node errored and its policy is fail
)

// Node execution actions, persisted in EventTrace.Action.
const (
	TraceActionPass = "pass"
	TraceActionDrop = "drop"
)

// Event is one event flowing into a pipeline. RawPayload is the body as received;
// FinalPayload is the body after the node chain has run (including fields written
// back by AI nodes). The table is named pipeline_event to avoid the MySQL reserved
// word EVENT.
type Event struct {
	ID            int64           `json:"id"              gorm:"primaryKey;autoIncrement"`
	EventUID      string          `json:"event_uid"       gorm:"size:64;uniqueIndex;not null"`
	PipelineID    int64           `json:"pipeline_id"     gorm:"index:idx_pipeline_status;not null"`
	Source        string          `json:"source"          gorm:"size:64"`
	RawPayload    json.RawMessage `json:"raw_payload"     gorm:"type:text"`
	FinalPayload  json.RawMessage `json:"final_payload"   gorm:"type:text"`
	Status        string          `json:"status"          gorm:"size:16;index:idx_pipeline_status;not null;default:processing"`
	DroppedAtNode string          `json:"dropped_at_node" gorm:"size:64"`
	Error         string          `json:"error"           gorm:"size:512"`
	ReceivedAt    time.Time       `json:"received_at"`
	FinishedAt    *time.Time      `json:"finished_at"`

	// Associated data, populated only when fetching details.
	Traces    []EventTrace `json:"traces"     gorm:"-"`
	AIResults []AIResult   `json:"ai_results" gorm:"-"`
}

func (Event) TableName() string { return "pipeline_event" }

// EventTrace records an event's lineage through each node — action, latency,
// error — so you can trace which node dropped or failed a given event.
type EventTrace struct {
	ID        int64     `json:"id"         gorm:"primaryKey;autoIncrement"`
	EventID   int64     `json:"event_id"   gorm:"index;not null"`
	NodeKey   string    `json:"node_key"   gorm:"size:64;not null"`
	NodeType  string    `json:"node_type"  gorm:"size:64"`
	Action    string    `json:"action"     gorm:"size:16"`
	LatencyMS int64     `json:"latency_ms"`
	Error     string    `json:"error"      gorm:"size:512"`
	CreatedAt time.Time `json:"created_at"`
}

func (EventTrace) TableName() string { return "pipeline_event_trace" }

// AIResult persists the outcome of each AI analysis node call. PromptSnapshot and
// Model are stored redundantly so historical results stay reproducible and
// comparable after a node's prompt is revised — essential when backtesting
// quantitative signals.
type AIResult struct {
	ID               int64           `json:"id"                gorm:"primaryKey;autoIncrement"`
	EventID          int64           `json:"event_id"          gorm:"index:idx_event_node;not null"`
	NodeKey          string          `json:"node_key"          gorm:"index:idx_event_node;size:64;not null"`
	Provider         string          `json:"provider"          gorm:"size:32"`
	Model            string          `json:"model"             gorm:"size:64"`
	PromptSnapshot   string          `json:"prompt_snapshot"   gorm:"type:text"`
	Output           json.RawMessage `json:"output"            gorm:"type:text"`
	TokensPrompt     int             `json:"tokens_prompt"`
	TokensCompletion int             `json:"tokens_completion"`
	LatencyMS        int64           `json:"latency_ms"`
	CreatedAt        time.Time       `json:"created_at"`
}

func (AIResult) TableName() string { return "pipeline_ai_result" }
