package application

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/quant4dad/internal/agentbridge"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/repository"
	"github.com/quant4dad/internal/service"
)

func TestAgentToolResultGuardReplay(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func([]agentbridge.Event)
		want   error
	}{
		{name: "guard"},
		{name: "started cannot become pre dispatch", mutate: func(e []agentbridge.Event) {
			e[3].Type, e[3].Data = e[2].Type, e[2].Data
			e[2].Type, e[2].Data = "tool.started", agentbridge.ToolData{ToolCallID: e[1].Data.(agentbridge.ToolProposedData).ToolCallID}
		}, want: service.ErrAgentToolNotFound},
		{name: "approval cannot become pre dispatch", mutate: func(e []agentbridge.Event) {
			e[3].Type, e[3].Data = e[2].Type, e[2].Data
			e[2].Type, e[2].Data = "approval.required", agentbridge.ApprovalRequiredData{ToolCallID: e[1].Data.(agentbridge.ToolProposedData).ToolCallID}
		}, want: service.ErrAgentToolNotFound},
		{name: "ordinary replacement proposal", mutate: func(e []agentbridge.Event) {
			e[3].Type, e[3].Data = e[2].Type, e[2].Data
			p := e[1].Data.(agentbridge.ToolProposedData)
			p.ArgumentsOmitted = nil
			e[2].Type, e[2].Data = "tool.proposed", p
		}, want: service.ErrAgentToolNotFound},
		{name: "different envelope", mutate: func(e []agentbridge.Event) {
			p := e[0].Data.(agentbridge.RunStartedData)
			p.ExecutionEnvelopeDigest = "other"
			e[0].Data = p
		}, want: service.ErrAgentRunUnavailable},

		{name: "ordinary proposal", mutate: func(e []agentbridge.Event) {
			p := e[1].Data.(agentbridge.ToolProposedData)
			p.ArgumentsOmitted = nil
			e[1].Data = p
		}, want: service.ErrAgentToolNotFound},
		{name: "wrong key", mutate: func(e []agentbridge.Event) {
			p := e[1].Data.(agentbridge.ToolProposedData)
			p.IdempotencyKey = "forged"
			e[1].Data = p
		}, want: service.ErrAgentToolNotFound},
		{name: "arguments present", mutate: func(e []agentbridge.Event) {
			p := e[1].Data.(agentbridge.ToolProposedData)
			p.Arguments = map[string]any{"untrusted": true}
			e[1].Data = p
		}, want: service.ErrAgentToolNotFound},
		{name: "wrong call", mutate: func(e []agentbridge.Event) {
			p := e[2].Data.(agentbridge.ToolFailedData)
			p.ToolCallID = "01M00000000000000000000009"
			e[2].Data = p
		}, want: service.ErrAgentToolNotFound},
		{name: "wrong run", mutate: func(e []agentbridge.Event) { e[1].RunID = "01M00000000000000000000009" }, want: service.ErrAgentToolNotFound},
		{name: "wrong session", mutate: func(e []agentbridge.Event) { e[1].SessionID = "01M00000000000000000000009" }, want: service.ErrAgentToolNotFound},
		{name: "unbound start", mutate: func(e []agentbridge.Event) { e[0].Type = "run.progress"; e[0].Data = agentbridge.EmptyData{} }, want: service.ErrAgentToolNotFound},
		{name: "unknown failure", mutate: func(e []agentbridge.Event) {
			p := e[2].Data.(agentbridge.ToolFailedData)
			p.Code = "private_provider_error"
			e[2].Data = p
		}, want: service.ErrAgentToolNotFound},
		{name: "ordinary tool failure", mutate: func(e []agentbridge.Event) {
			p := e[2].Data.(agentbridge.ToolFailedData)
			p.Code = "agent_tool_unavailable"
			e[2].Data = p
		}, want: service.ErrAgentToolNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runs, _, _, runtime, db, session := runAppFixture(t)
			if err := db.AutoMigrate(&domain.AgentToolAudit{}); err != nil {
				t.Fatal(err)
			}
			sent, err := runs.Send(context.Background(), "local-user", session, runTestMessage())
			if err != nil {
				t.Fatal(err)
			}
			projection := runtime.runs[sent.RunID]
			projection.LastEventID = "4"
			projection.Terminal = false
			projection.State = "running"
			runtime.runs[sent.RunID] = projection
			call := "01M00000000000000000000002"
			omitted := true
			events := []agentbridge.Event{
				{Type: "run.started", Data: agentbridge.RunStartedData{MessageID: projection.MessageID, ExecutionEnvelopeDigest: projection.ExecutionEnvelopeDigest}},
				{Type: "tool.proposed", Data: agentbridge.ToolProposedData{ToolCallID: call, Name: "mcp__q4d__validate_pipeline", Arguments: map[string]any{}, ArgumentsOmitted: &omitted, IdempotencyKey: "q4d:" + sent.RunID + ":" + call}},
				{Type: "tool.failed", Data: agentbridge.ToolFailedData{ToolCallID: call, Code: "agent_tool_context_or_arguments_rejected"}},
				{Type: "message.delta", Data: agentbridge.MessageData{MessageID: projection.MessageID, Text: "still generating"}},
			}
			for i := range events {
				events[i].ID = strconv.Itoa(i + 1)
				events[i].SessionID = session
				events[i].RunID = sent.RunID
			}
			if tc.mutate != nil {
				tc.mutate(events)
			}
			streams := 0
			runtime.stream = func(ctx context.Context, s, r, c string, cb agentbridge.StreamCallbacks) (agentbridge.StreamResult, error) {
				streams++
				if s != session || r != sent.RunID || c != "0" {
					t.Fatal("unbound stream")
				}
				for _, e := range events {
					if err := cb.Event(e); err != nil {
						return agentbridge.StreamResult{}, err
					}
				}
				t.Fatal("continued beyond captured active-run boundary")
				return agentbridge.StreamResult{}, nil
			}
			app := NewAgentToolResultApplication(service.NewAgentToolAuditService(repository.NewAgentToolAuditRepository(db), nil), runs)
			result, err := app.Get(context.Background(), "local-user", sent.RunID, call)
			if !errors.Is(err, tc.want) {
				t.Fatalf("result=%+v error=%v want=%v", result, err, tc.want)
			}
			if tc.want == nil && (result.ExecutionStage != "pre_dispatch" || result.Status != "failed" || result.ToolName != "validate_pipeline" || result.RunID != sent.RunID || result.ToolCallID != call || result.Risk != "" || len(result.Result) != 0) {
				t.Fatalf("unsafe projection: %+v", result)
			}
			var count int64
			db.Model(&domain.AgentToolAudit{}).Count(&count)
			if count != 0 {
				t.Fatal("GET wrote audit")
			}
			for _, request := range []struct{ actor, run string }{{"another-user", sent.RunID}, {"local-user", "01M00000000000000000000009"}} {
				if _, err := app.Get(context.Background(), request.actor, request.run, call); !errors.Is(err, service.ErrAgentRunNotFound) {
					t.Fatal("ownership", err)
				}
			}
			if streams != 1 {
				t.Fatal("unauthorized request replayed events")
			}
			// A durable Gateway record takes precedence and survives Runtime outage.
			row := domain.AgentToolAudit{ID: "audit", ActorID: "local-user", SessionID: session, RunID: sent.RunID, ToolCallID: call, ToolName: "validate_pipeline", Status: "failed", Risk: "R0", ErrorCode: "tool_unavailable"}
			if err := db.Create(&row).Error; err != nil {
				t.Fatal(err)
			}
			runtime.unavailable = true
			result, err = app.Get(context.Background(), "local-user", sent.RunID, call)
			if err != nil || result.ExecutionStage != "" || result.ErrorCode != "tool_unavailable" || streams != 1 {
				t.Fatal(result, err)
			}
		})
	}
}

func TestAgentToolResultReplayBounds(t *testing.T) {
	for _, kind := range []string{"events", "bytes", "timeout"} {
		t.Run(kind, func(t *testing.T) {
			runs, _, _, runtime, db, session := runAppFixture(t)
			if err := db.AutoMigrate(&domain.AgentToolAudit{}); err != nil {
				t.Fatal(err)
			}
			sent, err := runs.Send(context.Background(), "local-user", session, runTestMessage())
			if err != nil {
				t.Fatal(err)
			}
			runtime.stream = func(ctx context.Context, s, r, c string, cb agentbridge.StreamCallbacks) (agentbridge.StreamResult, error) {
				if kind == "timeout" {
					deadline, ok := ctx.Deadline()
					if !ok || time.Until(deadline) > 2*time.Second {
						t.Fatal("unbounded read")
					}
					<-ctx.Done()
					return agentbridge.StreamResult{}, ctx.Err()
				}
				data := agentbridge.MessageData{Text: "small"}
				if kind == "bytes" {
					data.Text = strings.Repeat("x", 8<<20)
				}
				for i := 1; i <= 8193; i++ {
					if err := cb.Event(agentbridge.Event{ID: strconv.Itoa(i), RunID: r, SessionID: s, Type: "message.delta", Data: data}); err != nil {
						return agentbridge.StreamResult{}, err
					}
				}
				t.Fatal("unbounded event replay")
				return agentbridge.StreamResult{}, nil
			}
			app := NewAgentToolResultApplication(service.NewAgentToolAuditService(repository.NewAgentToolAuditRepository(db), nil), runs)
			_, err = app.Get(context.Background(), "local-user", sent.RunID, "01M00000000000000000000002")
			if kind == "timeout" {
				if err == nil {
					t.Fatal("timeout accepted")
				}
			} else if !errors.Is(err, service.ErrAgentToolStore) {
				t.Fatal(err)
			}
		})
	}
}
