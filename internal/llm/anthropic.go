package llm

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bytedance/sonic"
)

const defaultAnthropicBaseURL = "https://api.anthropic.com"

// anthropicClient calls the Anthropic Messages API.
type anthropicClient struct {
	name    string
	baseURL string
	apiKey  string
	model   string
	http    *http.Client
}

func newAnthropicClient(name string, c ProviderConfig) *anthropicClient {
	base := strings.TrimRight(c.BaseURL, "/")
	if base == "" {
		base = defaultAnthropicBaseURL
	}
	return &anthropicClient{
		name:    name,
		baseURL: base,
		apiKey:  c.APIKey,
		model:   c.DefaultModel,
		http:    &http.Client{Timeout: 60 * time.Second},
	}
}

func (a *anthropicClient) Provider() string { return a.name }

type anthropicReq struct {
	Model       string             `json:"model"`
	MaxTokens   int                `json:"max_tokens"`
	System      string             `json:"system,omitempty"`
	Temperature float64            `json:"temperature,omitempty"`
	Messages    []anthropicMessage `json:"messages"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicResp struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

func (a *anthropicClient) Complete(ctx context.Context, req CompleteRequest) (CompleteResponse, error) {
	model := req.Model
	if model == "" {
		model = a.model
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 1024
	}
	system := req.System
	if req.JSONOutput {
		// Anthropic has no response_format, so the system prompt is what constrains it to emit
		// JSON only.
		system = strings.TrimSpace(system + "\nReturn ONLY a valid JSON object, no markdown fences, no commentary.")
	}

	body, err := sonic.Marshal(anthropicReq{
		Model:       model,
		MaxTokens:   maxTokens,
		System:      system,
		Temperature: req.Temperature,
		Messages:    []anthropicMessage{{Role: "user", Content: req.Prompt}},
	})
	if err != nil {
		return CompleteResponse{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return CompleteResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", a.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	resp, err := a.http.Do(httpReq)
	if err != nil {
		return CompleteResponse{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	var out anthropicResp
	if err := sonic.Unmarshal(raw, &out); err != nil {
		return CompleteResponse{}, fmt.Errorf("anthropic: decode response: %w (status %d)", err, resp.StatusCode)
	}
	if resp.StatusCode >= 300 || out.Error != nil {
		msg := "unknown error"
		if out.Error != nil {
			msg = out.Error.Message
		}
		return CompleteResponse{}, fmt.Errorf("anthropic: status %d: %s", resp.StatusCode, msg)
	}

	var sb strings.Builder
	for _, c := range out.Content {
		if c.Type == "text" {
			sb.WriteString(c.Text)
		}
	}
	return CompleteResponse{
		Text:             sb.String(),
		Model:            model,
		TokensPrompt:     out.Usage.InputTokens,
		TokensCompletion: out.Usage.OutputTokens,
	}, nil
}
