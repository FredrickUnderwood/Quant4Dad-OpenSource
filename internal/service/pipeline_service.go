package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/logger"
	"github.com/quant4dad/internal/pipeline"
	"github.com/quant4dad/internal/repository"
)

type PipelineService struct {
	repo *repository.PipelineRepository
	reg  *pipeline.Registry
	exec *pipeline.Executor
}

func NewPipelineService(repo *repository.PipelineRepository, reg *pipeline.Registry) *PipelineService {
	return &PipelineService{repo: repo, reg: reg, exec: pipeline.NewExecutor(reg)}
}

type NodeInput struct {
	NodeKey string          `json:"node_key"`
	Type    string          `json:"type"`
	Name    string          `json:"name"`
	Config  json.RawMessage `json:"config"`
	PosX    int             `json:"pos_x"`
	PosY    int             `json:"pos_y"`
}

type EdgeInput struct {
	FromNodeKey string          `json:"from_node_key"`
	ToNodeKey   string          `json:"to_node_key"`
	Condition   json.RawMessage `json:"condition,omitempty"`
}

type PipelineInput struct {
	ExpectedVersion *int        `json:"expected_version,omitempty"`
	Name            string      `json:"name"`
	Description     string      `json:"description"`
	Status          string      `json:"status"`
	Nodes           []NodeInput `json:"nodes"`
	Edges           []EdgeInput `json:"edges"`
	// Sources lists the subscribed news sources; arriving news is fed into this
	// pipeline as an event automatically.
	Sources []string `json:"sources"`
}

func (s *PipelineService) NodeTypes() []pipeline.NodeMeta { return s.reg.Metas() }

// ValidateAgent parses configuration only; it never calls a processor.
func (s *PipelineService) ValidateAgent(in PipelineInput) error {
	if len(in.Name) > 128 || len(in.Description) > 4096 || len(in.Nodes) > 64 || len(in.Edges) > 128 || len(in.Sources) > 32 {
		return ErrToolInput
	}
	switch in.Status {
	case "", domain.PipelineStatusDraft, domain.PipelineStatusEnabled, domain.PipelineStatusDisabled:
	default:
		return ErrToolInput
	}
	if err := s.validate(in); err != nil {
		return ErrToolInput
	}
	return nil
}

// Preview validates an unsaved DAG without writing product data or sending.
func (s *PipelineService) Preview(ctx context.Context, in PipelineInput, sample map[string]any) (pipeline.ExecResult, error) {
	if len(in.Nodes) > 64 || len(in.Edges) > 128 {
		return pipeline.ExecResult{}, errors.New("pipeline_preview_limit")
	}
	if err := s.validate(in); err != nil {
		return pipeline.ExecResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	nodes, edges := ToEngine(toDomain(in))
	return s.exec.Run(ctx, nodes, edges, &pipeline.Message{Payload: sample, Meta: map[string]any{"dry_run": true}}, true), nil
}

func (s *PipelineService) Create(ctx context.Context, in PipelineInput) (*domain.Pipeline, error) {
	if err := s.validate(in); err != nil {
		return nil, err
	}
	p := toDomain(in)
	if p.Status == "" {
		p.Status = domain.PipelineStatusDraft
	}
	if err := s.repo.Create(ctx, p); err != nil {
		return nil, err
	}
	logger.L().Info("pipeline created", zap.Int64("id", p.ID), zap.String("name", p.Name))
	return s.repo.GetByID(ctx, p.ID)
}

func (s *PipelineService) Update(ctx context.Context, id int64, in PipelineInput) (*domain.Pipeline, error) {
	previous, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if in.ExpectedVersion != nil && *in.ExpectedVersion != previous.Version {
		return nil, domain.ErrResourceConflict
	}
	if err := s.validate(in); err != nil {
		return nil, err
	}
	p := toDomain(in)
	p.ID = id
	p.Version = previous.Version
	if p.Status == "" {
		p.Status = domain.PipelineStatusDraft
	}
	if err := s.repo.Update(ctx, p); err != nil {
		return nil, err
	}
	logger.L().Info("pipeline updated", zap.Int64("id", id))
	return s.repo.GetByID(ctx, id)
}

func (s *PipelineService) GetByID(ctx context.Context, id int64) (*domain.Pipeline, error) {
	return s.repo.GetByID(ctx, id)
}

func (s *PipelineService) List(ctx context.Context) ([]*domain.Pipeline, error) {
	return s.repo.List(ctx)
}

func (s *PipelineService) SetStatus(ctx context.Context, id int64, status string, expected ...*int) error {
	switch status {
	case domain.PipelineStatusEnabled, domain.PipelineStatusDisabled, domain.PipelineStatusDraft:
	default:
		return errors.New("invalid status, expect enabled / disabled / draft")
	}
	p, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if len(expected) > 0 && expected[0] != nil && *expected[0] != p.Version {
		return domain.ErrResourceConflict
	}
	if err := s.repo.UpdateStatus(ctx, id, status, p.Version); err != nil {
		return err
	}
	logger.L().Info("pipeline status changed", zap.Int64("id", id), zap.String("status", status))
	return nil
}

func (s *PipelineService) Delete(ctx context.Context, id int64) error {
	if err := s.repo.Delete(ctx, id); err != nil {
		return err
	}
	logger.L().Info("pipeline deleted", zap.Int64("id", id))
	return nil
}

// DryRun runs the pipeline against a sample event and returns the per-node trace
// without persisting execution results or sending delivery notifications.
func (s *PipelineService) DryRun(ctx context.Context, id int64, sample map[string]any) (pipeline.ExecResult, error) {
	p, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return pipeline.ExecResult{}, err
	}
	nodes, edges := ToEngine(p)
	msg := &pipeline.Message{Payload: sample, Meta: map[string]any{"dry_run": true}}
	return s.exec.Run(ctx, nodes, edges, msg, true), nil
}

// validate checks the pipeline's structure: the name, node keys being unique and of
// registered types, node configs being valid, edges referencing existing nodes, and
// the graph being acyclic.
func (s *PipelineService) validate(in PipelineInput) error {
	if in.Name == "" {
		return errors.New("pipeline name required")
	}
	if len(in.Nodes) == 0 {
		return errors.New("pipeline must have at least one node")
	}
	keys := map[string]struct{}{}
	for _, n := range in.Nodes {
		if n.NodeKey == "" {
			return errors.New("node_key required")
		}
		if _, dup := keys[n.NodeKey]; dup {
			return fmt.Errorf("duplicate node_key: %s", n.NodeKey)
		}
		keys[n.NodeKey] = struct{}{}
		if err := s.reg.ValidateNode(n.Type, n.Config); err != nil {
			return fmt.Errorf("node %q: %w", n.NodeKey, err)
		}
	}
	for _, e := range in.Edges {
		if _, ok := keys[e.FromNodeKey]; !ok {
			return fmt.Errorf("edge references unknown node: %s", e.FromNodeKey)
		}
		if _, ok := keys[e.ToNodeKey]; !ok {
			return fmt.Errorf("edge references unknown node: %s", e.ToNodeKey)
		}
		if _, err := pipeline.ParseCondition(e.Condition); err != nil {
			return fmt.Errorf("edge %s→%s: %w", e.FromNodeKey, e.ToNodeKey, err)
		}
	}
	for _, src := range in.Sources {
		if !IsKnownNewsSource(src) {
			return fmt.Errorf("unknown news source %q, registered sources: %v", src, knownNewsSources)
		}
	}
	// Reuse the engine's topological sort to detect cycles.
	nodes := make([]pipeline.Node, 0, len(in.Nodes))
	for _, n := range in.Nodes {
		nodes = append(nodes, pipeline.Node{Key: n.NodeKey, Type: n.Type, Config: n.Config})
	}
	edges := make([]pipeline.Edge, 0, len(in.Edges))
	for _, e := range in.Edges {
		edges = append(edges, pipeline.Edge{From: e.FromNodeKey, To: e.ToNodeKey})
	}
	if _, err := pipeline.TopoCheck(nodes, edges); err != nil {
		return err
	}
	return nil
}

func toDomain(in PipelineInput) *domain.Pipeline {
	p := &domain.Pipeline{
		Name:        in.Name,
		Description: in.Description,
		Status:      in.Status,
		Sources:     in.Sources,
	}
	for _, n := range in.Nodes {
		p.Nodes = append(p.Nodes, domain.PipelineNode{
			NodeKey: n.NodeKey,
			Type:    n.Type,
			Name:    n.Name,
			Config:  n.Config,
			PosX:    n.PosX,
			PosY:    n.PosY,
		})
	}
	for _, e := range in.Edges {
		p.Edges = append(p.Edges, domain.PipelineEdge{
			FromNodeKey: e.FromNodeKey,
			ToNodeKey:   e.ToNodeKey,
			Condition:   e.Condition,
		})
	}
	return p
}

// ToEngine maps a domain pipeline onto the execution engine's nodes and edges.
func ToEngine(p *domain.Pipeline) ([]pipeline.Node, []pipeline.Edge) {
	nodes := make([]pipeline.Node, 0, len(p.Nodes))
	for _, n := range p.Nodes {
		nodes = append(nodes, pipeline.Node{Key: n.NodeKey, Type: n.Type, Config: n.Config})
	}
	edges := make([]pipeline.Edge, 0, len(p.Edges))
	for _, e := range p.Edges {
		edges = append(edges, pipeline.Edge{From: e.FromNodeKey, To: e.ToNodeKey, Condition: e.Condition})
	}
	return nodes, edges
}
