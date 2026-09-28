package agentbridge

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func frame(id, eventType string, payload any) string {
	data, _ := wireJSON.Marshal(Event{ID: id, Type: eventType, RunID: "run-1", SessionID: "session-1", OccurredAt: "2026-09-09T00:00:00Z", SchemaVersion: 1, Data: payload})
	return "id: " + id + "\nevent: " + eventType + "\ndata: " + string(data) + "\n\n"
}
func messageFrame(id string) string {
	return frame(id, "message.delta", MessageData{MessageID: "answer-1", Text: "你好 😀"})
}

func TestSSEFragmentedUnicodeAndLargeStringCursor(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Last-Event-ID") != "9007199254740992" || r.Header.Get("Accept") != "text/event-stream" {
			t.Error("cursor/accept header lost")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		stream := ": keepalive\n\n" + messageFrame("9007199254740993") + frame("9007199254740994", "run.completed", EmptyData{})
		for _, b := range []byte(strings.ReplaceAll(stream, "\n", "\r\n")) {
			_, _ = w.Write([]byte{b})
			w.(http.Flusher).Flush()
		}
	})
	var events []Event
	result, err := client.ConsumeEvents(context.Background(), "session-1", "run-1", "9007199254740992", func(e Event) error { events = append(events, e); return nil })
	if err != nil || result.LastEventID != "9007199254740994" || result.TerminalType != "run.completed" || len(events) != 2 || events[0].Data.(MessageData).Text != "你好 😀" {
		t.Fatalf("stream result mismatch: %+v %v", result, err)
	}
}

func TestSSEReadyAndKeepalivePreserveProtocolAndCursor(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, ": do-not-forward-private-comment\n\n"+messageFrame("1")+frame("2", "run.completed", EmptyData{}))
	})
	var order []string
	result, err := client.StreamEvents(context.Background(), "session-1", "run-1", "", StreamCallbacks{Ready: func() error { order = append(order, "ready"); return nil }, Keepalive: func() error { order = append(order, "heartbeat"); return nil }, Event: func(e Event) error { order = append(order, e.ID); return nil }})
	if err != nil || result.LastEventID != "2" || strings.Join(order, ",") != "ready,heartbeat,1,2" {
		t.Fatal(result, order, err)
	}
	for _, code := range []int{400, 410, 503} {
		bad := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			_, _ = io.WriteString(w, `{"error":{"code":"agent_event_cursor_expired"}}`)
		})
		_, err = bad.StreamEvents(context.Background(), "session-1", "run-1", "0", StreamCallbacks{Ready: func() error { t.Fatal("failed upstream opened browser stream"); return nil }, Event: func(Event) error { t.Fatal("error body reached consumer"); return nil }})
		if err == nil {
			t.Fatal("upstream error swallowed")
		}
	}
}

func TestSSECallbackFailureRetainsReplayCursor(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, messageFrame("1")+frame("2", "run.completed", EmptyData{}))
	})
	callbackErr := errors.New("persist failed")
	result, err := client.ConsumeEvents(context.Background(), "session-1", "run-1", "0", func(e Event) error {
		if e.Terminal() {
			return callbackErr
		}
		return nil
	})
	if err != callbackErr || result.LastEventID != "1" || result.TerminalType != "" {
		t.Fatal("unaccepted terminal advanced cursor")
	}
}

func TestSSERejectsIdentitySequenceAndPayloadMismatch(t *testing.T) {
	valid := messageFrame("1")
	for name, stream := range map[string]string{
		"gap": messageFrame("2"), "duplicate": valid + valid,
		"wrong session":   strings.Replace(valid, `"session_id":"session-1"`, `"session_id":"other"`, 1),
		"wrong run":       strings.Replace(valid, `"run_id":"run-1"`, `"run_id":"other"`, 1),
		"wrong id":        strings.Replace(valid, "id: 1", "id: 2", 1),
		"wrong type":      strings.Replace(valid, "event: message.delta", "event: message.completed", 1),
		"missing id":      strings.TrimPrefix(valid, "id: 1\n"),
		"duplicate field": "id: 1\n" + valid,
		"unknown data":    strings.Replace(valid, `"text":"你好 😀"`, `"text":"你好 😀","secret":"sensitive"`, 1),
		"oversized frame": "data: " + strings.Repeat("a", maxJSONBytes) + "\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			client := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, stream)
			})
			calls := 0
			result, err := client.ConsumeEvents(context.Background(), "session-1", "run-1", "0", func(Event) error { calls++; return nil })
			if !errors.Is(err, ErrProtocol) || result.TerminalType != "" {
				t.Fatalf("invalid frame accepted: %v", err)
			}
			if name == "duplicate" {
				if calls != 1 || result.LastEventID != "1" {
					t.Fatal("duplicate advanced cursor")
				}
			} else if calls != 0 || result.LastEventID != "0" {
				t.Fatal("invalid frame reached callback")
			}
		})
	}
}

func TestSSEEOFDoesNotInventTerminal(t *testing.T) {
	for name, stream := range map[string]string{"empty": "", "valid prefix": messageFrame("1"), "partial frame": strings.TrimSuffix(messageFrame("1"), "\n\n")} {
		t.Run(name, func(t *testing.T) {
			client := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, stream)
			})
			result, err := client.ConsumeEvents(context.Background(), "session-1", "run-1", "0", func(Event) error { return nil })
			if err != ErrStreamInterrupted || result.TerminalType != "" {
				t.Fatal("EOF accepted as completion")
			}
			if name == "valid prefix" {
				if result.LastEventID != "1" {
					t.Fatal("accepted prefix lost")
				}
			} else if result.LastEventID != "0" {
				t.Fatal("partial frame acknowledged")
			}
		})
	}
}

func TestSSECursorExpiryPreservesCursor(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, 410, map[string]any{"error": map[string]string{"code": "agent_event_cursor_expired"}})
	})
	result, err := client.ConsumeEvents(context.Background(), "session-1", "run-1", "12", func(Event) error { t.Fatal("expired cursor delivered event"); return nil })
	assertBridgeError(t, err, "agent_event_cursor_expired", 410, false)
	if result.LastEventID != "12" {
		t.Fatal("expired cursor reset")
	}
}

func TestSSEIdleAndContextCancellation(t *testing.T) {
	for _, idle := range []bool{true, false} {
		t.Run(map[bool]string{true: "idle", false: "context"}[idle], func(t *testing.T) {
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(200)
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}, func(c *Config) {
				if idle {
					c.StreamIdleTimeout = 40 * time.Millisecond
				}
			})
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if !idle {
				ctx, cancel = context.WithTimeout(ctx, 40*time.Millisecond)
				defer cancel()
			}
			result, err := client.ConsumeEvents(ctx, "session-1", "run-1", "0", func(Event) error { return nil })
			if idle && err != ErrStreamIdle || !idle && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("wrong interruption: %v", err)
			}
			if result.LastEventID != "0" {
				t.Fatal("timeout advanced cursor")
			}
		})
	}
}

func TestSSEErrorBodyCannotHangIndefinitely(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(503)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}, func(c *Config) { c.StreamIdleTimeout = 40 * time.Millisecond })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := client.ConsumeEvents(ctx, "session-1", "run-1", "0", func(Event) error { return nil })
	if !errors.Is(err, ErrTransport) || result.LastEventID != "0" || ctx.Err() != nil {
		t.Fatal("error body escaped the read deadline")
	}
}

func TestSSEKeepaliveAndSlowConsumerDoNotUseRequestTimeout(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for range 4 {
			_, _ = io.WriteString(w, ": keepalive\n\n")
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(30 * time.Millisecond):
			}
		}
		_, _ = io.WriteString(w, messageFrame("1")+frame("2", "run.completed", EmptyData{}))
	}, func(c *Config) {
		c.RequestTimeout = 20 * time.Millisecond
		c.StreamIdleTimeout = 200 * time.Millisecond
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result, err := client.ConsumeEvents(ctx, "session-1", "run-1", "0", func(e Event) error {
		if !e.Terminal() {
			time.Sleep(250 * time.Millisecond)
		}
		return nil
	})
	if err != nil || result.TerminalType != "run.completed" {
		t.Fatalf("healthy slow stream timed out: %v", err)
	}
}
