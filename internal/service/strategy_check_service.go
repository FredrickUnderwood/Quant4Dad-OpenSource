package service

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	appLog "github.com/quant4dad/internal/logger"
	"github.com/quant4dad/internal/script"
	"github.com/quant4dad/internal/utils/tooljson"
	"go.uber.org/zap"
)

var ErrStrategyValidationRequired = errors.New("strategy_validation_required")
var ErrStrategyValidationMismatch = errors.New("strategy_validation_mismatch")
var ErrStrategyValidationExpired = errors.New("strategy_validation_expired")
var validationIDPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

const strategyValidationLifetime = 30 * time.Minute

type StrategyCheckReport struct {
	Mode                string                 `json:"mode"`
	BehaviorContracts   []script.SuiteContract `json:"behavior_contracts"`
	Valid               bool                   `json:"valid"`
	Errors              []ToolValidationIssue  `json:"errors"`
	Checks              []string               `json:"checks"`
	Warnings            []string               `json:"warnings"`
	DefinitionDigest    string                 `json:"definition_digest,omitempty"`
	CheckerRevision     string                 `json:"checker_revision"`
	ScriptReport        *script.Report         `json:"script_report,omitempty"`
	ValidationID        string                 `json:"validation_id,omitempty"`
	ValidationExpiresAt string                 `json:"validation_expires_at,omitempty"`
}

// CheckAgent returns bounded evidence and issues an opaque, immutable record
// only after all requested checks pass. Ordinary local checks need no identity.
func (s *StrategyService) CheckAgent(ctx context.Context, in StrategyInput, suites []string) (StrategyCheckReport, error) {
	mode := "config"
	contracts := []script.SuiteContract{}
	if in.Body.IsScript() {
		mode, contracts = "script", script.SuiteContracts()
	}
	out := StrategyCheckReport{Mode: mode, BehaviorContracts: contracts, Errors: []ToolValidationIssue{}, Checks: []string{}, Warnings: []string{}, CheckerRevision: script.CheckerRevision}
	err := s.validateAgentStructure(ctx, in)
	if err == nil && script.ValidateSuites(suites) != nil {
		err = invalidField("behavior_tests", "Unknown or duplicate behavior test suite")
	}
	if err == nil && len(suites) > 0 && !in.Body.IsScript() {
		err = invalidField("behavior_tests", "Behavior suites require a Starlark script; configuration strategies are checked structurally")
	}
	if err != nil {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		out.Errors = ValidationIssues(err)
		return out, nil
	}
	out.Checks = append(out.Checks, "structure")
	if in.Body.IsScript() {
		r, err := script.Inspect(ctx, in.Body.Code, suites)
		out.ScriptReport = &r
		if r.Compile == "passed" {
			out.Checks = append(out.Checks, "starlark_compile")
		}
		if r.Smoke.Status == "passed" {
			out.Checks = append(out.Checks, "synthetic_smoke")
		}
		if r.Behavior.Status == "passed" {
			out.Checks = append(out.Checks, "behavior_tests")
		}
		out.Warnings = append(out.Warnings, "Synthetic smoke tests cover 64 bars and four position states, not every branch or longer warmups. No orders fill; this is not a real-data backtest. Branch coverage is not measured.")
		for _, d := range r.Diagnostics {
			if d.Severity == "warning" {
				out.Warnings = append(out.Warnings, d.Message)
			}
		}
		if len(suites) == 0 {
			out.Warnings = append(out.Warnings, "No behavior suite was requested; agreement with user intent remains unverified.")
		}
		if err != nil {
			if ctx.Err() != nil {
				return out, ctx.Err()
			}
			out.Errors = ValidationIssues(invalidField("body.code", err.Error()))
			return out, nil
		}
	}
	if in.Body.Execution.FillAt == "close" {
		out.Warnings = append(out.Warnings, "fill_at=close uses the signal bar's close for fills. Review same-bar execution assumptions; next_open fills at the following bar's open.")
	}
	digest, err := strategyDefinitionDigest(in)
	if err != nil {
		return out, ErrToolInput
	}
	out.Valid, out.DefinitionDigest = true, digest
	if execution, ok := AgentExecutionFromContext(ctx); ok {
		if s.validationFiles == nil || execution.Audit.ToolName != "validate_strategy" || execution.Audit.ActorID == "" || execution.Audit.SessionID == "" {
			return out, ErrToolUnavailable
		}
		expires := time.Now().UTC().Add(strategyValidationLifetime)
		record := strategyValidationRecord{Actor: execution.Audit.ActorID, Session: execution.Audit.SessionID, DefinitionDigest: digest, CheckerRevision: script.CheckerRevision, Passed: true, BehaviorSuites: append([]string{}, suites...), ExpiresAt: expires}
		body, err := sonic.Marshal(record)
		if err != nil {
			return out, ErrToolUnavailable
		}
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		ref, err := s.validationFiles.Put(body)
		if err != nil {
			return out, ErrToolUnavailable
		}
		out.ValidationID = strings.TrimSuffix(ref, ".json")
		out.ValidationExpiresAt = expires.Format(time.RFC3339Nano)
	}
	return out, nil
}

type strategyValidationRecord struct {
	Actor            string    `json:"actor"`
	Session          string    `json:"session"`
	DefinitionDigest string    `json:"definition_digest"`
	CheckerRevision  string    `json:"checker_revision"`
	Passed           bool      `json:"passed"`
	BehaviorSuites   []string  `json:"behavior_suites"`
	ExpiresAt        time.Time `json:"expires_at"`
}

// JSON object order and optional defaults are normalized, never source text or
// actual field values. Description, name, universe and execution all participate.
func strategyDefinitionDigest(in StrategyInput) (string, error) {
	in.ExpectedVersion = nil // The mutation's target/version is independently approval-bound.
	if in.Body.IsScript() {
		if in.Body.Lang == "" {
			in.Body.Lang = "starlark"
		}
		if len(in.Body.Indicators) == 0 {
			in.Body.Indicators = nil
		}
		if len(in.Body.Rules) == 0 {
			in.Body.Rules = nil
		}
	} else if in.Body.Mode == "" {
		in.Body.Mode = "config"
	}
	body, err := sonic.Marshal(in)
	if err != nil {
		return "", err
	}
	return tooljson.Hash(body)
}

func (s *StrategyService) verifyValidation(ctx context.Context, id string, in StrategyInput) error {
	execution, ok := AgentExecutionFromContext(ctx)
	if !ok || !validationIDPattern.MatchString(id) || s.validationFiles == nil {
		return ErrStrategyValidationRequired
	}
	body, err := s.validationFiles.Get(id + ".json")
	if err != nil {
		return ErrStrategyValidationRequired
	}
	var record strategyValidationRecord
	if sonic.Unmarshal(body, &record) != nil || !record.Passed || record.Actor != execution.Audit.ActorID || record.Session != execution.Audit.SessionID {
		return ErrStrategyValidationRequired
	}
	if !time.Now().Before(record.ExpiresAt) {
		return ErrStrategyValidationExpired
	}
	digest, err := strategyDefinitionDigest(in)
	if err != nil || record.CheckerRevision != script.CheckerRevision || record.DefinitionDigest != digest {
		return ErrStrategyValidationMismatch
	}
	appLog.Info(ctx, "agent strategy validation bound", zap.String("run_id", execution.Audit.RunID), zap.String("tool_call_id", execution.Audit.ToolCallID), zap.String("definition_digest", digest), zap.String("checker_revision", record.CheckerRevision))
	return nil
}

func (s *StrategyService) Maintain(ctx context.Context, now time.Time) (int, error) {
	if s.validationFiles == nil {
		return 0, nil
	}
	return s.validationFiles.Sweep(ctx, now, func(context.Context, string) (bool, error) { return false, nil })
}
