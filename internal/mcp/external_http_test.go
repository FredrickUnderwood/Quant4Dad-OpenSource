package mcp

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/quant4dad/internal/application"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/service"
)

type externalHTTPCall struct {
	session string
	id      string
	name    string
	args    string
}

type externalHTTPFixture struct {
	mu       sync.Mutex
	sessions map[string]bool
	created  int
	calls    []externalHTTPCall
	onCall   func(context.Context, func() error) ([]byte, string, error)
}

func newExternalHTTPFixture() *externalHTTPFixture {
	return &externalHTTPFixture{sessions: map[string]bool{}}
}

func (f *externalHTTPFixture) CreateSession(context.Context) (domain.ExternalMCPSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created++
	id := "session-" + strconv.Itoa(f.created)
	f.sessions[id] = true
	return domain.ExternalMCPSession{ID: id, ExpiresAt: time.Now().Add(time.Hour)}, nil
}

func (f *externalHTTPFixture) Session(_ context.Context, id string) (domain.ExternalMCPSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.sessions[id] {
		return domain.ExternalMCPSession{}, service.ErrExternalMCPSession
	}
	return domain.ExternalMCPSession{ID: id}, nil
}

func (f *externalHTTPFixture) CloseSession(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.sessions, id)
	return nil
}

func (*externalHTTPFixture) Definitions() []application.ToolDefinition {
	return []application.ToolDefinition{{Name: "fixture_read", Risk: "R0", InputSchema: map[string]any{"type": "object"}}, {Name: "fixture_write", Risk: "R2", InputSchema: map[string]any{"type": "object"}}}
}

func (*externalHTTPFixture) Revision() string { return "fixture-revision" }

func (f *externalHTTPFixture) Call(ctx context.Context, session string, id []byte, name string, args []byte, started func() error) ([]byte, string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, externalHTTPCall{session, string(id), name, string(args)})
	f.mu.Unlock()
	if f.onCall != nil {
		return f.onCall(ctx, started)
	}
	if err := started(); err != nil {
		return nil, "", err
	}
	return []byte(`{"data":{"ok":true},"untrusted_data":true}`), "", nil
}

func externalHTTPRequest(h http.Handler, session, method, body string, mutate func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/mcp", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+testToken)
	r.Header.Set("Accept", "application/json")
	if method == http.MethodPost {
		r.Header.Set("Content-Type", "application/json")
	}
	if session != "" {
		r.Header.Set("Mcp-Session-Id", session)
	}
	if mutate != nil {
		mutate(r)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func externalHTTPInitialize(t *testing.T, h http.Handler) string {
	t.Helper()
	w := externalHTTPRequest(h, "", http.MethodPost, `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"fixture","version":"1"}}}`, nil)
	if w.Code != http.StatusOK || w.Header().Get("Mcp-Session-Id") == "" || !strings.Contains(w.Body.String(), `"protocolVersion":"2025-11-25"`) {
		t.Fatal("initialize failed", w.Code, w.Body.String())
	}
	return w.Header().Get("Mcp-Session-Id")
}

func TestExternalHTTPSessionLifecycleAndCatalog(t *testing.T) {
	f := newExternalHTTPFixture()
	h := newExternalHTTPHandler(f, testToken)
	session := externalHTTPInitialize(t, h)
	second := externalHTTPInitialize(t, h)
	if session == second {
		t.Fatal("initialize reused session")
	}
	list := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	for _, path := range []string{"/mcp", "/external/mcp"} {
		w := externalHTTPRequest(h, session, http.MethodPost, list, func(r *http.Request) { r.URL.Path = path })
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"fixture_read"`) || !strings.Contains(w.Body.String(), `"fixture_write"`) || !strings.Contains(w.Body.String(), "fixture-revision") {
			t.Fatal("catalog missing authorized tools", w.Code, w.Body.String())
		}
	}
	if w := externalHTTPRequest(h, "", http.MethodPost, list, nil); w.Code != 400 {
		t.Fatal("missing session", w.Code)
	}
	if w := externalHTTPRequest(h, "unknown", http.MethodPost, list, nil); w.Code != 404 {
		t.Fatal("unknown session", w.Code)
	}
	if w := externalHTTPRequest(h, session, http.MethodGet, "", nil); w.Code != 405 || w.Header().Get("Allow") != "POST, DELETE" {
		t.Fatal("GET must advertise no independent SSE stream", w.Code)
	}
	if w := externalHTTPRequest(h, session, http.MethodDelete, "", nil); w.Code != 200 {
		t.Fatal("delete", w.Code)
	}
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		body := ""
		if method == http.MethodPost {
			body = list
		}
		if w := externalHTTPRequest(h, session, method, body, nil); w.Code != 404 {
			t.Fatal("closed session remained usable", method, w.Code)
		}
	}
	if w := externalHTTPRequest(h, second, http.MethodPost, list, nil); w.Code != 200 {
		t.Fatal("closing one session affected another", w.Code)
	}
}

func TestExternalHTTPRejectsUnauthorizedAndAuthorityHeaders(t *testing.T) {
	f := newExternalHTTPFixture()
	h := newExternalHTTPHandler(f, testToken)
	session := externalHTTPInitialize(t, h)
	call := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"fixture_write","arguments":{}}}`
	cases := []struct {
		name   string
		status int
		mutate func(*http.Request)
	}{
		{"missing bearer", 401, func(r *http.Request) { r.Header.Del("Authorization") }},
		{"wrong bearer", 401, func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+internalTestToken) }},
		{"duplicate bearer", 401, func(r *http.Request) { r.Header.Add("Authorization", "Bearer "+testToken) }},
		{"origin", 403, func(r *http.Request) { r.Header.Set("Origin", "https://example.test") }},
		{"empty origin", 403, func(r *http.Request) { r.Header["Origin"] = []string{""} }},
		{"query", 400, func(r *http.Request) { r.URL.RawQuery = "token=forged" }},
		{"empty query", 400, func(r *http.Request) { r.URL.ForceQuery = true }},
		{"encoding", 400, func(r *http.Request) { r.Header.Set("Content-Encoding", "gzip") }},
		{"protocol", 400, func(r *http.Request) { r.Header.Set("Mcp-Protocol-Version", "unknown") }},
		{"duplicate protocol", 400, func(r *http.Request) { r.Header["Mcp-Protocol-Version"] = []string{"2025-11-25", "2025-11-25"} }},
		{"duplicate session", 400, func(r *http.Request) { r.Header.Add("Mcp-Session-Id", session) }},
		{"combined session", 400, func(r *http.Request) { r.Header.Set("Mcp-Session-Id", session+","+session) }},
		{"whitespace session", 400, func(r *http.Request) { r.Header.Set("Mcp-Session-Id", " "+session) }},
		{"large session", 400, func(r *http.Request) { r.Header.Set("Mcp-Session-Id", strings.Repeat("a", 257)) }},
		{"wrong content type", 415, func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }},
		{"duplicate content type", 415, func(r *http.Request) { r.Header.Add("Content-Type", "application/json") }},
		{"unsupported accept", 406, func(r *http.Request) { r.Header.Set("Accept", "text/html") }},
		{"zero accept weight", 406, func(r *http.Request) { r.Header.Set("Accept", "application/json;q=0, text/event-stream;q=0") }},
		{"wrong path", 404, func(r *http.Request) { r.URL.Path = "/internal/mcp" }},
	}
	for _, header := range []string{"X-Q4D-Run-Capability", "X-Q4D-Tool-Call-ID", "X-Q4D-Approval-Receipt", "Idempotency-Key"} {
		cases = append(cases, struct {
			name   string
			status int
			mutate func(*http.Request)
		}{header, 400, func(r *http.Request) { r.Header.Set(header, "forged") }})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if w := externalHTTPRequest(h, session, http.MethodPost, call, tc.mutate); w.Code != tc.status {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
	for _, blocked := range []http.Handler{newExternalHTTPHandler(f, ""), ExternalHTTPHandler(nil, testToken)} {
		if w := externalHTTPRequest(blocked, session, http.MethodPost, call, nil); w.Code != 401 {
			t.Fatal("unconfigured handler did not deny access", w.Code)
		}
	}
	if len(f.calls) != 0 {
		t.Fatal("rejected request executed a tool")
	}
}

func TestExternalHTTPPreservesReplayIdentityAndNotificationsNeverExecute(t *testing.T) {
	f := newExternalHTTPFixture()
	h := newExternalHTTPHandler(f, testToken)
	a, b := externalHTTPInitialize(t, h), externalHTTPInitialize(t, h)
	call := `{"jsonrpc":"2.0","id":"stable-request","method":"tools/call","params":{"name":"fixture_write","arguments":{"value":1}}}`
	for _, session := range []string{a, a, b} {
		if w := externalHTTPRequest(h, session, http.MethodPost, call, nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"isError":false`) {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if len(f.calls) != 3 || f.calls[0] != f.calls[1] || f.calls[2].session == f.calls[0].session || f.calls[0].id != `"stable-request"` {
		t.Fatal("transport changed replay identity or crossed sessions", f.calls)
	}
	for _, id := range []string{`1`, `"1"`} {
		body := `{"jsonrpc":"2.0","id":` + id + `,"method":"tools/call","params":{"name":"fixture_write","arguments":{}}}`
		externalHTTPRequest(h, a, http.MethodPost, body, nil)
	}
	if f.calls[3].id == f.calls[4].id {
		t.Fatal("string and numeric IDs collided")
	}
	for _, body := range []string{
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"fixture_write","arguments":{}}}`,
	} {
		w := externalHTTPRequest(h, a, http.MethodPost, body, nil)
		if w.Code != 202 || w.Body.Len() != 0 {
			t.Fatal("notification response", w.Code, w.Body.String())
		}
	}
	if len(f.calls) != 5 {
		t.Fatal("notification executed a tool")
	}
}

func TestExternalHTTPStrictRPCAndBodyLimits(t *testing.T) {
	f := newExternalHTTPFixture()
	h := newExternalHTTPHandler(f, testToken)
	session := externalHTTPInitialize(t, h)
	for _, body := range []string{
		`[]`, `null`, `{`,
		`{"jsonrpc":"2.0","id":1,"id":2,"method":"tools/call"}`,
		`{"jsonrpc":"2.0","id":null,"method":"tools/call"}`,
		`{"jsonrpc":"2.0","id":true,"method":"tools/call"}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","unknown":true}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call"} {}`,
	} {
		w := externalHTTPRequest(h, session, http.MethodPost, body, nil)
		if w.Code != 400 || !strings.Contains(w.Body.String(), `"code":-32600`) {
			t.Fatal("malformed RPC accepted", body, w.Code, w.Body.String())
		}
	}
	if w := externalHTTPRequest(h, session, http.MethodPost, strings.Repeat("x", maxRequestBytes+1), nil); w.Code != 413 {
		t.Fatal("oversized body accepted", w.Code)
	}
	for _, body := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"fixture_write","arguments":{},"session_id":"forged"}}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"cursor":"unsupported"}}`,
	} {
		w := externalHTTPRequest(h, session, http.MethodPost, body, nil)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"code":-32602`) {
			t.Fatal("invalid parameters accepted", w.Code, w.Body.String())
		}
	}
	if w := externalHTTPRequest(h, session, http.MethodDelete, "{}", nil); w.Code != 400 {
		t.Fatal("DELETE accepted a body", w.Code)
	}
	if w := externalHTTPRequest(h, session, http.MethodPut, "", nil); w.Code != 405 {
		t.Fatal("unsupported method", w.Code)
	}
	if len(f.calls) != 0 {
		t.Fatal("invalid request executed a tool")
	}
}

func TestExternalHTTPJSONAndSSEFramingAndContext(t *testing.T) {
	f := newExternalHTTPFixture()
	h := newExternalHTTPHandler(f, testToken)
	session := externalHTTPInitialize(t, h)
	type contextKey struct{}
	f.onCall = func(ctx context.Context, started func() error) ([]byte, string, error) {
		if ctx.Value(contextKey{}) != "preserved" {
			t.Error("request context was discarded")
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 30*time.Second || time.Until(deadline) < 25*time.Second {
			t.Error("execution deadline missing or outside expected bounds")
		}
		if err := started(); err != nil {
			return nil, "", err
		}
		return []byte("{\n\"ok\":true\n}"), "", nil
	}
	call := `{"jsonrpc":"2.0","id":"sse","method":"tools/call","params":{"name":"fixture_write","arguments":{}}}`
	for _, accept := range []string{"application/json", "application/json, text/event-stream"} {
		w := externalHTTPRequest(h, session, http.MethodPost, call, func(r *http.Request) {
			r.Header.Set("Accept", accept)
			*r = *r.WithContext(context.WithValue(r.Context(), contextKey{}, "preserved"))
		})
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		body := w.Body.Bytes()
		if strings.Contains(accept, "text/event-stream") {
			if w.Header().Get("Content-Type") != "text/event-stream" || !bytes.HasPrefix(body, []byte(": processing\n\n")) {
				t.Fatal("invalid SSE headers or handshake", w.Header(), w.Body.String())
			}
			_, body, _ = bytes.Cut(body, []byte("event: message\ndata: "))
			body = bytes.TrimSuffix(body, []byte("\n\n"))
		} else if w.Header().Get("Content-Type") != "application/json" {
			t.Fatal("invalid JSON content type")
		}
		if !sonic.Valid(body) || bytes.Contains(body, []byte("\n")) || !bytes.Contains(body, []byte(`"id":"sse"`)) {
			t.Fatal("RPC response was not a single JSON line", string(body))
		}
	}
}

func TestExternalHTTPMapsErrorsWithoutLeakingDetails(t *testing.T) {
	f := newExternalHTTPFixture()
	h := newExternalHTTPHandler(f, testToken)
	session := externalHTTPInitialize(t, h)
	call := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"fixture_read","arguments":{}}}`
	for _, tc := range []struct {
		err  error
		code string
	}{
		{service.ErrAgentToolConflict, service.ErrAgentToolConflict.Error()},
		{context.DeadlineExceeded, "tool_timeout"},
		{application.ErrToolResultTooLarge, "tool_result_too_large"},
		{errors.New("private SQL / sensitive fixture detail"), "tool_unavailable"},
	} {
		f.onCall = func(context.Context, func() error) ([]byte, string, error) { return nil, "", tc.err }
		w := externalHTTPRequest(h, session, http.MethodPost, call, nil)
		if w.Code != 200 || !strings.Contains(w.Body.String(), tc.code) || !strings.Contains(w.Body.String(), `"isError":true`) || strings.Contains(w.Body.String(), "sensitive") {
			t.Fatal("unexpected error result", w.Code, w.Body.String())
		}
	}
}

func TestExternalHTTPConcurrencyIsBounded(t *testing.T) {
	f := newExternalHTTPFixture()
	h := newExternalHTTPHandler(f, testToken)
	session := externalHTTPInitialize(t, h)
	entered := make(chan struct{}, 64)
	release := make(chan struct{})
	f.onCall = func(ctx context.Context, _ func() error) ([]byte, string, error) {
		entered <- struct{}{}
		select {
		case <-release:
			return []byte(`{"ok":true}`), "", nil
		case <-ctx.Done():
			return nil, "", ctx.Err()
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := `{"jsonrpc":"2.0","id":` + strconv.Itoa(i) + `,"method":"tools/call","params":{"name":"fixture_read","arguments":{}}}`
			externalHTTPRequest(h, session, http.MethodPost, body, nil)
		}(i)
	}
	for i := 0; i < 64; i++ {
		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			close(release)
			wg.Wait()
			t.Fatal("concurrent requests did not start")
		}
	}
	w := externalHTTPRequest(h, session, http.MethodPost, `{"jsonrpc":"2.0","id":100,"method":"ping"}`, nil)
	close(release)
	wg.Wait()
	if w.Code != 503 || w.Header().Get("Retry-After") != "1" {
		t.Fatal("unbounded concurrent admission", w.Code)
	}
}
