package domain

import "time"

// AgentToolAudit is the execution ledger, not a copy of arguments or results.
// Full bounded responses reside in private artifacts referenced by digest.
type AgentToolAudit struct {
	ID             string `gorm:"size:64;primaryKey"`
	ActorID        string `gorm:"type:varbinary(64);not null;uniqueIndex:uk_agent_tool_key,priority:1"`
	SessionID      string `gorm:"size:32;not null;index:idx_agent_tool_session"`
	RunID          string `gorm:"size:32;not null;uniqueIndex:uk_agent_tool_call,priority:1"`
	ToolCallID     string `gorm:"size:26;not null;uniqueIndex:uk_agent_tool_call,priority:2"`
	ToolName       string `gorm:"size:128;not null;uniqueIndex:uk_agent_tool_key,priority:2"`
	IdempotencyKey string `gorm:"size:64;not null;uniqueIndex:uk_agent_tool_key,priority:3"`
	ArgsHash       string `gorm:"size:80;not null"`
	EnvelopeDigest string `gorm:"size:80;not null"`
	Risk           string `gorm:"size:4;not null"`
	Status         string `gorm:"size:24;not null;index:idx_agent_tool_status"`
	Started        bool   `gorm:"not null"`
	ResultRef      string `gorm:"size:128;not null"`
	ErrorCode      string `gorm:"size:64;not null"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
	FinishedAt     *time.Time
}

func (AgentToolAudit) TableName() string { return "agent_tool_audit" }
