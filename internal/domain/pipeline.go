package domain

import (
	"encoding/json"
	"time"
)

// Pipeline status values.
const (
	PipelineStatusDraft    = "draft"
	PipelineStatusEnabled  = "enabled"
	PipelineStatusDisabled = "disabled"
)

// Built-in node type identifiers. To add a node type, register a Processor with
// the pipeline engine and add a constant here — the set is open for extension.
const (
	NodeTypeKeywordFilter = "keyword_filter"
	NodeTypeAIAnalysis    = "ai_analysis"
)

// Pipeline is one user-composed event processing pipeline. Nodes and edges live in
// PipelineNode / PipelineEdge; edges are expressed as from/to, which leaves room
// for the DAG upgrade in phase two (phase one collapses the topological order into
// a linear chain).
type Pipeline struct {
	ID          int64     `json:"id"          gorm:"primaryKey;autoIncrement"`
	Name        string    `json:"name"        gorm:"size:128;not null"`
	Description string    `json:"description" gorm:"type:text"`
	Status      string    `json:"status"      gorm:"size:16;not null;default:draft"`
	Version     int       `json:"version"     gorm:"not null;default:1"`
	CreatedBy   string    `json:"created_by"  gorm:"size:64"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`

	// Associated data, populated only when loading or returning; not stored in
	// this table.
	Nodes []PipelineNode `json:"nodes" gorm:"-"`
	Edges []PipelineEdge `json:"edges" gorm:"-"`
	// Sources lists the news sources this pipeline subscribes to (see
	// NewsSubscription); arriving news is fed into this pipeline as an event
	// automatically. Populated only when loading or returning.
	Sources []string `json:"sources" gorm:"-"`
}

func (Pipeline) TableName() string { return "pipeline" }

// PipelineNode is one processing node on the canvas. Config is typed JSON,
// interpreted by that node's own Processor; its schema is served to the UI through
// the node-types endpoint to render the form.
type PipelineNode struct {
	ID         int64           `json:"id"          gorm:"primaryKey;autoIncrement"`
	PipelineID int64           `json:"pipeline_id" gorm:"index;not null"`
	NodeKey    string          `json:"node_key"    gorm:"size:64;not null"` // unique within the canvas
	Type       string          `json:"type"        gorm:"size:64;not null"`
	Name       string          `json:"name"        gorm:"size:128"`
	Config     json.RawMessage `json:"config"      gorm:"type:text"`
	PosX       int             `json:"pos_x"`
	PosY       int             `json:"pos_y"`
}

func (PipelineNode) TableName() string { return "pipeline_node" }

// PipelineEdge is a directed edge between nodes. In a linear chain the edges form
// one path; in a DAG they can branch. Condition is an optional routing condition
// (see the JSON form of pipeline.EdgeCondition): when non-empty, the edge only
// activates if the upstream node's output satisfies it; empty means unconditional.
type PipelineEdge struct {
	ID          int64           `json:"id"            gorm:"primaryKey;autoIncrement"`
	PipelineID  int64           `json:"pipeline_id"   gorm:"index;not null"`
	FromNodeKey string          `json:"from_node_key" gorm:"size:64;not null"`
	ToNodeKey   string          `json:"to_node_key"   gorm:"size:64;not null"`
	Condition   json.RawMessage `json:"condition,omitempty" gorm:"type:text"`
}

func (PipelineEdge) TableName() string { return "pipeline_edge" }
