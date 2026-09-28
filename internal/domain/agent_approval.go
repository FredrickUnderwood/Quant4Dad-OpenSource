package domain

import "time"

// Approval stores the decision and a nonce, never a bearer receipt signature.
// The opaque receipt is regenerated only by the trusted Go signer.
type AgentApprovalReceipt struct {
	ID             string    `json:"id" gorm:"size:64;primaryKey"`
	AuditID        string    `json:"-" gorm:"size:64;not null;uniqueIndex"`
	ActorID        string    `json:"-" gorm:"type:varbinary(64);not null;index"`
	SessionID      string    `json:"session_id" gorm:"size:32;not null;index"`
	RunID          string    `json:"run_id" gorm:"size:32;not null;index"`
	ToolCallID     string    `json:"tool_call_id" gorm:"size:26;not null"`
	ToolName       string    `json:"tool_name" gorm:"size:128;not null"`
	ArgsHash       string    `json:"arguments_hash" gorm:"size:80;not null"`
	EnvelopeDigest string    `json:"-" gorm:"size:80;not null"`
	Risk           string    `json:"risk" gorm:"size:4;not null"`
	Status         string    `json:"status" gorm:"size:24;not null;index"`
	ReceiptNonce   *string   `json:"-" gorm:"size:64;uniqueIndex"`
	ExpiresAt      time.Time `json:"expires_at" gorm:"not null;index"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func (AgentApprovalReceipt) TableName() string { return "agent_approval_receipt" }

// AgentToolEffect is the business idempotency record, committed atomically
// with a product mutation. Its result contains only bounded resource IDs and
// versions, so a crash before artifact publication can be reconciled safely.
type AgentToolEffect struct {
	AuditID    string `gorm:"size:64;primaryKey"`
	ArgsHash   string `gorm:"size:80;not null"`
	ResultJSON string `gorm:"type:text;not null"`
	CreatedAt  time.Time
}

func (AgentToolEffect) TableName() string { return "agent_tool_effect" }

type AgentMutation struct {
	Kind     string
	Strategy *Strategy
	Pipeline *Pipeline
	Backtest *BacktestJob
}
type AgentMutationResult struct {
	ID      int64  `json:"id,omitempty"`
	Version int    `json:"version,omitempty"`
	Status  string `json:"status,omitempty"`
	JobID   int64  `json:"job_id,omitempty"`
}
