package nodes

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/quant4dad/internal/notify"
	"github.com/quant4dad/internal/pipeline"
)

// fakeSender records the last message sent and can be told to return an error, for testing
// the delivery node.
type fakeSender struct {
	channel string
	err     error
	last    notify.Message
}

func (s *fakeSender) Channel() string { return s.channel }
func (s *fakeSender) Send(_ context.Context, msg notify.Message) error {
	s.last = msg
	return s.err
}

// fakeNotifier always resolves to the preset sender, or to a resolution error.
type fakeNotifier struct {
	sender *fakeSender
	err    error
}

func (n fakeNotifier) Resolve(context.Context, string) (notify.Sender, error) {
	if n.err != nil {
		return nil, n.err
	}
	return n.sender, nil
}

func TestDelivery_RendersAndSends(t *testing.T) {
	sender := &fakeSender{channel: "email"}
	d := NewDelivery(fakeNotifier{sender: sender})

	cfg := `{"channel":"email","to":"a@x.com, b@x.com","title":"播报:{{.payload.title}}","body":"评级 {{.payload.ai_result.rating}}"}`
	if err := d.Validate(json.RawMessage(cfg)); err != nil {
		t.Fatalf("validate: %v", err)
	}

	rc := &pipeline.RunContext{Msg: &pipeline.Message{Payload: map[string]any{
		"title":     "茅台大涨",
		"ai_result": map[string]any{"rating": "看多"},
	}}}
	act, err := d.Process(context.Background(), rc, json.RawMessage(cfg))
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if act != pipeline.ActionPass {
		t.Fatalf("want pass, got %s", act)
	}
	if sender.last.Title != "播报:茅台大涨" {
		t.Fatalf("title = %q", sender.last.Title)
	}
	if sender.last.Body != "评级 看多" {
		t.Fatalf("body = %q", sender.last.Body)
	}
	if len(sender.last.To) != 2 || sender.last.To[0] != "a@x.com" || sender.last.To[1] != "b@x.com" {
		t.Fatalf("recipients = %#v", sender.last.To)
	}
}

func TestDelivery_OnErrorFailVsContinue(t *testing.T) {
	failSend := func(onErr string) (pipeline.Action, error) {
		sender := &fakeSender{channel: "feishu", err: errors.New("boom")}
		d := NewDelivery(fakeNotifier{sender: sender})
		cfg := `{"channel":"feishu","body":"x","on_error":"` + onErr + `"}`
		if err := d.Validate(json.RawMessage(cfg)); err != nil {
			t.Fatalf("validate: %v", err)
		}
		rc := &pipeline.RunContext{Msg: &pipeline.Message{Payload: map[string]any{}}}
		return d.Process(context.Background(), rc, json.RawMessage(cfg))
	}

	// fail: a send failure must return an error to the executor and abort.
	if _, err := failSend("fail"); err == nil {
		t.Fatal("on_error=fail should return error on send failure")
	}
	// continue: a send failure must pass, with the error recorded in the lineage rather than
	// interrupting.
	if act, err := failSend("continue"); err != nil || act != pipeline.ActionPass {
		t.Fatalf("on_error=continue want pass/nil, got %s / %v", act, err)
	}
}

func TestDelivery_DryRunPreviewsWithoutResolvingOrSending(t *testing.T) {
	// A nil notifier would panic on any credential resolution or send attempt.
	reg := pipeline.NewRegistry()
	reg.Register(NewDelivery(nil))
	for _, channel := range []string{"email", "feishu"} {
		cfg := json.RawMessage(`{"channel":"` + channel + `","title":"预览","body":"{{.payload.text}}"}`)
		result := pipeline.NewExecutor(reg).Run(context.Background(), []pipeline.Node{{Key: "delivery", Type: "delivery", Config: cfg}}, nil,
			&pipeline.Message{Payload: map[string]any{"text": "本地 🐉"}}, true)
		if result.Err != nil || len(result.Traces) != 1 {
			t.Fatalf("preview: %+v", result)
		}
		preview := result.Traces[0].DeliveryPreview
		if preview == nil || preview.Body != "本地 🐉" || preview.Channel != channel || preview.UsesDefaultRecipients != (channel == "email") {
			t.Fatalf("invalid preview: %+v", preview)
		}
	}
	d := NewDelivery(nil)
	rc := &pipeline.RunContext{Msg: &pipeline.Message{Payload: map[string]any{"text": strings.Repeat("x", 16385)}}, DryRun: true}
	if _, err := d.Process(context.Background(), rc, json.RawMessage(`{"channel":"email","body":"{{.payload.text}}","on_error":"continue"}`)); err == nil {
		t.Fatal("oversized preview bypassed the output bound")
	}
}

func TestDelivery_ValidationRejectsBadChannelAndEmptyBody(t *testing.T) {
	d := NewDelivery(fakeNotifier{})
	if err := d.Validate(json.RawMessage(`{"channel":"sms","body":"x"}`)); err == nil {
		t.Fatal("unsupported channel should fail validation")
	}
	if err := d.Validate(json.RawMessage(`{"channel":"email","body":""}`)); err == nil {
		t.Fatal("empty body should fail validation")
	}
}
