package agentbridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testHash = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const testToolID = "01K4M000000000000000000000"

func stringPointer(value string) *string { return &value }

func testSession() SessionCreate {
	s := SessionCreate{SessionID: "session-1", Provider: "fixture", Model: "alpha", Profile: "research", ProfileRevision: testHash, ModelConfigRevision: "v1"}
	s.ProvisionRequestHash, _ = ProvisionHash(s)
	return s
}
func testPrompt() PromptRequest {
	return PromptRequest{SessionID: "session-1", RunID: "run-1", ClientRequestID: "client-1", RequestHash: testHash,
		ExecutionEnvelopeDigest: testHash, RunCapability: "synthetic-capability", Content: []TextContent{{Type: "text", Text: "你好 😀\nquery"}}}
}
func testRun() Run {
	return Run{SessionID: "session-1", RunID: "run-1", MessageID: "message-1", ExecutionEnvelopeDigest: testHash, Durable: true, State: "completed", Terminal: true, LastEventID: "12"}
}
func testClient(t *testing.T, handler http.HandlerFunc, options ...func(*Config)) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-bridge-token" {
			t.Error("missing bridge authorization")
		}
		if r.URL.RawQuery != "" && !strings.Contains(r.URL.Path, "/sessions/") {
			t.Error("credential/query in unexpected route")
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	cfg := Config{BaseURL: server.URL, Token: "synthetic-bridge-token"}
	for _, option := range options {
		option(&cfg)
	}
	client, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.CloseIdleConnections)
	return client
}
func writeJSON(t *testing.T, w http.ResponseWriter, status int, value any) {
	t.Helper()
	data, err := wireJSON.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}
func assertBridgeError(t *testing.T, err error, code string, status int, unknown bool) {
	t.Helper()
	var value *Error
	if !errors.As(err, &value) || value.Code != code || value.Status != status || value.OutcomeUnknown != unknown {
		t.Fatalf("unexpected error: %#v", err)
	}
}

func TestConfigRejectsUntrustedURLForms(t *testing.T) {
	for _, origin := range []string{"", "ftp://host", "http://u:secret@host", "http://host/path", "http://host?token=secret", "http://host?", "http://host#secret", "http://host/%2e%2e"} {
		if _, err := New(Config{BaseURL: origin, Token: "token"}); err != ErrInvalidRequest {
			t.Fatalf("accepted invalid origin")
		}
	}
	for _, token := range []string{"", "token\nheader", "Bearer token", strings.Repeat("a", 8193)} {
		if _, err := New(Config{BaseURL: "http://localhost", Token: token}); err != ErrInvalidRequest {
			t.Fatal("accepted invalid credential")
		}
	}
}

func TestClientLifecycleRoutes(t *testing.T) {
	session, prompt := testSession(), testPrompt()
	var paths []string
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.EscapedPath())
		body, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/q4d/v1/health":
			writeJSON(t, w, 200, map[string]string{"status": "ready"})
		case "/q4d/v1/capabilities":
			writeJSON(t, w, 200, map[string]any{"bridge_protocol": 1, "adapter_version": "fixture", "backend": "cordis", "dsh_version": "alpha", "session_format": 0, "event_journal_format": 1, "session_binding_format": 1, "model_config_revision": "v1", "future_extension": true,
				"features": map[string]bool{"session_resume": true, "event_replay": true, "cancel": true, "approval": true, "raw_provider_delta": false, "compaction_events": false, "fork": false, "session_provisioning": true, "future_extension": true}})
		case "/q4d/v1/sessions":
			var input SessionCreate
			if decode(body, &input) != nil || input != session {
				t.Error("session request mismatch")
			}
			writeJSON(t, w, 200, SessionCreated{SessionID: input.SessionID, DSHSessionID: input.SessionID, Durable: true, Provider: input.Provider, Model: input.Model, Profile: input.Profile, CreatedProfileRevision: input.ProfileRevision, CreatedModelConfigRevision: input.ModelConfigRevision, ProvisionRequestHash: input.ProvisionRequestHash, RuntimeProvenance: Provenance{BridgeProtocol: 1, AdapterVersion: "fixture", Backend: "cordis", DSHVersion: "alpha", SessionFormat: 0, EventJournalFormat: 1, SessionBindingFormat: 1}})
		case "/q4d/v1/sessions/session-1/prompts":
			var input PromptRequest
			if decode(body, &input) != nil || !reflect.DeepEqual(input, prompt) {
				t.Error("prompt request mismatch")
			}
			writeJSON(t, w, 202, PromptAccepted{RunID: "run-1", MessageID: "message-1", Durable: true, State: "accepted"})
		case "/q4d/v1/runs/run-1", "/q4d/v1/runs/run-1/cancel":
			if r.Method == "POST" && string(body) != "{}" {
				t.Error("cancel body must be empty object")
			}
			writeJSON(t, w, 200, testRun())
		case "/q4d/v1/approvals/q4d:run-1:" + testToolID + "/decision":
			var input ApprovalDecision
			if decode(body, &input) != nil || input.ApprovalReceipt != "synthetic-receipt" {
				t.Error("approval body mismatch")
			}
			writeJSON(t, w, 200, ApprovalAcknowledged{RunID: input.RunID, ToolCallID: input.ToolCallID, Decision: input.Decision})
		case "/q4d/v1/sessions/session-1/close":
			if string(body) != "{}" {
				t.Error("close body must be empty object")
			}
			writeJSON(t, w, 200, SessionClosed{SessionID: "session-1", DSHSessionID: "session-1", Durable: true, Loaded: false})
		default:
			t.Error("unexpected route")
			w.WriteHeader(404)
		}
	})
	ctx := context.Background()
	if err := client.Health(ctx); err != nil {
		t.Fatal(err)
	}
	caps, err := client.Capabilities(ctx)
	if err != nil || caps.CheckCompatibility() != nil {
		t.Fatalf("capabilities failed: %v", err)
	}
	if _, err := client.CreateSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Prompt(ctx, prompt); err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetRun(ctx, "session-1", "run-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CancelRun(ctx, "session-1", "run-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.DecideApproval(ctx, "q4d:run-1:"+testToolID, ApprovalDecision{RunID: "run-1", ToolCallID: testToolID, Decision: "allow_once", ApprovalReceipt: "synthetic-receipt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CloseSession(ctx, "session-1"); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 8 || paths[2] != "POST /q4d/v1/sessions" || paths[3] != "POST /q4d/v1/sessions/session-1/prompts" {
		t.Fatal("unexpected request methods")
	}
}

func TestInvalidRequestsNeverReachRuntime(t *testing.T) {
	var calls atomic.Int32
	client := testClient(t, func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(500) })
	ctx := context.Background()
	session := testSession()
	session.ProvisionRequestHash = testHash
	_, createErr := client.CreateSession(ctx, session)
	prompt := testPrompt()
	prompt.Content = append(prompt.Content, TextContent{Type: "text", Text: "extra"})
	_, promptErr := client.Prompt(ctx, prompt)
	_, closeErr := client.CloseSession(ctx, "../session")
	_, runErr := client.GetRun(ctx, "session-1", "run?secret")
	_, transcriptErr := client.Transcript(ctx, "session-1", TranscriptQuery{SnapshotSeq: "01"})
	_, approveErr := client.DecideApproval(ctx, "approval-1", ApprovalDecision{RunID: "run-1", ToolCallID: testToolID, Decision: "allow_once"})
	_, rejectErr := client.DecideApproval(ctx, "approval-1", ApprovalDecision{RunID: "run-1", ToolCallID: testToolID, Decision: "reject", ApprovalReceipt: "secret"})
	_, streamErr := client.ConsumeEvents(ctx, "session-1", "run-1", "-1", func(Event) error { return nil })
	for _, err := range []error{createErr, promptErr, closeErr, runErr, transcriptErr, approveErr, rejectErr, streamErr} {
		if err != ErrInvalidRequest {
			t.Fatalf("unexpected validation: %v", err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid request reached runtime")
	}
}

func TestResponseIdentityAndWireValidation(t *testing.T) {
	base := `{"run_id":"run-1","message_id":"message-1","durable":true,"state":"accepted"}`
	for name, body := range map[string]string{
		"wrong identity":   strings.Replace(base, "run-1", "other", 1),
		"missing durable":  strings.Replace(base, `"durable":true,`, "", 1),
		"null durable":     strings.Replace(base, `"durable":true`, `"durable":null`, 1),
		"duplicate":        strings.Replace(base, `"durable":true`, `"durable":false,"durable":true`, 1),
		"wrong case":       strings.Replace(base, "durable", "Durable", 1),
		"secret extension": strings.TrimSuffix(base, "}") + `,"capability":"secret"}`,
		"trailing JSON":    base + `{}`, "invalid utf8": strings.Replace(base, "message-1", "\xff", 1),
	} {
		t.Run(name, func(t *testing.T) {
			client := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(202)
				_, _ = io.WriteString(w, body)
			})
			result, err := client.Prompt(context.Background(), testPrompt())
			assertBridgeError(t, err, ErrProtocol.Error(), 202, true)
			if result != (PromptAccepted{}) {
				t.Fatal("invalid response escaped")
			}
		})
	}
}

func TestErrorCodesAndUnknownMutationOutcome(t *testing.T) {
	for _, status := range []int{403, 409, 504, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			code := map[int]string{403: "agent_capability_rejected", 409: "agent_request_conflict", 504: "agent_operation_timeout", 500: "agent_internal_error"}[status]
			client := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(t, w, status, map[string]any{"error": map[string]string{"code": code}})
			})
			_, err := client.Prompt(context.Background(), testPrompt())
			assertBridgeError(t, err, code, status, status >= 500)
		})
	}
}

func TestErrorsAndFormattingDoNotExposeCredentials(t *testing.T) {
	secret := "sensitive-response-secret"
	for _, body := range []string{`{"error":{"code":"agent_` + secret + `"}}`, `{"error":{"code":"agent_internal_error","detail":"` + secret + `"}}`, "<html>" + secret} {
		client := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(500)
			_, _ = io.WriteString(w, body)
		})
		_, err := client.Prompt(context.Background(), testPrompt())
		if !errors.Is(err, ErrProtocol) || strings.Contains(fmt.Sprintf("%v %+v %#v", err, err, err), secret) {
			t.Fatal("untrusted error exposed")
		}
	}
	for _, value := range []any{Config{Token: secret}, PromptRequest{RunCapability: secret}, ApprovalDecision{ApprovalReceipt: secret}} {
		if strings.Contains(fmt.Sprintf("%v %+v %#v", value, value, value), secret) {
			t.Fatal("formatted credential exposed")
		}
	}
}

func TestRedirectDoesNotForwardCredential(t *testing.T) {
	var forwarded atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { forwarded.Add(1); w.WriteHeader(200) }))
	defer target.Close()
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	})
	_, err := client.Prompt(context.Background(), testPrompt())
	if !errors.Is(err, ErrProtocol) || forwarded.Load() != 0 {
		t.Fatal("followed redirect")
	}
}

func TestMutationTimeoutDoesNotRetryOrClaimFailure(t *testing.T) {
	var calls atomic.Int32
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}, func(c *Config) { c.RequestTimeout = 40 * time.Millisecond })
	_, err := client.Prompt(context.Background(), testPrompt())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout lost: %v", err)
	}
	assertBridgeError(t, err, "agent_bridge_timeout", 0, true)
	if calls.Load() != 1 {
		t.Fatal("mutation was retried")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = client.Prompt(ctx, testPrompt())
	assertBridgeError(t, err, "agent_bridge_cancelled", 0, false)
	if calls.Load() != 1 {
		t.Fatal("cancelled request dispatched")
	}
}

func TestOversizedAndMislabelledResponseRejected(t *testing.T) {
	for _, media := range []string{"application/json", "text/html", "application/json; charset=latin1"} {
		client := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", media)
			_, _ = io.WriteString(w, strings.Repeat(" ", maxJSONBytes+1))
		})
		if err := client.Health(context.Background()); !errors.Is(err, ErrProtocol) {
			t.Fatalf("accepted invalid response: %v", err)
		}
	}
}

func TestRunStateAndCompatibilityChecks(t *testing.T) {
	run := testRun()
	if !validRun(run, "session-1", "run-1") {
		t.Fatal("valid run rejected")
	}
	for _, edit := range []func(*Run){func(r *Run) { r.SessionID = "other" }, func(r *Run) { r.Terminal = false }, func(r *Run) { r.Durable = false }, func(r *Run) { r.LastEventID = "00" }, func(r *Run) { r.State = "unknown" }} {
		bad := run
		edit(&bad)
		if validRun(bad, "session-1", "run-1") {
			t.Fatal("accepted inconsistent run")
		}
	}
	one := 1
	caps := Capabilities{BridgeProtocol: 1, SessionFormat: 0, EventJournalFormat: 1, SessionBindingFormat: &one, Features: Features{SessionProvisioning: true, SessionResume: true, EventReplay: true, Cancel: true, Approval: true}}
	if caps.CheckCompatibility() != nil {
		t.Fatal("valid capability rejected")
	}
	for _, edit := range []func(*Capabilities){func(c *Capabilities) { c.BridgeProtocol = 2 }, func(c *Capabilities) { c.SessionFormat = 1 }, func(c *Capabilities) { c.EventJournalFormat = 2 }, func(c *Capabilities) { c.SessionBindingFormat = nil }, func(c *Capabilities) { c.Features.Approval = false }} {
		bad := caps
		edit(&bad)
		if bad.CheckCompatibility() != ErrIncompatible {
			t.Fatal("incompatible runtime accepted")
		}
	}
}

func TestTranscriptPaginationAndArchivalBlocks(t *testing.T) {
	text, name, args, bad := "", "query_kline", `{"symbol":"TEST"}`, false
	content := []TextContent{{Type: "text", Text: "result"}}
	page := TranscriptPage{SessionID: "session-1", SnapshotSeq: "20", HasMore: true, NextBeforeSeq: stringPointer("4"), Items: []TranscriptItem{{Seq: "4", MessageID: "message-1", RunID: "run-1", Role: "assistant", OccurredAt: "2026-09-09T00:00:00Z", Content: []TranscriptContent{{Type: "text", Text: &text}, {Type: "tool_request", Name: &name, Arguments: &args}, {Type: "tool_result", IsError: &bad, Content: &content}}}}}
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "before_seq=10&snapshot_seq=20&limit=2" {
			t.Error("query lost pagination")
		}
		writeJSON(t, w, 200, page)
	})
	query := TranscriptQuery{BeforeSeq: "10", SnapshotSeq: "20", Limit: 2}
	got, err := client.Transcript(context.Background(), "session-1", query)
	if err != nil || !reflect.DeepEqual(page, got) {
		t.Fatalf("page mismatch: %v", err)
	}
	for _, edit := range []func(*TranscriptPage){func(p *TranscriptPage) { p.SnapshotSeq = "21" }, func(p *TranscriptPage) { p.NextBeforeSeq = stringPointer("5") }, func(p *TranscriptPage) { p.Items = append(p.Items, p.Items[0]) }, func(p *TranscriptPage) { p.HasMore = false }} {
		bad := page
		edit(&bad)
		if validTranscript(bad, "session-1", query) {
			t.Fatal("accepted invalid page")
		}
	}
}

func TestTranscriptSnapshotLossIsExplicit(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, 409, map[string]any{"error": map[string]string{"code": "agent_transcript_snapshot_unavailable"}})
	})
	page, err := client.Transcript(context.Background(), "session-1", TranscriptQuery{SnapshotSeq: "25"})
	assertBridgeError(t, err, "agent_transcript_snapshot_unavailable", 409, false)
	if page.Items != nil {
		t.Fatal("missing history became a valid page")
	}
	if _, err := queryString(TranscriptQuery{SnapshotSeq: "25", BeforeSeq: "26"}); err != ErrInvalidRequest {
		t.Fatal("accepted cursor beyond snapshot")
	}
}

func TestSharedEventPayloadFixtures(t *testing.T) {
	data, err := os.ReadFile("../../agent-runtime/evals/fixture-v1/contracts/bridge/fixtures/events.valid.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures map[string]any
	if wireJSON.Unmarshal(data, &fixtures) != nil {
		t.Fatal("invalid fixtures")
	}
	if len(fixtures) != 16 {
		t.Fatal("event fixture coverage changed")
	}
	for eventType, payload := range fixtures {
		t.Run(eventType, func(t *testing.T) {
			value := map[string]any{"id": "9007199254740993", "run_id": "run-1", "session_id": "session-1", "type": eventType, "occurred_at": "2026-09-09T00:00:00Z", "schema_version": 1, "data": payload, "future_extension": true}
			encoded, _ := wireJSON.Marshal(value)
			event, err := parseEvent(encoded)
			if err != nil || event.Type != eventType {
				t.Fatalf("shared fixture rejected: %v", err)
			}
			if usage, ok := event.Data.(UsageData); ok && (usage.Usage.TotalTokens != nil || usage.Usage.CacheReadTokens != nil || usage.Usage.ReasoningTokens != nil) {
				t.Fatal("invented optional usage")
			}
			payload.(map[string]any)["unexpected_credential"] = "secret"
			encoded, _ = wireJSON.Marshal(value)
			if _, err := parseEvent(encoded); !errors.Is(err, ErrProtocol) {
				t.Fatal("accepted unknown payload field")
			}
		})
	}
}

func TestEventSemanticValidation(t *testing.T) {
	for name, data := range map[string]string{
		"unknown progress":           `{"type":"run.progress","data":{"stage":"provider_raw_error"}}`,
		"null":                       `{"type":"run.completed","data":null}`,
		"retryable":                  `{"type":"run.failed","data":{"code":"agent_model_error","retryable":true}}`,
		"missing boolean":            `{"type":"run.failed","data":{"code":"agent_model_error"}}`,
		"unsafe usage":               `{"type":"usage.updated","data":{"message_id":"m","usage":{"input_tokens":9007199254740992,"output_tokens":0}}}`,
		"null usage":                 `{"type":"usage.updated","data":{"message_id":"m","usage":{"input_tokens":1,"output_tokens":0,"total_tokens":null}}}`,
		"wrong tool binding":         `{"type":"tool.proposed","data":{"tool_call_id":"` + testToolID + `","name":"query","arguments":{},"idempotency_key":"q4d:other:` + testToolID + `","source_seq":"1"}}`,
		"nonempty omitted arguments": `{"type":"tool.proposed","data":{"tool_call_id":"` + testToolID + `","name":"query","arguments":{"x":1},"arguments_omitted":true,"idempotency_key":"q4d:run-1:` + testToolID + `","source_seq":"1"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			full := `{"id":"1","run_id":"run-1","session_id":"session-1","occurred_at":"2026-09-09T00:00:00Z","schema_version":1,` + strings.TrimPrefix(data, "{")
			if _, err := parseEvent([]byte(full)); !errors.Is(err, ErrProtocol) {
				t.Fatalf("invalid event accepted: %v", err)
			}
		})
	}
}
