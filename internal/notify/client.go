// Package notify provides a uniform abstraction for delivering messages to the outside world
// across channels. The event pipeline's delivery nodes resolve a Sender by channel name
// through a Factory and then call Send, which hides the differences between channels. Two
// channels are built in: email over SMTP, and Feishu via a custom bot webhook.
package notify

import (
	"context"
	"fmt"
)

// Channel name constants. Register a new channel here and add a branch in buildSender.
const (
	ChannelEmail  = "email"
	ChannelFeishu = "feishu"
)

// Message is one message to deliver. Title is the heading (an email subject or a Feishu
// message title) and Body is the content. Each channel presents them as it can: email sends
// an HTML body, where both newlines and <br> break lines, while Feishu sends a rich text
// (post) message with the title and body in separate parts.
type Message struct {
	Title string
	Body  string
	// To overrides this send's targets, i.e. the email recipient list. When empty each channel
	// falls back to the default target from its own config, such as the email config's
	// default_to. A Feishu webhook's target is fixed, so it ignores this.
	To []string
}

// Sender is one delivery channel's sender.
type Sender interface {
	Channel() string
	Send(ctx context.Context, msg Message) error
}

// EmailConfig describes the email (SMTP) channel's connection parameters, sourced from the
// setting table.
type EmailConfig struct {
	Host      string   // SMTP server address, e.g. smtp.gmail.com
	Port      int      // SMTP port: 587 (STARTTLS), 465 (SSL) or 25
	Username  string   // login username, usually the full email address
	Password  string   // login password or app password
	From      string   // sender address; falls back to Username when empty
	UseSSL    bool     // true = implicit SSL on 465; false = STARTTLS or plaintext
	DefaultTo []string // recipients used when a node names none
}

// FeishuConfig describes the Feishu custom bot webhook channel's connection parameters,
// sourced from the setting table.
type FeishuConfig struct {
	WebhookURL string // the custom bot's webhook URL
	Secret     string // optional: the key used when signature verification is enabled
}

// ChannelConfigs is every channel config available at one resolve. A nil pointer means that
// channel is unconfigured, and resolving it errors with a pointer to the settings page.
type ChannelConfigs struct {
	Email  *EmailConfig
	Feishu *FeishuConfig
}

// ChannelSource returns the currently available channel configs when called. Driven by the
// setting table, every resolve reads the latest config, so UI changes take effect at once.
type ChannelSource func(ctx context.Context) (ChannelConfigs, error)

// DynamicFactory reads the latest channel config through ChannelSource on every resolve and
// builds the sender on demand, which is what lets delivery settings be maintained from the UI
// at runtime, with no restart.
type DynamicFactory struct {
	source ChannelSource
}

func NewDynamicFactory(source ChannelSource) *DynamicFactory {
	return &DynamicFactory{source: source}
}

// Resolve implements the delivery node's resolver interface: look up the config by channel
// name and build the sender.
func (f *DynamicFactory) Resolve(ctx context.Context, channel string) (Sender, error) {
	cfgs, err := f.source(ctx)
	if err != nil {
		return nil, fmt.Errorf("notify: load channel settings: %w", err)
	}
	return buildSender(channel, cfgs)
}

// buildSender builds one channel's sender from config, returning a guiding error when the
// channel is unconfigured.
func buildSender(channel string, cfgs ChannelConfigs) (Sender, error) {
	switch channel {
	case ChannelEmail:
		if cfgs.Email == nil || cfgs.Email.Host == "" {
			return nil, fmt.Errorf("notify: the email channel is not configured (fill in SMTP under Settings)")
		}
		return newEmailSender(*cfgs.Email), nil
	case ChannelFeishu:
		if cfgs.Feishu == nil || cfgs.Feishu.WebhookURL == "" {
			return nil, fmt.Errorf("notify: the Feishu channel is not configured (fill in the webhook under Settings)")
		}
		return newFeishuSender(*cfgs.Feishu), nil
	default:
		return nil, fmt.Errorf("notify: unsupported delivery channel %q", channel)
	}
}
