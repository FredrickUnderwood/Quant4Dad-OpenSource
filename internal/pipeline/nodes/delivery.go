package nodes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"text/template"

	"github.com/bytedance/sonic"

	"github.com/quant4dad/internal/notify"
	"github.com/quant4dad/internal/pipeline"
)

// Notifier resolves a delivery sender by channel name; notify.DynamicFactory implements
// it, reading the channel config from the setting table at runtime.
type Notifier interface {
	Resolve(ctx context.Context, channel string) (notify.Sender, error)
}

// deliveryConfig is the delivery node's config. Title and Body are Go text/templates that
// can reach {{.payload.xxx}} — including fields written back by an upstream AI node, such as
// {{.payload.ai_result.xxx}} — and {{.meta.xxx}}, which is how the message and the
// AI-generated fields reach the outside channel.
type deliveryConfig struct {
	Channel string `json:"channel"`  // email / feishu
	To      string `json:"to"`       // email recipients, comma/semicolon/newline separated; empty uses the channel default. Ignored for Feishu
	Title   string `json:"title"`    // title template (the email subject or Feishu message title)
	Body    string `json:"body"`     // body template
	OnError string `json:"on_error"` // fail (the default, aborts) / continue (records but lets it through)
}

// Delivery renders the title and body templates and sends the message out through the
// configured channel (email or Feishu). As the pipeline's exit node it passes the message
// through (ActionPass) after sending, never changing retention.
type Delivery struct {
	notifier Notifier
}

func NewDelivery(notifier Notifier) *Delivery { return &Delivery{notifier: notifier} }

func (*Delivery) Type() string        { return "delivery" }
func (*Delivery) DisplayName() string { return "触达" }
func (*Delivery) Category() string    { return "output" }

func (*Delivery) ConfigSchema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "required": ["channel", "body"],
  "properties": {
    "channel": {"type": "string", "x_enum_source": "notify_channels", "title": "触达渠道", "description": "从设置中已配置的触达通道里选择"},
    "to": {"type": "string", "title": "收件人(邮件)", "description": "多个用逗号/换行分隔；留空用设置里的默认收件人", "x_hide_when": {"channel": ["feishu"]}},
    "title": {"type": "string", "title": "标题模板", "description": "支持 {{.payload.字段}}，如邮件主题/飞书标题"},
    "body": {"type": "string", "title": "正文模板", "description": "支持 {{.payload.字段}}，可引用 AI 写回字段如 {{.payload.ai_result.xxx}}"},
    "on_error": {"type": "string", "enum": ["fail", "continue"], "default": "fail", "title": "出错策略"}
  }
}`)
}

func (*Delivery) Validate(raw json.RawMessage) error {
	cfg, err := parseDeliveryConfig(raw)
	if err != nil {
		return err
	}
	if cfg.Channel != notify.ChannelEmail && cfg.Channel != notify.ChannelFeishu {
		return fmt.Errorf("delivery: unsupported channel %q (expect email or feishu)", cfg.Channel)
	}
	if strings.TrimSpace(cfg.Body) == "" {
		return errors.New("delivery: body must not be empty")
	}
	if _, err := template.New("t").Parse(cfg.Title); err != nil {
		return fmt.Errorf("delivery: invalid title template: %w", err)
	}
	if _, err := template.New("b").Parse(cfg.Body); err != nil {
		return fmt.Errorf("delivery: invalid body template: %w", err)
	}
	if cfg.OnError != "" && cfg.OnError != "fail" && cfg.OnError != "continue" {
		return fmt.Errorf("delivery: invalid on_error %q (expect fail or continue)", cfg.OnError)
	}
	return nil
}

func (d *Delivery) Process(ctx context.Context, rc *pipeline.RunContext, raw json.RawMessage) (pipeline.Action, error) {
	cfg, err := parseDeliveryConfig(raw)
	if err != nil {
		return pipeline.ActionPass, err
	}

	if rc.DryRun {
		return d.preview(rc, cfg)
	}
	title, err := renderPrompt(cfg.Title, rc.Msg)
	if err != nil {
		return d.onError(rc, cfg, fmt.Errorf("delivery: failed to render title: %w", err))
	}
	body, err := renderPrompt(cfg.Body, rc.Msg)
	if err != nil {
		return d.onError(rc, cfg, fmt.Errorf("delivery: failed to render body: %w", err))
	}

	sender, err := d.notifier.Resolve(ctx, cfg.Channel)
	if err != nil {
		return d.onError(rc, cfg, err)
	}

	msg := notify.Message{Title: title, Body: body, To: splitRecipients(cfg.To)}
	if err := sender.Send(ctx, msg); err != nil {
		return d.onError(rc, cfg, err)
	}
	return pipeline.ActionPass, nil
}

func (d *Delivery) preview(rc *pipeline.RunContext, cfg deliveryConfig) (pipeline.Action, error) {
	if cfg.Channel != notify.ChannelEmail && cfg.Channel != notify.ChannelFeishu {
		return pipeline.ActionPass, errors.New("delivery_preview_channel_invalid")
	}
	title, err := renderPreview(cfg.Title, rc.Msg, 1024)
	if err != nil {
		return pipeline.ActionPass, err
	}
	body, err := renderPreview(cfg.Body, rc.Msg, 16384)
	if err != nil {
		return pipeline.ActionPass, err
	}
	recipients := []string{}
	if cfg.Channel == notify.ChannelEmail {
		recipients = append(recipients, splitRecipients(cfg.To)...)
	}
	if len(recipients) > 100 || len(cfg.To) > 4096 {
		return pipeline.ActionPass, errors.New("delivery_preview_recipients_limit")
	}
	rc.PreviewDelivery(pipeline.DeliveryPreview{Channel: cfg.Channel, Recipients: recipients,
		UsesDefaultRecipients: cfg.Channel == notify.ChannelEmail && len(recipients) == 0, Title: title, Body: body})
	return pipeline.ActionPass, nil
}

type previewWriter struct {
	text      strings.Builder
	remaining int
}

func (w *previewWriter) Write(p []byte) (int, error) {
	if len(p) > w.remaining {
		return 0, errors.New("delivery_preview_output_limit")
	}
	w.remaining -= len(p)
	return w.text.Write(p)
}
func renderPreview(value string, msg *pipeline.Message, limit int) (string, error) {
	t, err := template.New("preview").Option("missingkey=zero").Parse(value)
	if err != nil {
		return "", errors.New("delivery_preview_template_invalid")
	}
	w := &previewWriter{remaining: limit}
	if err = t.Execute(w, map[string]any{"payload": msg.Payload, "meta": msg.Meta}); err != nil {
		return "", errors.New("delivery_preview_render_failed")
	}
	return w.text.String(), nil
}

// onError decides, from the config, whether a failure aborts or is let through. With
// continue, the failure reason goes into the node's lineage via rc.Note ->
// NodeTrace.Error rather than being silently swallowed; fail instead returns the error to
// the executor and aborts.
func (*Delivery) onError(rc *pipeline.RunContext, cfg deliveryConfig, err error) (pipeline.Action, error) {
	if cfg.OnError == "continue" {
		rc.Note(err.Error())
		return pipeline.ActionPass, nil
	}
	return pipeline.ActionPass, err
}

// splitRecipients splits the recipient string into addresses on commas, semicolons,
// newlines and whitespace.
func splitRecipients(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r' || r == ' ' || r == '\t'
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

func parseDeliveryConfig(raw json.RawMessage) (deliveryConfig, error) {
	var cfg deliveryConfig
	if len(raw) == 0 {
		return cfg, errors.New("delivery: config is empty")
	}
	if err := sonic.Unmarshal(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("delivery: failed to parse config: %w", err)
	}
	if cfg.Channel == "" {
		cfg.Channel = notify.ChannelEmail
	}
	return cfg, nil
}
