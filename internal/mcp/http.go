package mcp

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
)

const mcpPath = "/mcp"

// HTTPHandler is the external, read-only entry point. Empty credentials deny
// all MCP requests. The future internal Gateway has a separate authority path.
func (s *Server) HTTPHandler(authToken string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			w.WriteHeader(405)
			return
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc(mcpPath, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if authToken == "" || !bearerOK(r, authToken) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", 401)
			return
		}
		// External MCP is not a browser endpoint. No Origin or internal authority is
		// accepted here, even if a caller also possesses the external token.
		if len(r.Header.Values("Origin")) > 0 || r.URL.RawQuery != "" || r.URL.ForceQuery || r.Header.Get("Content-Encoding") != "" {
			http.Error(w, "invalid request", 400)
			return
		}
		for _, name := range []string{"X-Q4D-Run-Capability", "X-Q4D-Tool-Call-ID", "X-Q4D-Approval-Receipt", "Idempotency-Key"} {
			if len(r.Header.Values(name)) > 0 {
				http.Error(w, "invalid request", 400)
				return
			}
		}
		s.serveStreamableHTTP(w, r)
	})
	return mux
}
func (s *Server) serveStreamableHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		select {
		case s.slots <- struct{}{}:
			defer func() { <-s.slots }()
		default:
			w.Header().Set("Retry-After", "1")
			http.Error(w, "mcp busy", http.StatusServiceUnavailable)
			return
		}
		media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || media != "application/json" {
			http.Error(w, "application/json required", 415)
			return
		}
		controller := http.NewResponseController(w)
		_ = controller.SetReadDeadline(time.Now().Add(5 * time.Second))
		defer controller.SetReadDeadline(time.Time{})
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBytes))
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				http.Error(w, "request too large", 413)
			} else {
				http.Error(w, "invalid request", 400)
			}
			return
		}
		_ = controller.SetReadDeadline(time.Time{})
		req, err := parseRequest(body)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(400)
			_, _ = w.Write(encodeResponse(&rpcResponse{JSONRPC: "2.0", Error: &rpcError{codeInvalidRequest, "invalid request"}}))
			return
		}
		resp := s.handle(r.Context(), req)
		if resp == nil {
			w.WriteHeader(202)
			return
		}
		_ = controller.SetWriteDeadline(time.Now().Add(10 * time.Second))
		defer controller.SetWriteDeadline(time.Time{})
		if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("X-Accel-Buffering", "no")
			_, _ = w.Write(append(append([]byte("event: message\ndata: "), encodeResponse(resp)...), []byte("\n\n")...))
			_ = controller.Flush()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(encodeResponse(resp))
	case http.MethodDelete:
		w.WriteHeader(200)
	default:
		w.Header().Set("Allow", "POST, DELETE")
		http.Error(w, "method not allowed", 405)
	}
}
func bearerOK(r *http.Request, token string) bool {
	values := r.Header.Values("Authorization")
	if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") {
		return false
	}
	got := strings.TrimPrefix(values[0], "Bearer ")
	a, b := sha256.Sum256([]byte(got)), sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}
