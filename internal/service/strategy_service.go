package service

import (
	"context"
	"errors"
	"strconv"

	"github.com/bytedance/sonic"

	"go.uber.org/zap"

	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/expr"
	"github.com/quant4dad/internal/indicator"
	"github.com/quant4dad/internal/logger"
	"github.com/quant4dad/internal/repository"
	"github.com/quant4dad/internal/script"
)

type StrategyService struct {
	repo            *repository.StrategyRepository
	validationFiles *repository.AgentToolArtifactRepository
}

func NewStrategyService(repo *repository.StrategyRepository, validationFiles ...*repository.AgentToolArtifactRepository) *StrategyService {
	s := &StrategyService{repo: repo}
	if len(validationFiles) > 0 {
		s.validationFiles = validationFiles[0]
	}
	return s
}

type StrategyInput struct {
	ExpectedVersion *int                `json:"expected_version,omitempty"`
	Name            string              `json:"name"`
	Description     string              `json:"description"`
	Universe        []string            `json:"universe"`
	Period          domain.BarPeriod    `json:"period"`
	Body            domain.StrategyBody `json:"body"`
}

func (s *StrategyService) Create(ctx context.Context, in StrategyInput) (*domain.Strategy, error) {
	if err := s.validate(in); err != nil {
		return nil, err
	}
	body, err := sonic.Marshal(in.Body)
	if err != nil {
		return nil, err
	}
	st := &domain.Strategy{
		Name:        in.Name,
		Description: in.Description,
		Universe:    domain.StringSlice(in.Universe),
		Period:      in.Period,
		Body:        body,
	}
	if err := s.repo.Create(ctx, st); err != nil {
		return nil, err
	}
	logger.L().Info("strategy created", zap.Int64("id", st.ID), zap.String("name", st.Name))
	return st, nil
}

func (s *StrategyService) Update(ctx context.Context, id int64, in StrategyInput) (*domain.Strategy, error) {
	st, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if in.ExpectedVersion != nil && *in.ExpectedVersion != st.Version {
		return nil, domain.ErrResourceConflict
	}
	if err := s.validate(in); err != nil {
		return nil, err
	}
	body, err := sonic.Marshal(in.Body)
	if err != nil {
		return nil, err
	}
	st.Name = in.Name
	st.Description = in.Description
	st.Universe = domain.StringSlice(in.Universe)
	st.Period = in.Period
	st.Body = body
	if err := s.repo.Update(ctx, st); err != nil {
		return nil, err
	}
	logger.L().Info("strategy updated", zap.Int64("id", id))
	return st, nil
}

func (s *StrategyService) GetByID(ctx context.Context, id int64) (*domain.Strategy, error) {
	return s.repo.GetByID(ctx, id)
}

func (s *StrategyService) Delete(ctx context.Context, id int64) error {
	if err := s.repo.Delete(ctx, id); err != nil {
		return err
	}
	logger.L().Info("strategy deleted", zap.Int64("id", id))
	return nil
}

func (s *StrategyService) List(ctx context.Context) ([]*domain.Strategy, error) {
	return s.repo.List(ctx)
}

// ValidateAgent applies the same checks used by Agent validation, persistence
// and backtest admission. Script probes use synthetic data and never persist.
func (s *StrategyService) ValidateAgent(in StrategyInput) error {
	return s.ValidateAgentContext(context.Background(), in)
}

func (s *StrategyService) ValidateAgentContext(ctx context.Context, in StrategyInput) error {
	if err := s.validateAgentStructure(ctx, in); err != nil {
		return err
	}
	if in.Body.IsScript() {
		if err := script.Check(ctx, in.Body.Code); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return invalidField("body.code", err.Error())
		}
	}
	return nil
}

func (s *StrategyService) validateAgentStructure(ctx context.Context, in StrategyInput) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(in.Name) > 128 || len(in.Description) > 4096 || len(in.Universe) < 1 || len(in.Universe) > 20 || len(in.Body.Indicators) > 32 || len(in.Body.Rules) > 32 {
		return ErrToolInput
	}
	for _, code := range in.Universe {
		if !ValidInstrumentCode(code) {
			return ErrToolInput
		}
	}
	if err := validateStrategyMetadata(in); err != nil {
		return err
	}
	if in.Body.Execution.FillAt != "" && in.Body.Execution.FillAt != "next_open" && in.Body.Execution.FillAt != "close" {
		return invalidField("body.execution.fill_at", "Expected next_open or close")
	}
	if in.Body.IsScript() {
		if in.Body.Lang != "" && in.Body.Lang != "starlark" {
			return invalidField("body.lang", "Script strategies support only starlark")
		}
		if len(in.Body.Indicators) != 0 || len(in.Body.Rules) != 0 {
			return invalidField("body", "Script mode computes its own signals; omit indicators and rules")
		}
		return nil
	}
	if in.Body.Mode != "" && in.Body.Mode != "config" {
		return invalidField("body.mode", "Expected config or script")
	}
	if in.Body.Code != "" || in.Body.Lang != "" {
		return invalidField("body", "Code and lang require mode=script")
	}
	if err := s.validate(in); err != nil {
		var issue *ToolValidationIssue
		if errors.As(err, &issue) {
			return issue
		}
		return invalidField("strategy", "Invalid strategy configuration")
	}
	return nil
}

// validate enforces structural integrity:
//   - non-empty name and universe
//   - period in 1d/1w/1mo
//   - unique alias per indicator and recognized indicator type
//   - rule names unique and non-empty
//   - every alias referenced by a rule's `when` is declared
//   - conditions return boolean and all operands have compatible types
func (s *StrategyService) validate(in StrategyInput) error {
	if err := validateStrategyMetadata(in); err != nil {
		return err
	}
	// Script mode: validate by compiling the Starlark program (syntax + on_bar
	// presence). Indicators/rules are unused in this mode.
	if in.Body.IsScript() {
		if in.Body.Lang != "" && in.Body.Lang != "starlark" {
			return errors.New("unsupported script lang: " + in.Body.Lang)
		}
		if _, err := script.Compile(in.Body.Code); err != nil {
			return err
		}
		return nil
	}
	aliases := map[string]struct{}{}
	for i, ind := range in.Body.Indicators {
		path := "body.indicators[" + strconv.Itoa(i) + "]"
		if ind.Alias == "" {
			return invalidField(path+".alias", "Indicator alias is required")
		}
		if _, dup := aliases[ind.Alias]; dup {
			return invalidField(path+".alias", "Duplicate indicator alias: "+ind.Alias)
		}
		aliases[ind.Alias] = struct{}{}
		if _, err := indicator.Get(ind.Type); err != nil {
			return invalidField(path+".type", "Unknown indicator type")
		}
		if err := indicator.ValidateParams(ind.Type, ind.Params); err != nil {
			return invalidField(path+".params", err.Error())
		}
	}
	ruleNames := map[string]struct{}{}
	for i, r := range in.Body.Rules {
		path := "body.rules[" + strconv.Itoa(i) + "]"
		if r.Name == "" {
			return invalidField(path+".name", "Rule name is required")
		}
		if _, dup := ruleNames[r.Name]; dup {
			return invalidField(path+".name", "Duplicate rule name: "+r.Name)
		}
		ruleNames[r.Name] = struct{}{}
		node, err := expr.Parse(r.When)
		if err != nil {
			return invalidField(path+".when", err.Error())
		}
		if err := expr.ValidateCondition(node); err != nil {
			return invalidField(path+".when", err.Error())
		}
		for _, ref := range expr.CollectRefs(node) {
			if _, ok := aliases[ref]; !ok {
				return invalidField(path+".when", "Undeclared indicator alias: "+ref)
			}
		}
		if r.Then.Action != string(domain.TradeSideBuy) && r.Then.Action != string(domain.TradeSideSell) {
			return invalidField(path+".then.action", "Expected buy or sell")
		}
	}
	return nil
}

func validateStrategyMetadata(in StrategyInput) error {
	if in.Name == "" {
		return invalidField("name", "Strategy name is required")
	}
	if len(in.Universe) == 0 {
		return invalidField("universe", "At least one instrument is required")
	}
	switch in.Period {
	case domain.Bar1d, domain.Bar1w, domain.Bar1mo:
		return nil
	default:
		return invalidField("period", "Expected 1d, 1w or 1mo")
	}
}
