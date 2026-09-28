// Package nodes implements the event pipeline's built-in node processors (keyword
// filtering, AI analysis, and so on). Each processor implements pipeline.Processor and is
// registered into the Registry during wiring.
package nodes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/bytedance/sonic"

	"github.com/quant4dad/internal/pipeline"
)

// List types: a blacklist blocks on a hit, a whitelist blocks on a miss.
const (
	listBlacklist = "blacklist"
	listWhitelist = "whitelist"
)

// keywordConfig is the keyword filter node's config. Aimed at non-technical users, it
// keeps only two obvious options: the keyword list and the list type. The matching rule is
// fixed: case-insensitive substring containment, and matching any one keyword counts as a
// hit.
type keywordConfig struct {
	Keywords []string `json:"keywords"`  // keywords to match, one per line
	ListType string   `json:"list_type"` // blacklist (the default, blocks on a hit) / whitelist (blocks on a miss)
}

// cleanKeywords strips whitespace and blank lines, leaving the keywords that actually
// take part in matching.
func (c keywordConfig) cleanKeywords() []string {
	out := make([]string, 0, len(c.Keywords))
	for _, kw := range c.Keywords {
		if s := strings.TrimSpace(kw); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// KeywordFilter decides whether a message continues, according to the list type: a
// blacklist drops on a keyword hit, a whitelist drops on a miss. Dropping returns
// ActionDrop and the engine short-circuits the nodes downstream.
type KeywordFilter struct{}

func NewKeywordFilter() *KeywordFilter { return &KeywordFilter{} }

func (*KeywordFilter) Type() string        { return "keyword_filter" }
func (*KeywordFilter) DisplayName() string { return "关键词过滤" }
func (*KeywordFilter) Category() string    { return "filter" }

func (*KeywordFilter) ConfigSchema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "required": ["keywords"],
  "properties": {
    "keywords": {"type": "array", "items": {"type": "string"}, "title": "关键词", "description": "每行一个关键词，匹配时不区分大小写"},
    "list_type": {
      "type": "string",
      "enum": ["blacklist", "whitelist"],
      "enumNames": ["黑名单：命中关键词就拦截", "白名单：只保留命中关键词的消息"],
      "default": "blacklist",
      "title": "名单类型"
    }
  }
}`)
}

func (*KeywordFilter) Validate(raw json.RawMessage) error {
	cfg, err := parseKeywordConfig(raw)
	if err != nil {
		return err
	}
	if len(cfg.cleanKeywords()) == 0 {
		return errors.New("keyword_filter: keywords must not be empty")
	}
	return nil
}

func (*KeywordFilter) Process(_ context.Context, rc *pipeline.RunContext, raw json.RawMessage) (pipeline.Action, error) {
	cfg, err := parseKeywordConfig(raw)
	if err != nil {
		return pipeline.ActionPass, err
	}

	hit := hitAny(cfg.cleanKeywords(), payloadText(rc.Msg.Payload))

	// Whitelist: block on a miss. Blacklist: block on a hit.
	if cfg.ListType == listWhitelist {
		if !hit {
			return pipeline.ActionDrop, nil
		}
		return pipeline.ActionPass, nil
	}
	if hit {
		return pipeline.ActionDrop, nil
	}
	return pipeline.ActionPass, nil
}

// hitAny reports whether the text matches any keyword, case-insensitively.
func hitAny(keywords []string, text string) bool {
	lower := strings.ToLower(text)
	for _, kw := range keywords {
		if strings.Contains(lower, strings.ToLower(kw)) {
			return true
		}
	}
	return false
}

// payloadText serializes the whole payload to a string for matching.
func payloadText(payload map[string]any) string {
	b, _ := sonic.MarshalString(payload)
	return b
}

func parseKeywordConfig(raw json.RawMessage) (keywordConfig, error) {
	var cfg keywordConfig
	if len(raw) == 0 {
		return cfg, errors.New("keyword_filter: config is empty")
	}
	if err := sonic.Unmarshal(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("keyword_filter: failed to parse config: %w", err)
	}
	if cfg.ListType == "" {
		cfg.ListType = listBlacklist
	}
	return cfg, nil
}
