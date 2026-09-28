package agentbridge

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bytedance/sonic"
	"github.com/bytedance/sonic/ast"
)

var wireJSON = sonic.Config{CaseSensitive: true, UseNumber: true, ValidateString: true, UseUnicodeErrors: true}.Froze()
var (
	idPattern       = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	providerPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)
	modelPattern    = regexp.MustCompile(`^[A-Za-z0-9_./:-]{1,128}$`)
	approvalPattern = regexp.MustCompile(`^[A-Za-z0-9_:-]{1,256}$`)
	hashPattern     = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	ulidPattern     = regexp.MustCompile(`^[0-7][0-9A-HJKMNP-TV-Z]{25}$`)
	codePattern     = regexp.MustCompile(`^agent_[a-z_]+$`)
)

// rawJSON is internal only; public DTOs never expose unvalidated wire envelopes.
type rawJSON []byte

func (r *rawJSON) UnmarshalJSON(data []byte) error { *r = bytes.Clone(data); return nil }

// decode enforces required/present/non-null fields, exact key case and duplicate
// rejection in addition to Go types. omitempty means optional on the wire;
// nullable is explicit. Only capabilities/features and event roots allow extensions.
func decode(data []byte, dst any) error {
	if !utf8.Valid(data) || !wireJSON.Valid(data) {
		return ErrProtocol
	}
	node := ast.NewRaw(string(data))
	if !uniqueKeys(&node, 0) || !shape(data, reflect.TypeOf(dst).Elem(), false) || wireJSON.Unmarshal(data, dst) != nil {
		return ErrProtocol
	}
	return nil
}

func uniqueKeys(node *ast.Node, depth int) bool {
	if depth > 128 {
		return false
	}
	switch node.TypeSafe() {
	case ast.V_OBJECT:
		it, err := node.Properties()
		if err != nil {
			return false
		}
		seen := map[string]bool{}
		var pair ast.Pair
		for it.Next(&pair) {
			if seen[pair.Key] || !uniqueKeys(&pair.Value, depth+1) {
				return false
			}
			seen[pair.Key] = true
		}
	case ast.V_ARRAY:
		it, err := node.Values()
		if err != nil {
			return false
		}
		var child ast.Node
		for it.Next(&child) {
			if !uniqueKeys(&child, depth+1) {
				return false
			}
		}
	}
	return true
}

func shape(data []byte, typ reflect.Type, nullable bool) bool {
	data = bytes.TrimSpace(data)
	if bytes.Equal(data, []byte("null")) {
		return nullable
	}
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ == reflect.TypeFor[rawJSON]() || typ.Kind() == reflect.Interface {
		return true
	}
	switch typ.Kind() {
	case reflect.Struct:
		var obj map[string]rawJSON
		if wireJSON.Unmarshal(data, &obj) != nil || obj == nil {
			return false
		}
		allowExtra := typ == reflect.TypeFor[Capabilities]() || typ == reflect.TypeFor[Features]() || typ == reflect.TypeFor[eventWire]()
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			tag := strings.Split(field.Tag.Get("json"), ",")
			value, ok := obj[tag[0]]
			if !ok {
				if len(tag) != 2 || tag[1] != "omitempty" {
					return false
				}
				continue
			}
			if !shape(value, field.Type, field.Tag.Get("bridge") == "nullable") {
				return false
			}
			delete(obj, tag[0])
		}
		return allowExtra || len(obj) == 0
	case reflect.Slice:
		var items []rawJSON
		if wireJSON.Unmarshal(data, &items) != nil || items == nil {
			return false
		}
		for _, item := range items {
			if !shape(item, typ.Elem(), false) {
				return false
			}
		}
	case reflect.Map:
		return len(data) > 0 && data[0] == '{'
	}
	return true // Scalar types are checked by the final Sonic decode.
}

func textLength(s string, min, max int) bool {
	return utf8.ValidString(s) && utf8.RuneCountInString(s) >= min && utf8.RuneCountInString(s) <= max
}
func credential(s string) bool {
	if len(s) == 0 || len(s) > 8192 {
		return false
	}
	for i := range len(s) {
		if s[i] < '!' || s[i] > '~' {
			return false
		}
	}
	return true
}
func decimal(s string) bool {
	if s == "" || (len(s) > 1 && s[0] == '0') {
		return false
	}
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
func compareSeq(a, b string) int {
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	return strings.Compare(a, b)
}
func transcriptSeq(s string) bool { return decimal(s) && compareSeq(s, "9007199254740991") <= 0 }
func timestamp(s string) bool     { _, err := time.Parse(time.RFC3339Nano, s); return err == nil }
func terminalState(s string) bool {
	switch strings.TrimPrefix(s, "run.") {
	case "completed", "failed", "interrupted", "cancelled":
		return true
	}
	return false
}

// ProvisionHash hashes the candidate v1 five-string configuration domain, in
// its specified field order. SessionID is a separate idempotency key. This is
// deliberately not a general Tool/envelope canonical-JSON implementation.
func ProvisionHash(s SessionCreate) (string, error) {
	if !providerPattern.MatchString(s.Provider) || !modelPattern.MatchString(s.Model) || !idPattern.MatchString(s.Profile) ||
		!hashPattern.MatchString(s.ProfileRevision) || !idPattern.MatchString(s.ModelConfigRevision) {
		return "", ErrInvalidRequest
	}
	value := struct {
		Model               string `json:"model"`
		ModelConfigRevision string `json:"model_config_revision"`
		Profile             string `json:"profile"`
		ProfileRevision     string `json:"profile_revision"`
		Provider            string `json:"provider"`
	}{s.Model, s.ModelConfigRevision, s.Profile, s.ProfileRevision, s.Provider}
	data, err := wireJSON.Marshal(value)
	if err != nil {
		return "", ErrInvalidRequest
	}
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func validRun(r Run, sessionID, runID string) bool {
	if r.SessionID != sessionID || r.RunID != runID || !textLength(r.MessageID, 1, 128) ||
		!hashPattern.MatchString(r.ExecutionEnvelopeDigest) || !decimal(r.LastEventID) ||
		r.Terminal != terminalState(r.State) || r.Durable != (r.LastEventID != "0") || (r.Terminal && !r.Durable) {
		return false
	}
	switch r.State {
	case "pending", "running", "waiting_approval", "cancelling", "recovering", "completed", "failed", "interrupted", "cancelled":
		return true
	}
	return false
}

func validTranscript(page TranscriptPage, sessionID string, q TranscriptQuery) bool {
	if page.SessionID != sessionID || !transcriptSeq(page.SnapshotSeq) ||
		(q.SnapshotSeq != "" && page.SnapshotSeq != q.SnapshotSeq) || len(page.Items) > 100 ||
		(q.Limit > 0 && len(page.Items) > q.Limit) {
		return false
	}
	if page.HasMore {
		if len(page.Items) == 0 || page.NextBeforeSeq == nil || *page.NextBeforeSeq != page.Items[0].Seq {
			return false
		}
	} else if page.NextBeforeSeq != nil {
		return false
	}
	previous := ""
	for _, item := range page.Items {
		if !transcriptSeq(item.Seq) || compareSeq(item.Seq, page.SnapshotSeq) >= 0 ||
			(q.BeforeSeq != "" && compareSeq(item.Seq, q.BeforeSeq) >= 0) ||
			(previous != "" && compareSeq(item.Seq, previous) <= 0) || item.MessageID == "" || item.RunID == "" ||
			!timestamp(item.OccurredAt) || (item.Role != "user" && item.Role != "assistant" && item.Role != "tool") {
			return false
		}
		previous = item.Seq
		for _, block := range item.Content {
			switch block.Type {
			case "text":
				if block.Text == nil || block.Name != nil || block.Arguments != nil || block.IsError != nil || block.Content != nil {
					return false
				}
			case "tool_request":
				if block.Name == nil || block.Arguments == nil || block.Text != nil || block.IsError != nil || block.Content != nil {
					return false
				}
			case "tool_result":
				if block.IsError == nil || block.Content == nil || block.Text != nil || block.Name != nil || block.Arguments != nil {
					return false
				}
				for _, text := range *block.Content {
					if text.Type != "text" {
						return false
					}
				}
			default:
				return false
			}
		}
	}
	return true
}

type eventWire struct {
	ID            string  `json:"id"`
	RunID         string  `json:"run_id"`
	SessionID     string  `json:"session_id"`
	Type          string  `json:"type"`
	OccurredAt    string  `json:"occurred_at"`
	SchemaVersion int     `json:"schema_version"`
	Data          rawJSON `json:"data"`
}

func parseEvent(data []byte) (Event, error) {
	var w eventWire
	if decode(data, &w) != nil || !decimal(w.ID) || w.ID == "0" || w.SessionID == "" || w.RunID == "" || !timestamp(w.OccurredAt) {
		return Event{}, ErrProtocol
	}
	if w.SchemaVersion != 1 {
		return Event{}, ErrIncompatible
	}
	e := Event{ID: w.ID, RunID: w.RunID, SessionID: w.SessionID, Type: w.Type, OccurredAt: w.OccurredAt, SchemaVersion: w.SchemaVersion}
	valid := false
	switch w.Type {
	case "run.started":
		var p RunStartedData
		valid = decode(w.Data, &p) == nil && p.MessageID != "" && hashPattern.MatchString(p.ExecutionEnvelopeDigest)
		e.Data = p
	case "message.delta", "message.completed":
		var p MessageData
		valid = decode(w.Data, &p) == nil && p.MessageID != "" && (w.Type == "message.completed" || p.Text != "")
		e.Data = p
	case "tool.proposed":
		var p ToolProposedData
		valid = decode(w.Data, &p) == nil && ulidPattern.MatchString(p.ToolCallID) && textLength(p.Name, 1, 256) &&
			p.IdempotencyKey == "q4d:"+w.RunID+":"+p.ToolCallID && idPattern.MatchString(w.RunID) && decimal(p.SourceSeq) &&
			(p.ArgumentsOmitted == nil || (*p.ArgumentsOmitted && len(p.Arguments) == 0))
		e.Data = p
	case "approval.required":
		var p ApprovalRequiredData
		valid = decode(w.Data, &p) == nil && ulidPattern.MatchString(p.ToolCallID) && p.Name != "" && p.ApprovalID != "" &&
			hashPattern.MatchString(p.ArgumentsHash) && (p.Risk == "R2" || p.Risk == "R3") && timestamp(p.ExpiresAt)
		e.Data = p
	case "tool.started", "tool.completed":
		var p ToolData
		valid = decode(w.Data, &p) == nil && ulidPattern.MatchString(p.ToolCallID)
		e.Data = p
	case "tool.failed":
		var p ToolFailedData
		valid = decode(w.Data, &p) == nil && ulidPattern.MatchString(p.ToolCallID) && codePattern.MatchString(p.Code)
		e.Data = p
	case "usage.updated":
		var p UsageData
		valid = decode(w.Data, &p) == nil && p.MessageID != "" && validUsage(p.Usage)
		e.Data = p
	case "context.compacted":
		var p CompactedData
		valid = decode(w.Data, &p) == nil && decimal(p.SourceSeq)
		e.Data = p
	case "run.progress":
		var p RunProgressData
		valid = decode(w.Data, &p) == nil && (p.Stage == "compacting" || p.Stage == "compaction_failed" || p.Stage == "context_recovery" || p.Stage == "output_recovery" || p.Stage == "finalizing")
		e.Data = p
	case "run.completed", "heartbeat":
		var p EmptyData
		valid = decode(w.Data, &p) == nil
		e.Data = p
	case "run.failed", "run.interrupted":
		var p RunFailedData
		valid = decode(w.Data, &p) == nil && !p.Retryable
		if p.Budget != nil {
			b := p.Budget
			valid = valid && w.Type == "run.failed" && (b.Dimension == "output_per_call" || b.Dimension == "model_calls" || b.Dimension == "tool_calls" || b.Dimension == "input_tokens" || b.Dimension == "output_tokens" || b.Dimension == "context_window")
			for _, n := range []int64{b.Used, b.Limit, b.Requested} {
				valid = valid && n >= 0 && n <= 9007199254740991
			}
		}
		if w.Type == "run.interrupted" {
			valid = valid && p.Code == "agent_runtime_interrupted"
		} else {
			valid = valid && (p.Code == "agent_model_error" || p.Code == "agent_model_limit" || p.Code == "agent_run_blocked" || p.Code == "agent_run_failed")
		}
		e.Data = p
	case "run.cancelled":
		var p RunCancelledData
		valid = decode(w.Data, &p) == nil && p.Reason == "user"
		e.Data = p
	default:
		return Event{}, ErrIncompatible
	}
	if !valid {
		return Event{}, ErrProtocol
	}
	return e, nil
}

func validUsage(u Usage) bool {
	for _, n := range []*int64{&u.InputTokens, &u.OutputTokens, u.TotalTokens, u.CacheReadTokens, u.CacheWriteTokens, u.ReasoningTokens} {
		if n != nil && (*n < 0 || *n > 9007199254740991) {
			return false
		}
	}
	return true
}

func queryString(q TranscriptQuery) (string, error) {
	if q.Limit < 0 || q.Limit > 100 || (q.BeforeSeq != "" && !transcriptSeq(q.BeforeSeq)) || (q.SnapshotSeq != "" && !transcriptSeq(q.SnapshotSeq)) {
		return "", ErrInvalidRequest
	}
	if q.BeforeSeq != "" && q.SnapshotSeq != "" && compareSeq(q.BeforeSeq, q.SnapshotSeq) > 0 {
		return "", ErrInvalidRequest
	}
	var parts []string
	if q.BeforeSeq != "" {
		parts = append(parts, "before_seq="+q.BeforeSeq)
	}
	if q.SnapshotSeq != "" {
		parts = append(parts, "snapshot_seq="+q.SnapshotSeq)
	}
	if q.Limit > 0 {
		parts = append(parts, "limit="+strconv.Itoa(q.Limit))
	}
	if len(parts) == 0 {
		return "", nil
	}
	return "?" + strings.Join(parts, "&"), nil
}
