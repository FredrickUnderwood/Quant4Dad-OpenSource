package agentbridge

import (
	"bufio"
	"context"
	"errors"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

type StreamResult struct {
	LastEventID  string
	TerminalType string // Empty unless a terminal event was accepted by the caller.
}

// StreamCallbacks are synchronous and provide backpressure. Ready runs only
// after a successful upstream status/content type. Keepalive carries no data/ID.
type StreamCallbacks struct {
	Ready     func() error
	Keepalive func() error
	Event     func(Event) error
}

func ValidateEventCursor(cursor string) error {
	if cursor != "" && (!decimal(cursor) || len(cursor) > 1024) {
		return ErrInvalidRequest
	}
	return nil
}

// ConsumeEvents consumes one connection. accept runs synchronously, providing
// backpressure; its successful return acknowledges the event and advances the
// returned cursor. A callback error retains the preceding cursor. There is no
// automatic reconnect, approval, cancellation or resubmission.
//
// EOF without an accepted terminal is ErrStreamInterrupted, including an empty
// replay after an already-consumed terminal. Reconcile GetRun before deciding
// whether to reconnect. A 410 requires a transcript reload, not cursor reset.
func (c *Client) ConsumeEvents(ctx context.Context, sessionID, runID, lastEventID string, accept func(Event) error) (StreamResult, error) {
	return c.StreamEvents(ctx, sessionID, runID, lastEventID, StreamCallbacks{Event: accept})
}

func (c *Client) StreamEvents(ctx context.Context, sessionID, runID, lastEventID string, callbacks StreamCallbacks) (StreamResult, error) {
	if lastEventID == "" {
		lastEventID = "0"
	}
	result := StreamResult{LastEventID: lastEventID}
	if !idPattern.MatchString(sessionID) || !idPattern.MatchString(runID) || ValidateEventCursor(lastEventID) != nil || callbacks.Event == nil {
		return result, ErrInvalidRequest
	}
	response, err := c.send(ctx, http.MethodGet, "/runs/"+runID+"/events", nil, "text/event-stream", lastEventID)
	if err != nil {
		return result, err
	}
	defer response.Body.Close()
	reader := &idleReader{body: response.Body, timeout: c.streamIdleTimeout}
	response.Body = reader
	if response.StatusCode != http.StatusOK {
		return result, responseError(ctx, response, false)
	}
	if !contentType(response, "text/event-stream") {
		return result, ErrProtocol
	}
	if callbacks.Ready != nil {
		if err := callbacks.Ready(); err != nil {
			return result, err
		}
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), maxJSONBytes+1)
	var id, eventType string
	comment := false
	var data strings.Builder
	frameBytes := 0
	for scanner.Scan() {
		line := scanner.Text()
		frameBytes += len(line) + 1
		if frameBytes > maxJSONBytes {
			return result, ErrProtocol
		}
		if line != "" {
			if strings.HasPrefix(line, ":") {
				comment = true
				continue
			}
			field, value, _ := strings.Cut(line, ":")
			value = strings.TrimPrefix(value, " ")
			switch field {
			case "id":
				if id != "" || !decimal(value) || value == "0" || len(value) > 1024 {
					return result, ErrProtocol
				}
				id = value
			case "event":
				if eventType != "" || value == "" {
					return result, ErrProtocol
				}
				eventType = value
			case "data":
				data.WriteString(value)
				data.WriteByte('\n')
			}
			continue
		}
		if data.Len() > 0 {
			event, err := parseEvent([]byte(data.String()))
			if err != nil {
				return result, err
			}
			next, _ := new(big.Int).SetString(result.LastEventID, 10)
			next.Add(next, big.NewInt(1))
			if event.ID != id || event.Type != eventType || event.RunID != runID || event.SessionID != sessionID || event.ID != next.String() {
				return result, ErrProtocol
			}
			if err := ctx.Err(); err != nil {
				return result, failure(err, 0, false)
			}
			if err := callbacks.Event(event); err != nil {
				return result, err
			}
			result.LastEventID = event.ID
			if event.Terminal() {
				result.TerminalType = event.Type
				return result, nil
			}
		} else if id != "" || eventType != "" {
			return result, ErrProtocol
		} else if comment && callbacks.Keepalive != nil {
			if err := callbacks.Keepalive(); err != nil {
				return result, err
			}
		}
		comment = false
		id, eventType = "", ""
		data.Reset()
		frameBytes = 0
	}
	if err := ctx.Err(); err != nil {
		return result, failure(err, 0, false)
	}
	if errors.Is(scanner.Err(), ErrStreamIdle) {
		return result, ErrStreamIdle
	}
	if errors.Is(scanner.Err(), bufio.ErrTooLong) {
		return result, ErrProtocol
	}
	// A partial frame or abrupt socket close is never acknowledged.
	return result, ErrStreamInterrupted
}

// Idle time counts blocked network reads, not time spent persisting an event in
// the callback. Closing the body releases blocked reads without a goroutine leak.
type idleReader struct {
	body    io.ReadCloser
	timeout time.Duration
}

func (r *idleReader) Close() error { return r.body.Close() }

func (r *idleReader) Read(p []byte) (int, error) {
	var expired atomic.Bool
	timer := time.AfterFunc(r.timeout, func() { expired.Store(true); r.body.Close() })
	n, err := r.body.Read(p)
	timer.Stop()
	if expired.Load() {
		return 0, ErrStreamIdle
	}
	return n, err
}
