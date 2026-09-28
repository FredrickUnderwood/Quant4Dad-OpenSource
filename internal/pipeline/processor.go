package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
)

// Processor is the common interface for every node. Adding a node type means
// implementing this interface and registering it with the Registry — open for extension.
type Processor interface {
	// Type returns the node type identifier, e.g. "keyword_filter".
	Type() string
	// ConfigSchema returns the JSON Schema for the node's config, served to the UI to
	// render the config form dynamically.
	ConfigSchema() json.RawMessage
	// Validate checks whether one node's config is valid; called when saving a pipeline.
	Validate(cfg json.RawMessage) error
	// Process handles one message. It may read and write rc.Msg.Payload, and returns the
	// routing decision.
	Process(ctx context.Context, rc *RunContext, cfg json.RawMessage) (Action, error)
}

// NodeMeta is the node type metadata served to the UI.
type NodeMeta struct {
	Type         string          `json:"type"`
	Name         string          `json:"name"`
	Category     string          `json:"category"`
	ConfigSchema json.RawMessage `json:"config_schema"`
}

// Describer lets a node optionally supply display metadata (name, category). Defaults
// are used when it is not implemented.
type Describer interface {
	DisplayName() string
	Category() string
}

// Registry is the node type registry.
type Registry struct {
	m map[string]Processor
}

func NewRegistry() *Registry { return &Registry{m: make(map[string]Processor)} }

// Register records a node processor; a duplicate type panics, surfacing the programming
// error at startup.
func (r *Registry) Register(p Processor) {
	t := p.Type()
	if _, dup := r.m[t]; dup {
		panic("pipeline: duplicate processor type " + t)
	}
	r.m[t] = p
}

func (r *Registry) Get(t string) (Processor, bool) {
	p, ok := r.m[t]
	return p, ok
}

// Metas returns the metadata for every registered node type, for the node-types endpoint.
func (r *Registry) Metas() []NodeMeta {
	out := make([]NodeMeta, 0, len(r.m))
	for t, p := range r.m {
		meta := NodeMeta{Type: t, Name: t, Category: "general", ConfigSchema: p.ConfigSchema()}
		if d, ok := p.(Describer); ok {
			meta.Name = d.DisplayName()
			meta.Category = d.Category()
		}
		out = append(out, meta)
	}
	return out
}

// ValidateNode checks one node's type and config.
func (r *Registry) ValidateNode(nodeType string, cfg json.RawMessage) error {
	p, ok := r.Get(nodeType)
	if !ok {
		return fmt.Errorf("unknown node type: %s", nodeType)
	}
	return p.Validate(cfg)
}
