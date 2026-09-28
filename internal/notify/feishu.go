package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/bytedance/sonic"
)

// feishuBrRe matches the HTML line-break tags <br>, <br/> and <br />, case-insensitively, all
// of which are treated as a newline.
var feishuBrRe = regexp.MustCompile(`(?i)<br\s*/?>`)

// feishuHTTP is the shared client for Feishu webhook delivery, with a timeout so it cannot
// stall the execution chain.
var feishuHTTP = &http.Client{Timeout: 15 * time.Second}

// feishuSender sends rich text (post) messages through a Feishu custom bot webhook.
type feishuSender struct {
	cfg FeishuConfig
}

func newFeishuSender(cfg FeishuConfig) *feishuSender { return &feishuSender{cfg: cfg} }

func (*feishuSender) Channel() string { return ChannelFeishu }

func (s *feishuSender) Send(ctx context.Context, msg Message) error {
	body, err := s.buildBody(msg)
	if err != nil {
		return fmt.Errorf("notify/feishu: failed to build the message: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("notify/feishu: failed to build the request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := feishuHTTP.Do(req)
	if err != nil {
		return fmt.Errorf("notify/feishu: send failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New("notify/feishu: HTTP delivery failed")
	}

	// Feishu can still signal a business failure under HTTP 200, with code!=0 in the body, so
	// the body has to be parsed to tell.
	var out struct {
		Code *int `json:"code"`
		// Newer webhooks report failures in the StatusCode/StatusMessage fields.
		StatusCode *int `json:"StatusCode"`
	}
	response, err := io.ReadAll(io.LimitReader(resp.Body, 65537))
	if err != nil {
		return err
	}
	if len(response) > 65536 || sonic.Unmarshal(response, &out) != nil || out.Code == nil && out.StatusCode == nil {
		return errors.New("notify/feishu: delivery acknowledgement invalid")
	}
	if out.Code != nil && *out.Code != 0 || out.StatusCode != nil && *out.StatusCode != 0 {
		return errors.New("notify/feishu: delivery rejected")
	}
	return nil
}

// buildBody assembles the Feishu custom bot's post rich text message, falling back to plain
// title text when no title is given. With signing enabled it attaches timestamp and sign.
func (s *feishuSender) buildBody(msg Message) ([]byte, error) {
	title := strings.TrimSpace(msg.Title)
	if title == "" {
		title = "流水线触达"
	}

	payload := map[string]any{
		"msg_type": "post",
		"content": map[string]any{
			"post": map[string]any{
				"zh_cn": map[string]any{
					"title":   title,
					"content": feishuBodyLines(msg.Body),
				},
			},
		},
	}

	if strings.TrimSpace(s.cfg.Secret) != "" {
		ts := time.Now().Unix()
		sign, err := genSign(s.cfg.Secret, ts)
		if err != nil {
			return nil, err
		}
		payload["timestamp"] = fmt.Sprintf("%d", ts)
		payload["sign"] = sign
	}

	return sonic.Marshal(payload)
}

// feishuBodyLines splits the body into the several segments a Feishu post rich text message
// takes. A \n inside a single text segment does not break the line there; each line has to be
// its own sub-array (paragraph) of content. So <br> and \n are both treated as newlines and
// split line by line, which makes the template's line breaks actually show up.
func feishuBodyLines(body string) [][]map[string]any {
	normalized := feishuBrRe.ReplaceAllString(body, "\n")
	normalized = strings.ReplaceAll(normalized, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	lines := strings.Split(normalized, "\n")
	content := make([][]map[string]any, 0, len(lines))
	for _, line := range lines {
		content = append(content, []map[string]any{{"tag": "text", "text": line}})
	}
	return content
}

// genSign computes the signature per Feishu's custom bot signing scheme: HMAC-SHA256 the
// empty string using "{timestamp}\n{secret}" as the key, then base64 the result.
func genSign(secret string, ts int64) (string, error) {
	stringToSign := fmt.Sprintf("%d\n%s", ts, secret)
	h := hmac.New(sha256.New, []byte(stringToSign))
	if _, err := h.Write([]byte("")); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(h.Sum(nil)), nil
}
