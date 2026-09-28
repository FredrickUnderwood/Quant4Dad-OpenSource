// Package agentbridge implements the Q4D Bridge v1 wire client. Product state,
// authorization, provisioning reconciliation and reconnect policy belong to callers.
package agentbridge

type SessionCreate struct {
	SessionID            string `json:"q4d_session_id"`
	Provider             string `json:"provider"`
	Model                string `json:"model"`
	Profile              string `json:"profile"`
	ProfileRevision      string `json:"profile_revision"`
	ModelConfigRevision  string `json:"model_config_revision"`
	ProvisionRequestHash string `json:"provision_request_hash"`
}

type Provenance struct {
	BridgeProtocol       int    `json:"bridge_protocol"`
	AdapterVersion       string `json:"adapter_version"`
	Backend              string `json:"backend"`
	DSHVersion           string `json:"dsh_version"`
	SessionFormat        int    `json:"session_format"`
	EventJournalFormat   int    `json:"event_journal_format"`
	SessionBindingFormat int    `json:"session_binding_format"`
}

type SessionCreated struct {
	SessionID                  string     `json:"session_id"`
	DSHSessionID               string     `json:"dsh_session_id"`
	Durable                    bool       `json:"durable"`
	Provider                   string     `json:"provider"`
	Model                      string     `json:"model"`
	Profile                    string     `json:"profile"`
	CreatedProfileRevision     string     `json:"created_profile_revision"`
	CreatedModelConfigRevision string     `json:"created_model_config_revision"`
	ProvisionRequestHash       string     `json:"provision_request_hash"`
	RuntimeProvenance          Provenance `json:"runtime_provenance"`
}

type SessionClosed struct {
	SessionID    string `json:"session_id"`
	DSHSessionID string `json:"dsh_session_id"`
	Durable      bool   `json:"durable"`
	Loaded       bool   `json:"loaded"`
}

type Features struct {
	SessionResume       bool `json:"session_resume"`
	EventReplay         bool `json:"event_replay"`
	Cancel              bool `json:"cancel"`
	Approval            bool `json:"approval"`
	RawProviderDelta    bool `json:"raw_provider_delta"`
	CompactionEvents    bool `json:"compaction_events"`
	Fork                bool `json:"fork"`
	SessionProvisioning bool `json:"session_provisioning,omitempty"`
}

type Capabilities struct {
	BridgeProtocol       int               `json:"bridge_protocol"`
	AdapterVersion       string            `json:"adapter_version"`
	Backend              string            `json:"backend"`
	DSHVersion           string            `json:"dsh_version"`
	SessionFormat        int               `json:"session_format"`
	EventJournalFormat   int               `json:"event_journal_format"`
	SessionBindingFormat *int              `json:"session_binding_format,omitempty"`
	Features             Features          `json:"features"`
	ModelConfigRevision  string            `json:"model_config_revision"`
	RuntimeManifest      *RuntimeManifest  `json:"runtime_manifest,omitempty"`
	ReadyModels          *[]ReadyModel     `json:"ready_models,omitempty"`
	ProfileSource        string            `json:"profile_source,omitempty"`
	ProfilesAligned      *bool             `json:"profiles_aligned,omitempty"`
	Profiles             []ProfileIdentity `json:"profiles,omitempty"`
}
type ProfileIdentity struct {
	ID                  string `json:"id"`
	Revision            string `json:"revision"`
	PromptBundleDigest  string `json:"promptBundleDigest"`
	SkillsDigest        string `json:"skillsDigest"`
	ToolCatalogRevision string `json:"toolCatalogRevision"`
}
type RuntimeManifest struct {
	Q4DVersion          string `json:"q4d_version"`
	AgentImageDigest    string `json:"agent_image_digest"`
	AgentRuntimeVersion string `json:"agent_runtime_version"`
	AdapterVersion      string `json:"adapter_version"`
	DSHVersion          string `json:"dsh_version"`
	BridgeProtocol      int    `json:"bridge_protocol"`
}
type ReadyModel struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// CheckCompatibility explicitly gates the current v1 lifecycle/replay surface.
// Backend/artifact versions must additionally be checked by deployment readiness.
func (c Capabilities) CheckCompatibility() error {
	if c.BridgeProtocol != 1 || c.SessionFormat != 0 || c.EventJournalFormat != 1 ||
		c.SessionBindingFormat == nil || *c.SessionBindingFormat != 1 ||
		!c.Features.SessionProvisioning || !c.Features.SessionResume || !c.Features.EventReplay ||
		!c.Features.Cancel || !c.Features.Approval {
		return ErrIncompatible
	}
	return nil
}

type TextContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// PromptRequest contains an ephemeral credential. Never log/serialize it to a
// product journal. Its request hash and envelope digest are supplied by the BFF.
type PromptRequest struct {
	SessionID               string        `json:"q4d_session_id"`
	RunID                   string        `json:"run_id"`
	ClientRequestID         string        `json:"client_request_id"`
	RequestHash             string        `json:"request_hash"`
	RunCapability           string        `json:"run_capability"`
	ExecutionEnvelopeDigest string        `json:"execution_envelope_digest"`
	Content                 []TextContent `json:"content"`
}

type PromptAccepted struct {
	RunID     string `json:"run_id"`
	MessageID string `json:"message_id"`
	Durable   bool   `json:"durable"`
	State     string `json:"state"`
}

type Run struct {
	SessionID               string `json:"session_id"`
	RunID                   string `json:"run_id"`
	MessageID               string `json:"message_id"`
	ExecutionEnvelopeDigest string `json:"execution_envelope_digest"`
	Durable                 bool   `json:"durable"`
	State                   string `json:"state"`
	Terminal                bool   `json:"terminal"`
	LastEventID             string `json:"last_event_id"`
}

type ApprovalDecision struct {
	RunID           string `json:"run_id"`
	ToolCallID      string `json:"tool_call_id"`
	Decision        string `json:"decision"`
	ApprovalReceipt string `json:"approval_receipt,omitempty"`
}

type ApprovalAcknowledged struct {
	RunID      string `json:"run_id"`
	ToolCallID string `json:"tool_call_id"`
	Decision   string `json:"decision"`
}

type TranscriptQuery struct {
	BeforeSeq   string
	SnapshotSeq string
	Limit       int // Zero selects the server default (100).
}

// Validate lets product handlers reject invalid cursors even when a Session is
// still provisioning and no Runtime transcript request will be sent.
func (q TranscriptQuery) Validate() error {
	_, err := queryString(q)
	return err
}

type TranscriptPage struct {
	SessionID     string           `json:"session_id"`
	SnapshotSeq   string           `json:"snapshot_seq"`
	Items         []TranscriptItem `json:"items"`
	HasMore       bool             `json:"has_more"`
	NextBeforeSeq *string          `json:"next_before_seq" bridge:"nullable"`
}

type TranscriptItem struct {
	Seq            string              `json:"seq"`
	MessageID      string              `json:"message_id"`
	RunID          string              `json:"run_id"`
	Role           string              `json:"role"`
	OccurredAt     string              `json:"occurred_at"`
	Content        []TranscriptContent `json:"content"`
	OmittedContent bool                `json:"omitted_content"`
}

// TranscriptContent is archival display data. A tool_request is never authority
// to execute a Tool. Pointers distinguish absent fields from empty text/false.
type TranscriptContent struct {
	Type      string         `json:"type"`
	Text      *string        `json:"text,omitempty"`
	Name      *string        `json:"name,omitempty"`
	Arguments *string        `json:"arguments,omitempty"`
	IsError   *bool          `json:"is_error,omitempty"`
	Content   *[]TextContent `json:"content,omitempty"`
}

// Event.Data is one of the payload value types below (or EmptyData).
// Unknown envelope extensions are discarded; unknown data fields are rejected.
type Event struct {
	ID            string `json:"id"`
	RunID         string `json:"run_id"`
	SessionID     string `json:"session_id"`
	Type          string `json:"type"`
	OccurredAt    string `json:"occurred_at"`
	SchemaVersion int    `json:"schema_version"`
	Data          any    `json:"data"`
}

func (e Event) Terminal() bool { return terminalState(e.Type) }

type EmptyData struct{}
type RunStartedData struct {
	MessageID               string `json:"message_id"`
	ExecutionEnvelopeDigest string `json:"execution_envelope_digest"`
}
type MessageData struct {
	MessageID string `json:"message_id"`
	Text      string `json:"text"`
}
type ToolProposedData struct {
	ToolCallID       string         `json:"tool_call_id"`
	Name             string         `json:"name"`
	Arguments        map[string]any `json:"arguments"`
	IdempotencyKey   string         `json:"idempotency_key"`
	SourceSeq        string         `json:"source_seq"`
	ArgumentsOmitted *bool          `json:"arguments_omitted,omitempty"`
}
type ApprovalRequiredData struct {
	ToolCallID    string `json:"tool_call_id"`
	Name          string `json:"name"`
	ApprovalID    string `json:"approval_id"`
	ArgumentsHash string `json:"arguments_hash"`
	Risk          string `json:"risk"`
	ExpiresAt     string `json:"expires_at"`
}
type ToolData struct {
	ToolCallID string `json:"tool_call_id"`
}
type ToolFailedData struct {
	ToolCallID string `json:"tool_call_id"`
	Code       string `json:"code"`
}
type Usage struct {
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	TotalTokens      *int64 `json:"total_tokens,omitempty"`
	CacheReadTokens  *int64 `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens *int64 `json:"cache_write_tokens,omitempty"`
	ReasoningTokens  *int64 `json:"reasoning_tokens,omitempty"`
}
type UsageData struct {
	MessageID string `json:"message_id"`
	Usage     Usage  `json:"usage"`
}

type RunProgressData struct {
	Stage string `json:"stage"`
}
type CompactedData struct {
	SourceSeq string `json:"source_seq"`
}
type RunBudgetDetail struct {
	Dimension string `json:"dimension"`
	Used      int64  `json:"used"`
	Limit     int64  `json:"limit"`
	Requested int64  `json:"requested"`
}
type RunFailedData struct {
	Budget    *RunBudgetDetail `json:"budget,omitempty"`
	Code      string           `json:"code"`
	Retryable bool             `json:"retryable"`
}
type RunCancelledData struct {
	Reason string `json:"reason"`
}
