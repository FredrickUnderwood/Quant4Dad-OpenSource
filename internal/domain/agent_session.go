package domain

import "time"

const (
	AgentSessionProvisioning = "provisioning"
	AgentSessionActive       = "active"
	AgentSessionArchived     = "archived"
	AgentSessionFailed       = "provisioning_failed"
)

// Product metadata only. Conversation and Run state remain in DSH JSONL.
// Binary actor columns and hashed client keys preserve exact identity equality.
// A NULL DSH ID permits multiple incomplete sagas under the unique index.
type AgentSessionBinding struct {
	ID                   string  `gorm:"size:32;primaryKey;index:idx_agent_session_actor_id,priority:2"`
	DSHSessionID         *string `gorm:"size:128;uniqueIndex:uk_agent_session_dsh"`
	ActorID              string  `gorm:"type:varbinary(64);not null;uniqueIndex:uk_agent_session_provision,priority:1;index:idx_agent_session_actor_id,priority:1"`
	ProvisionRequestKey  string  `gorm:"size:64;not null;uniqueIndex:uk_agent_session_provision,priority:2"` // SHA-256 of the client key.
	CreateRequestHash    string  `gorm:"size:80;not null"`                                                   // User fields, including original title, independent of revisions.
	ProvisionRequestHash string  `gorm:"size:80;not null"`                                                   // Exact Bridge creation fields.
	Status               string  `gorm:"size:32;not null;index:idx_agent_session_reconcile,priority:1"`
	Title                string  `gorm:"size:256;not null"`
	// Durable, coalescing title work. Conversation content remains in Runtime.
	TitleGeneration            uint64 `gorm:"not null;default:0"`
	TitleSettledGeneration     uint64 `gorm:"not null;default:0"`
	TitleAttempt               string `gorm:"size:32;not null;default:''"`
	TitleLeaseUntilMS          int64  `gorm:"not null;default:0"`
	Profile                    string `gorm:"size:32;not null"`
	Provider                   string `gorm:"size:128;not null"`
	Model                      string `gorm:"size:128;not null"`
	CreatedProfileRevision     string `gorm:"size:80;not null"`
	CreatedModelConfigRevision string `gorm:"size:32;not null"`
	RuntimeProvenanceJSON      string `gorm:"type:text;not null"`
	ProvisioningErrorCode      string `gorm:"size:64;not null"`
	ProvisionAttempt           string `gorm:"size:32;not null"`
	LeaseUntilMS               int64  `gorm:"not null"`
	RetryAtMS                  int64  `gorm:"not null;index:idx_agent_session_reconcile,priority:2"`
	ArchivedAt                 *time.Time
	CreatedAt                  time.Time
	UpdatedAt                  time.Time
}

func (AgentSessionBinding) TableName() string { return "agent_session_binding" }
