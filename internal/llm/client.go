// Package llm provides a uniform abstraction for calling LLMs across providers. The event
// pipeline's AI nodes resolve a Client by provider name through a Factory and then call
// Complete, which hides the differences between vendors.
package llm

import (
	"context"
	"fmt"
	"strings"
)

// CompleteRequest is one completion request. Prompt is the rendered user prompt and System is
// optional. With JSONOutput=true the model is asked for JSON as far as the provider allows:
// OpenAI through response_format, Anthropic through prompt constraints.
type CompleteRequest struct {
	Model       string
	System      string
	Prompt      string
	MaxTokens   int
	Temperature float64
	JSONOutput  bool
}

// CompleteResponse is one completion result. Text is the model's output; the token counts are
// persisted for accounting.
type CompleteResponse struct {
	Text             string
	Model            string
	TokensPrompt     int
	TokensCompletion int
}

// Client is one provider's client.
type Client interface {
	Provider() string
	Complete(ctx context.Context, req CompleteRequest) (CompleteResponse, error)
}

// ProviderConfig describes one provider's connection parameters, sourced from config.
type ProviderConfig struct {
	Type         string // anthropic / openai (openai-compatible, covering Ollama and assorted gateways)
	BaseURL      string
	APIKey       string
	DefaultModel string
}

// buildClient builds one provider client from config. With an empty type it infers from the
// provider name: a name containing "claude" or "anthropic" means anthropic, anything else is
// treated as openai-compatible.
func buildClient(name string, c ProviderConfig) (Client, error) {
	typ := strings.ToLower(strings.TrimSpace(c.Type))
	if typ == "" {
		if strings.Contains(strings.ToLower(name), "claude") || strings.Contains(strings.ToLower(name), "anthropic") {
			typ = "anthropic"
		} else {
			typ = "openai"
		}
	}
	switch typ {
	case "anthropic":
		return newAnthropicClient(name, c), nil
	case "openai":
		return newOpenAIClient(name, c), nil
	default:
		return nil, fmt.Errorf("llm: unknown provider type %q for %q", c.Type, name)
	}
}

// ProviderSource returns the currently available provider configs when called. Driven by the
// setting table, every resolve reads the latest config, so UI changes take effect at once.
type ProviderSource func(ctx context.Context) (map[string]ProviderConfig, error)

// DynamicFactory reads the latest provider config through ProviderSource on every resolve and
// builds the client on demand, which is what lets LLM settings be maintained from the UI at
// runtime, with no restart and no config file edit.
type DynamicFactory struct {
	source ProviderSource
}

func NewDynamicFactory(source ProviderSource) *DynamicFactory {
	return &DynamicFactory{source: source}
}

// Resolve implements the AI node's resolver interface: look up the config by provider name
// and build the client.
func (f *DynamicFactory) Resolve(ctx context.Context, provider string) (Client, error) {
	cfgs, err := f.source(ctx)
	if err != nil {
		return nil, fmt.Errorf("llm: load provider settings: %w", err)
	}
	c, ok := cfgs[provider]
	if !ok {
		return nil, fmt.Errorf("llm: provider %q not configured (add it under Settings)", provider)
	}
	return buildClient(provider, c)
}
