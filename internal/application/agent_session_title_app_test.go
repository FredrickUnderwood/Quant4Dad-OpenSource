package application

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/quant4dad/internal/agentbridge"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/llm"
	"github.com/quant4dad/internal/service"
)

type titleTestRuntime struct {
	*sessionTestRuntime
	items []agentbridge.TranscriptItem
	read  func(context.Context, string, agentbridge.TranscriptQuery) (agentbridge.TranscriptPage, error)
}

func (r *titleTestRuntime) Transcript(ctx context.Context, id string, q agentbridge.TranscriptQuery) (agentbridge.TranscriptPage, error) {
	if r.read != nil {
		return r.read(ctx, id, q)
	}
	return agentbridge.TranscriptPage{SessionID: id, SnapshotSeq: "99", Items: r.items}, nil
}

type titleTestModel struct {
	requests []llm.CompleteRequest
	complete func(context.Context, llm.CompleteRequest) (llm.CompleteResponse, error)
}

func (m *titleTestModel) Provider() string { return "fixture" }
func (m *titleTestModel) Resolve(_ context.Context, provider string) (llm.Client, error) {
	if provider != "fixture" {
		return nil, errors.New("unexpected provider")
	}
	return m, nil
}
func (m *titleTestModel) Complete(ctx context.Context, request llm.CompleteRequest) (llm.CompleteResponse, error) {
	m.requests = append(m.requests, request)
	if m.complete != nil {
		return m.complete(ctx, request)
	}
	return llm.CompleteResponse{Text: "山西汾酒趋势策略", TokensPrompt: 100, TokensCompletion: 10}, nil
}

func titleUser(text string) agentbridge.TranscriptItem {
	return agentbridge.TranscriptItem{Role: "user", Content: []agentbridge.TranscriptContent{{Type: "text", Text: &text}}}
}

func TestAgentSessionTitleEveryAcceptedMessageAndIdempotency(t *testing.T) {
	runs, sessions, settings, _, _, id := runAppFixture(t)
	ctx := context.Background()
	runtime := &titleTestRuntime{items: []agentbridge.TranscriptItem{titleUser("帮我研究山西汾酒趋势策略")}}
	model := &titleTestModel{}
	worker := NewAgentSessionTitleApplication(runs.sessions, service.NewAgentSessionRuntimeService(runtime), settings, model)
	input := runTestMessage()
	first, err := runs.Send(ctx, "local-user", id, input)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := sessions.Metadata(ctx, "local-user", id)
	if err != nil || !metadata.TitlePending || metadata.TitleRevision != 1 {
		t.Fatal(metadata, err)
	}
	if err := worker.ProcessBatch(ctx); err != nil {
		t.Fatal(err)
	}
	metadata, _ = sessions.Metadata(ctx, "local-user", id)
	if metadata.Title != "山西汾酒趋势策略" || metadata.TitlePending || metadata.TitleRevision != 2 {
		t.Fatal(metadata)
	}
	for range 3 {
		if _, err := runs.Send(ctx, "local-user", id, input); err != nil {
			t.Fatal(err)
		}
		if _, err := runs.Get(ctx, "local-user", first.RunID); err != nil {
			t.Fatal(err)
		}
		if err := worker.ProcessBatch(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if len(model.requests) != 1 {
		t.Fatal("duplicate delivery regenerated title")
	}
	input.ClientRequestID = "second-message"
	runtime.items = append(runtime.items, titleUser("改成贵州茅台，关注风险"))
	model.complete = func(_ context.Context, request llm.CompleteRequest) (llm.CompleteResponse, error) {
		if request.Model != "fixture-model" || request.MaxTokens > 512 || request.System == "" || !strings.Contains(request.Prompt, "山西汾酒趋势策略") || !strings.Contains(request.Prompt, "改成贵州茅台") {
			t.Fatal(request)
		}
		return llm.CompleteResponse{Text: "“贵州茅台策略风险分析”"}, nil
	}
	if _, err := runs.Send(ctx, "local-user", id, input); err != nil {
		t.Fatal(err)
	}
	if err := worker.ProcessBatch(ctx); err != nil {
		t.Fatal(err)
	}
	metadata, _ = sessions.Metadata(ctx, "local-user", id)
	if metadata.Title != "贵州茅台策略风险分析" || metadata.TitleRevision != 4 || len(model.requests) != 2 {
		t.Fatal(metadata, len(model.requests))
	}
	if _, err := sessions.Metadata(ctx, "another-owner", id); !errors.Is(err, service.ErrAgentSessionNotFound) {
		t.Fatal("metadata ownership failed", err)
	}
}

func TestAgentSessionTitleLateResultFenced(t *testing.T) {
	for _, action := range []string{"manual", "new_message"} {
		t.Run(action, func(t *testing.T) {
			runs, sessions, settings, _, _, id := runAppFixture(t)
			ctx := context.Background()
			if _, err := runs.Send(ctx, "local-user", id, runTestMessage()); err != nil {
				t.Fatal(err)
			}
			model := &titleTestModel{}
			model.complete = func(context.Context, llm.CompleteRequest) (llm.CompleteResponse, error) {
				if action == "manual" {
					title := "我手动指定的标题"
					if _, err := sessions.Patch(ctx, "local-user", id, service.AgentSessionPatch{Title: &title}); err != nil {
						t.Fatal(err)
					}
				} else {
					input := runTestMessage()
					input.ClientRequestID = "newer-request"
					if _, err := runs.Send(ctx, "local-user", id, input); err != nil {
						t.Fatal(err)
					}
				}
				return llm.CompleteResponse{Text: "已经过期的标题"}, nil
			}
			runtime := &titleTestRuntime{items: []agentbridge.TranscriptItem{titleUser("趋势策略")}}
			worker := NewAgentSessionTitleApplication(runs.sessions, service.NewAgentSessionRuntimeService(runtime), settings, model)
			if err := worker.ProcessBatch(ctx); err != nil {
				t.Fatal(err)
			}
			value, _ := sessions.Metadata(ctx, "local-user", id)
			if value.Title == "已经过期的标题" || value.TitlePending != (action == "new_message") {
				t.Fatal(value)
			}
			if action == "manual" {
				if value.Title != "我手动指定的标题" {
					t.Fatal(value)
				}
				input := runTestMessage()
				input.ClientRequestID = "after-manual-rename"
				if _, err := runs.Send(ctx, "local-user", id, input); err != nil {
					t.Fatal(err)
				}
			}
			model.complete = nil
			if err := worker.ProcessBatch(ctx); err != nil {
				t.Fatal(err)
			}
			value, _ = sessions.Metadata(ctx, "local-user", id)
			if value.Title != "山西汾酒趋势策略" || value.TitlePending {
				t.Fatal(value)
			}
		})
	}
}

func TestAgentSessionTitleFailureSettlesWithoutAffectingMessage(t *testing.T) {
	for _, reason := range []string{"provider", "invalid_output", "disabled", "transcript", "cancelled"} {
		t.Run(reason, func(t *testing.T) {
			runs, sessions, settings, _, _, id := runAppFixture(t)
			ctx := context.Background()
			sent, err := runs.Send(ctx, "local-user", id, runTestMessage())
			if err != nil || !sent.Durable {
				t.Fatal(sent, err)
			}
			runtime := &titleTestRuntime{items: []agentbridge.TranscriptItem{titleUser("测试需求")}}
			model := &titleTestModel{}
			work, cancel := context.WithCancel(ctx)
			defer cancel()
			model.complete = func(context.Context, llm.CompleteRequest) (llm.CompleteResponse, error) {
				if reason == "cancelled" {
					cancel()
					return llm.CompleteResponse{}, work.Err()
				}
				if reason == "provider" {
					return llm.CompleteResponse{}, errors.New("private provider error")
				}
				return llm.CompleteResponse{Text: "<think>reasoning</think>\n标题"}, nil
			}
			if reason == "disabled" {
				disabled := false
				if _, err := settings.PatchLLMProvider(ctx, "fixture", service.LLMProviderPatch{Agent: &service.AgentModelOptionsPatch{Enabled: &disabled}}, ""); err != nil {
					t.Fatal(err)
				}
			}
			if reason == "transcript" {
				runtime.read = func(context.Context, string, agentbridge.TranscriptQuery) (agentbridge.TranscriptPage, error) {
					return agentbridge.TranscriptPage{}, errors.New("unavailable")
				}
			}
			worker := NewAgentSessionTitleApplication(runs.sessions, service.NewAgentSessionRuntimeService(runtime), settings, model)
			err = worker.ProcessBatch(work)
			if (err != nil) != (reason == "cancelled") {
				t.Fatal(err)
			}
			value, _ := sessions.Metadata(ctx, "local-user", id)
			if value.Title != "原始标题" || value.TitlePending != (reason == "cancelled") {
				t.Fatal(value)
			}
			before := len(model.requests)
			if err := worker.ProcessBatch(ctx); err != nil || len(model.requests) != before {
				t.Fatal("unexpected immediate retry", err)
			}
			if result, err := runs.Get(ctx, "local-user", sent.RunID); err != nil || !result.Durable {
				t.Fatal("title failure affected run", err)
			}
		})
	}
}

func TestAgentSessionTitleLeaseRecoveryAndPendingAdmission(t *testing.T) {
	runs, sessions, _, runtime, db, id := runAppFixture(t)
	ctx := context.Background()
	runtime.lose = true
	sent, err := runs.Send(ctx, "local-user", id, runTestMessage())
	if err == nil || sent.Durable {
		t.Fatal("lost reply fixture failed")
	}
	value, _ := sessions.Metadata(ctx, "local-user", id)
	if value.TitlePending {
		t.Fatal("unconfirmed send queued a title")
	}
	if _, err := runs.Reconcile(ctx, "local-user", sent.RunID); err != nil {
		t.Fatal(err)
	}
	rows, err := runs.sessions.DueTitles(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	row := rows[0]
	first, claimed, err := runs.sessions.ClaimTitle(ctx, row)
	if err != nil || !claimed {
		t.Fatal(claimed, err)
	}
	if _, claimed, err := runs.sessions.ClaimTitle(ctx, row); err != nil || claimed {
		t.Fatal("two API workers claimed the same title", err)
	}
	if err := db.Model(&domain.AgentSessionBinding{}).Where("id = ?", id).Update("title_lease_until_ms", time.Now().UnixMilli()-1).Error; err != nil {
		t.Fatal(err)
	}
	second, claimed, err := runs.sessions.ClaimTitle(ctx, row)
	if err != nil || !claimed {
		t.Fatal(claimed, err)
	}
	if applied, err := runs.sessions.FinishTitle(ctx, row, first, "旧进程标题"); err != nil || applied {
		t.Fatal("expired worker overwrote recovered work", err)
	}
	if applied, err := runs.sessions.FinishTitle(ctx, row, second, "恢复生成的标题"); err != nil || !applied {
		t.Fatal(applied, err)
	}
}

func TestAgentSessionTitleContextIsBoundedAndUserOnly(t *testing.T) {
	_, sessions, settings, _, _ := sessionAppFixture(t)
	runtime := &titleTestRuntime{}
	secret := "TOOL_RESULT_NOT_FOR_TITLE"
	older := "10"
	reads := 0
	runtime.read = func(_ context.Context, id string, query agentbridge.TranscriptQuery) (agentbridge.TranscriptPage, error) {
		reads++
		if reads == 1 {
			return agentbridge.TranscriptPage{SessionID: id, SnapshotSeq: "99", Items: []agentbridge.TranscriptItem{{Role: "tool", Content: []agentbridge.TranscriptContent{{Type: "text", Text: &secret}}}}, HasMore: true, NextBeforeSeq: &older}, nil
		}
		if query.SnapshotSeq != "99" || query.BeforeSeq != "10" {
			t.Fatal(query)
		}
		return agentbridge.TranscriptPage{SessionID: id, SnapshotSeq: "99", Items: []agentbridge.TranscriptItem{titleUser("旧需求"), titleUser(strings.Repeat("中🐉", 10000))}}, nil
	}
	worker := NewAgentSessionTitleApplication(sessions, service.NewAgentSessionRuntimeService(runtime), settings, &titleTestModel{})
	prompt, err := worker.prompt(context.Background(), domain.AgentSessionBinding{ID: "fixture", Title: "旧标题"}, 1600)
	if err != nil || strings.Contains(prompt, secret) || !strings.Contains(prompt, "旧需求") || len([]rune(prompt)) > 1800 {
		t.Fatal(len(prompt), err)
	}
	var value struct {
		Messages []string `json:"recent_user_messages"`
	}
	if err := sonic.UnmarshalString(prompt, &value); err != nil || len(value.Messages) != 2 || len([]rune(value.Messages[1])) != 1200 {
		t.Fatal(err)
	}
}
