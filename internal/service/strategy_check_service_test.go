package service

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/repository"
	"github.com/quant4dad/internal/script"
)

func validationFixture(t *testing.T) (*StrategyService, *repository.AgentToolArtifactRepository, context.Context, StrategyInput) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	files, err := repository.NewAgentToolArtifactRepository(filepath.Join(root, "checks"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithAgentExecution(context.Background(), domain.AgentToolAudit{ID: "audit", ToolName: "validate_strategy", ActorID: "owner", SessionID: "session"}, domain.AgentApprovalReceipt{})
	in := StrategyInput{Name: "bound", Universe: []string{"sh.600809"}, Period: domain.Bar1d, Body: domain.StrategyBody{Mode: "script", Lang: "starlark", Code: "def on_bar(ctx):\n    return buy(shares=100) if not ctx.has_position else None\n", Execution: domain.ExecutionSpec{FillAt: "next_open"}}}
	return NewStrategyService(nil, files), files, ctx, in
}

func TestStrategyValidationBindsWholeDefinitionAndSurvivesRestart(t *testing.T) {
	s, files, ctx, in := validationFixture(t)
	r, err := s.CheckAgent(ctx, in, nil)
	if err != nil || !r.Valid || !validationIDPattern.MatchString(r.ValidationID) {
		t.Fatal(r, err)
	}
	// A new service instance reads the same private, hash-verified receipt.
	s = NewStrategyService(nil, files)
	if err = s.verifyValidation(ctx, r.ValidationID, in); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*StrategyInput){
		func(x *StrategyInput) { x.Description = "added after check" }, func(x *StrategyInput) { x.Name = "different" },
		func(x *StrategyInput) { x.Body.Code = strings.Replace(x.Body.Code, "100", "200", 1) },
		func(x *StrategyInput) { x.Universe = []string{"sh.600519"} }, func(x *StrategyInput) { x.Period = domain.Bar1w },
		func(x *StrategyInput) { x.Body.Execution.FillAt = "close" },
	} {
		changed := in
		change(&changed)
		if err = s.verifyValidation(ctx, r.ValidationID, changed); !errors.Is(err, ErrStrategyValidationMismatch) {
			t.Fatal("changed input admitted", err)
		}
	}
	// Approval may resume in another Run of this same user/session.
	resumed := WithAgentExecution(context.Background(), domain.AgentToolAudit{ID: "new-call", RunID: "new-run", ActorID: "owner", SessionID: "session", ToolName: "create_strategy"}, domain.AgentApprovalReceipt{})
	if err = s.verifyValidation(resumed, r.ValidationID, in); err != nil {
		t.Fatal(err)
	}
	// Optional representational defaults do not force a spurious revalidation.
	in.Body.Lang = ""
	in.Body.Indicators = []domain.IndicatorSpec{}
	in.Body.Rules = []domain.RuleSpec{}
	if err = s.verifyValidation(ctx, r.ValidationID, in); err != nil {
		t.Fatal(err)
	}
	for _, audit := range []domain.AgentToolAudit{{ID: "x", ActorID: "other", SessionID: "session"}, {ID: "x", ActorID: "owner", SessionID: "other"}} {
		if err = s.verifyValidation(WithAgentExecution(context.Background(), audit, domain.AgentApprovalReceipt{}), r.ValidationID, in); !errors.Is(err, ErrStrategyValidationRequired) {
			t.Fatal("cross-scope receipt admitted", err)
		}
	}
	if err = s.verifyValidation(ctx, strings.Repeat("0", 64), in); !errors.Is(err, ErrStrategyValidationRequired) {
		t.Fatal(err)
	}
	body, err := files.Get(r.ValidationID + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var record strategyValidationRecord
	if err = sonic.Unmarshal(body, &record); err != nil {
		t.Fatal(err)
	}
	for _, revision := range []bool{false, true} {
		copy := record
		if revision {
			copy.CheckerRevision = "old"
		} else {
			copy.ExpiresAt = time.Now().Add(-time.Minute)
		}
		body, _ := sonic.Marshal(copy)
		ref, err := files.Put(body)
		if err != nil {
			t.Fatal(err)
		}
		err = s.verifyValidation(ctx, strings.TrimSuffix(ref, ".json"), in)
		if (!revision && !errors.Is(err, ErrStrategyValidationExpired)) || (revision && !errors.Is(err, ErrStrategyValidationMismatch)) {
			t.Fatal(err)
		}
	}
}

func TestStrategyValidationIssuesNoReceiptForFailedBehavior(t *testing.T) {
	s, _, ctx, in := validationFixture(t)
	r, err := s.CheckAgent(ctx, in, []string{script.DailyRoundTrip100})
	if err != nil || r.Valid || r.ValidationID != "" || r.ScriptReport.Smoke.Status != "passed" || r.ScriptReport.Behavior.Status != "failed" {
		t.Fatal(r, err)
	}
	in.Body.Code = "def on_bar(ctx):\n    return ctx.ma(3)"
	r, err = s.CheckAgent(ctx, in, nil)
	if err != nil || r.Valid || r.ValidationID != "" || r.ScriptReport.Diagnostics[0].Line != 2 {
		t.Fatal(r, err)
	}
	in.Body.Code = "def on_bar(ctx):\n    return None"
	r, err = NewStrategyService(nil).CheckAgent(ctx, in, nil)
	if !errors.Is(err, ErrToolUnavailable) || r.ValidationID != "" {
		t.Fatal("authenticated check silently lost durable store", r, err)
	}
}
