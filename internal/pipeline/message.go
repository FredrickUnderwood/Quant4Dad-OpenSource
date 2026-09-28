// Package pipeline implements the event pipeline's execution engine: a common message
// envelope, the node processor interface, the node registry, and a synchronous chained
// executor. The engine itself depends on neither storage nor HTTP, which keeps it easy to
// unit-test and reuse; the application layer persists the results.
package pipeline

import (
	"encoding/json"
	"time"
)

// Action is the routing decision a node returns after processing.
type Action int

const (
	ActionPass Action = iota // continue to the next node
	ActionDrop               // discard the event and stop the nodes downstream
)

func (a Action) String() string {
	if a == ActionDrop {
		return "drop"
	}
	return "pass"
}

// Message is the common envelope threaded through every node. Payload carries the
// original fields plus whatever each node accumulates or writes back; Meta carries
// metadata such as the source and timestamps.
type Message struct {
	ID      string         `json:"id"`
	Payload map[string]any `json:"payload"`
	Meta    map[string]any `json:"meta"`
}

// AIInvocation records one model call made by an AI node. RunContext collects them and
// the application layer turns them into domain.AIResult rows.
type AIInvocation struct {
	NodeKey          string
	Provider         string
	Model            string
	PromptSnapshot   string
	Output           json.RawMessage
	TokensPrompt     int
	TokensCompletion int
	LatencyMS        int64
}

// RunContext is the context for one execution of one message. Through it a Processor
// reaches the message, learns which node it is, tells whether this is a dry run, and
// records AI call results.
type RunContext struct {
	Msg     *Message
	NodeKey string
	DryRun  bool

	aiResults       []AIInvocation
	nodeNote        string
	deliveryPreview *DeliveryPreview
}

// DeliveryPreview is rendered without resolving channel credentials or sending.
type DeliveryPreview struct {
	Channel               string   `json:"channel"`
	Recipients            []string `json:"recipients"`
	UsesDefaultRecipients bool     `json:"uses_default_recipients"`
	Title                 string   `json:"title"`
	Body                  string   `json:"body"`
}

func (rc *RunContext) PreviewDelivery(preview DeliveryPreview) { rc.deliveryPreview = &preview }

// Note records a non-fatal remark about the current node — typically the failure reason
// when on_error=continue lets the message through. The executor writes it into that
// node's NodeTrace.Error (the action stays pass), so it is visible in the lineage rather
// than silently swallowed. The executor clears it before each node runs.
func (rc *RunContext) Note(msg string) { rc.nodeNote = msg }

// RecordAI is called by an AI node after a model call, registering the result for later
// persistence.
func (rc *RunContext) RecordAI(inv AIInvocation) {
	inv.NodeKey = rc.NodeKey
	rc.aiResults = append(rc.aiResults, inv)
}

// AIResults returns every AI call recorded during this execution.
func (rc *RunContext) AIResults() []AIInvocation { return rc.aiResults }

// NodeTrace records one node's execution lineage.
type NodeTrace struct {
	NodeKey   string `json:"node_key"`
	NodeType  string `json:"node_type"`
	Action    string `json:"action"`
	LatencyMS int64  `json:"latency_ms"`
	Error     string `json:"error"`
	// Output is populated only on a dry run, so the UI can inspect each node's effect.
	Output          map[string]any   `json:"output,omitempty"`
	DeliveryPreview *DeliveryPreview `json:"delivery_preview,omitempty"`
}

// ExecResult is the summary of one message's trip through the whole pipeline.
type ExecResult struct {
	Status        string         // passed / dropped / failed
	DroppedAtNode string         // key of the node that dropped it
	FailedAtNode  string         // key of the node that failed
	Err           error          // the failure reason
	Traces        []NodeTrace    // per-node lineage
	AIResults     []AIInvocation // AI call records
	FinalPayload  map[string]any // the final payload
}

const (
	statusPassed  = "passed"
	statusDropped = "dropped"
	statusFailed  = "failed"
)

func nowMillis(start time.Time) int64 { return time.Since(start).Milliseconds() }
