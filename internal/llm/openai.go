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

const defaultOpenAIBaseURL = "https://api.openai.com/v1"

// openAIClient calls the OpenAI-compatible Chat Completions API. That protocol is widely
// supported — by OpenAI, Ollama at /v1, and assorted gateways — so one implementation covers
// many providers, with nothing to change but base_url.
type openAIClient struct {
	name    string
	baseURL string
	apiKey  string
	model   string
	http    *http.Client
}

func newOpenAIClient(name string, c ProviderConfig) *openAIClient {
	base := strings.TrimRight(c.BaseURL, "/")
	if base == "" {
		base = defaultOpenAIBaseURL
	}
	return &openAIClient{
		name:    name,
		baseURL: base,
		apiKey:  c.APIKey,
		model:   c.DefaultModel,
		http:    &http.Client{Timeout: 60 * time.Second},
	}
}

func (o *openAIClient) Provider() string { return o.name }

type openAIReq struct {
	Model          string          `json:"model"`
	Messages       []openAIMessage `json:"messages"`
	Temperature    float64         `json:"temperature,omitempty"`
	MaxTokens      int             `json:"max_tokens,omitempty"`
	ResponseFormat *openAIRespFmt  `json:"response_format,omitempty"`
}

type openAIRespFmt struct {
	Type string `json:"type"`
}

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIResp struct {
	Choices []struct {
		Message openAIMessage `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (o *openAIClient) Complete(ctx context.Context, req CompleteRequest) (CompleteResponse, error) {
	model := req.Model
	if model == "" {
		model = o.model
	}

	msgs := make([]openAIMessage, 0, 2)
	if strings.TrimSpace(req.System) != "" {
		msgs = append(msgs, openAIMessage{Role: "system", Content: req.System})
	}
	msgs = append(msgs, openAIMessage{Role: "user", Content: req.Prompt})

	payload := openAIReq{
		Model:       model,
		Messages:    msgs,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
	}
	if req.JSONOutput {
		payload.ResponseFormat = &openAIRespFmt{Type: "json_object"}
	}

	body, err := sonic.Marshal(payload)
	if err != nil {
		return CompleteResponse{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return CompleteResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if o.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+o.apiKey)
	}

	resp, err := o.http.Do(httpReq)
	if err != nil {
		return CompleteResponse{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	var out openAIResp
	if err := sonic.Unmarshal(raw, &out); err != nil {
		return CompleteResponse{}, fmt.Errorf("openai: decode response: %w (status %d)", err, resp.StatusCode)
	}
	if resp.StatusCode >= 300 || out.Error != nil {
		msg := "unknown error"
		if out.Error != nil {
			msg = out.Error.Message
		}
		return CompleteResponse{}, fmt.Errorf("openai: status %d: %s", resp.StatusCode, msg)
	}
	if len(out.Choices) == 0 {
		return CompleteResponse{}, fmt.Errorf("openai: empty choices")
	}

	return CompleteResponse{
		Text:             out.Choices[0].Message.Content,
		Model:            model,
		TokensPrompt:     out.Usage.PromptTokens,
		TokensCompletion: out.Usage.CompletionTokens,
	}, nil
}
