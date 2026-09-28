package nodes

import "github.com/quant4dad/internal/pipeline"

// BuildRegistry registers every built-in node processor. Adding a node type means adding
// one Register line here. resolver resolves the LLM client for AI nodes; notifier resolves
// the outbound channel for delivery nodes.
func BuildRegistry(resolver Resolver, notifier Notifier) *pipeline.Registry {
	reg := pipeline.NewRegistry()
	reg.Register(NewKeywordFilter())
	reg.Register(NewAIAnalysis(resolver))
	reg.Register(NewDelivery(notifier))
	return reg
}
