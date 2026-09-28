package domain

import "time"

// AgentRequestBinding is a delivery index, never a Run execution state machine.
// Content, capabilities, signatures and credentials must not be stored here.
type AgentRequestBinding struct {
	ID               string  `gorm:"size:32;primaryKey"`
	SessionID        string  `gorm:"size:32;not null;uniqueIndex:uk_agent_request_client,priority:1"`
	ActorID          string  `gorm:"type:varbinary(64);not null;index:idx_agent_request_actor"`
	ClientRequestKey string  `gorm:"size:64;not null;uniqueIndex:uk_agent_request_client,priority:2"`
	RequestHash      string  `gorm:"size:80;not null"`
	EnvelopeDigest   string  `gorm:"size:80;not null"`
	ClaimsJSON       string  `gorm:"type:text;not null"`
	SigningKeyID     string  `gorm:"size:128;not null"`
	DeadlineMS       int64   `gorm:"not null"`
	MessageID        *string `gorm:"size:128"` // NULL until a matching durable acknowledgement.
	Revoked          bool    `gorm:"not null"`
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func (AgentRequestBinding) TableName() string { return "agent_request_binding" }
