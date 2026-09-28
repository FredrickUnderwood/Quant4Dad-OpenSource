package mcp

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/quant4dad/internal/agentrunauth"
	"github.com/quant4dad/internal/application"
	"github.com/quant4dad/internal/service"
	"github.com/quant4dad/internal/utils/strictjson"
)

// InternalHTTPHandler is a separate ingress with mandatory Runtime and Run
// credentials. The external Server never accepts or forwards this authority.
func InternalHTTPHandler(app *application.AgentToolGatewayApplication, token string) http.Handler {
	protocol := NewServer("quant4dad-internal", "1")
	slots := make(chan struct{}, 64)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.URL.Path != "/internal/mcp" {
			http.NotFound(w, r)
			return
		}
		if app == nil || token == "" || !bearerOK(r, token) {
			http.Error(w, "unauthorized", 401)
			return
		}
		receipts := r.Header.Values("X-Q4D-Approval-Receipt")
		validReceipt := true
		if len(receipts) == 1 {
			value, err := base64.RawURLEncoding.Strict().DecodeString(receipts[0])
			validReceipt = err == nil && len(value) == 64 && base64.RawURLEncoding.EncodeToString(value) == receipts[0]
		}
		if len(r.Header.Values("Origin")) != 0 || r.URL.RawQuery != "" || r.URL.ForceQuery || len(r.Header.Values("Content-Encoding")) != 0 || len(receipts) > 1 || !validReceipt {
			http.Error(w, "invalid request", 400)
			return
		}
		capabilities := r.Header.Values("X-Q4D-Run-Capability")
		calls, keys := r.Header.Values("X-Q4D-Tool-Call-ID"), r.Header.Values("Idempotency-Key")
		if len(capabilities) != 1 || len(capabilities[0]) > agentrunauth.MaxTokenBytes || len(calls) != 1 || len(keys) != 1 || len(calls[0]) != 26 || len(keys[0]) > 64 {
			http.Error(w, "invalid authority", 403)
			return
		}
		if r.Method != "POST" && r.Method != "DELETE" {
			w.Header().Set("Allow", "POST, DELETE")
			http.Error(w, "method not allowed", 405)
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			w.Header().Set("Retry-After", "1")
			http.Error(w, "gateway busy", 503)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		claims, err := app.Authorize(ctx, capabilities[0])
		if err != nil || !service.AgentToolIdentity(claims.Envelope.RunID, calls[0], keys[0]) {
			http.Error(w, "agent_capability_rejected", 403)
			return
		}
		if r.Method == "DELETE" {
			w.WriteHeader(200)
			return
		}
		media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || media != "application/json" {
			http.Error(w, "application/json required", 415)
			return
		}
		// Gin's Flush has no error result. Preserve actual network flush errors
		// for the start handshake, while writes still pass through its accounting.
		var raw http.ResponseWriter = w
		for {
			u, ok := raw.(interface{ Unwrap() http.ResponseWriter })
			if !ok {
				break
			}
			raw = u.Unwrap()
		}
		controller := http.NewResponseController(raw)
		_ = controller.SetReadDeadline(time.Now().Add(5 * time.Second))
		defer controller.SetReadDeadline(time.Time{})
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBytes))
		_ = controller.SetReadDeadline(time.Time{})
		if err != nil {
			var large *http.MaxBytesError
			status := 400
			if errors.As(err, &large) {
				status = 413
			}
			http.Error(w, "invalid request", status)
			return
		}
		req, err := parseRequest(body)
		if err != nil {
			http.Error(w, "invalid request", 400)
			return
		}
		if len(req.ID) == 0 {
			w.WriteHeader(202)
			return
		}
		streaming := false
		write := func(frame []byte) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			_ = controller.SetWriteDeadline(time.Now().Add(10 * time.Second))
			_, err := w.Write(frame)
			if err != nil {
				return err
			}
			return controller.Flush()
		}
		defer controller.SetWriteDeadline(time.Time{})
		var response *rpcResponse
		reply := func(value any) { response = &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: value} }
		switch req.Method {
		case "tools/list":
			reply(map[string]any{"tools": toolWire(app.Definitions(claims), claims.Envelope.ToolCatalogRevision)})
		case "tools/call":
			var input struct {
				Name      string  `json:"name"`
				Arguments rawJSON `json:"arguments"`
				Meta      struct {
					Progress rawJSON `json:"progressToken"`
				} `json:"_meta"`
			}
			if strictjson.DecodeNullable(req.Params, &input, maxRequestBytes) != nil || !validProgressToken(input.Meta.Progress) || !strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
				http.Error(w, "invalid params", 400)
				return
			}
			started := func() error {
				streaming = true
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("X-Accel-Buffering", "no")
				message := map[string]any{"jsonrpc": "2.0", "method": "notifications/progress", "params": map[string]any{"progressToken": input.Meta.Progress, "progress": 0, "total": 1, "message": "q4d.tool.started.v1"}}
				return write(sseJSON(message))
			}
			data, code, err := app.Call(ctx, capabilities[0], calls[0], keys[0], input.Name, input.Arguments, started, receipts...)
			if err != nil {
				code = internalToolError(err)
			}
			if code == "agent_approval_required" && len(data) > 0 {
				reply(map[string]any{"isError": true, "content": []map[string]any{{"type": "text", "text": code}}, "structuredContent": rawJSON(data)})
			} else if code != "" {
				reply(map[string]any{"isError": true, "content": []map[string]any{{"type": "text", "text": code}}})
			} else {
				reply(map[string]any{"isError": false, "content": []map[string]any{{"type": "text", "text": string(data)}}, "structuredContent": rawJSON(data)})
			}
		default:
			response = protocol.handle(ctx, req)
		}
		if streaming {
			_ = write(append(append([]byte("event: message\ndata: "), encodeResponse(response)...), []byte("\n\n")...))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = controller.SetWriteDeadline(time.Now().Add(10 * time.Second))
		_, _ = w.Write(encodeResponse(response))
	})
}
func validProgressToken(value rawJSON) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	if value[0] == '"' {
		return true
	}
	n, err := strconv.ParseInt(string(value), 10, 64)
	return err == nil && n >= 0 && n <= 9007199254740991
}
func sseJSON(value any) []byte {
	data, _ := sonic.Marshal(value)
	return append(append([]byte("event: message\ndata: "), data...), []byte("\n\n")...)
}
func internalToolError(err error) string {
	for _, known := range []error{application.ErrToolForbidden, service.ErrToolInput, service.ErrAgentRunRejected, service.ErrAgentToolConflict, service.ErrAgentToolPending, service.ErrAgentToolBudget, service.ErrAgentToolStore, service.ErrAgentApproval} {
		if errors.Is(err, known) {
			return known.Error()
		}
	}
	return "tool_unavailable"
}
