package agentbridge

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/quant4dad/internal/logger"
	"go.uber.org/zap"
)

var (
	ErrInvalidRequest    = errors.New("agent_bridge_invalid_request")
	ErrProtocol          = errors.New("agent_bridge_invalid_response")
	ErrIncompatible      = errors.New("agent_bridge_incompatible")
	ErrTransport         = errors.New("agent_bridge_transport_error")
	ErrStreamInterrupted = errors.New("agent_bridge_stream_interrupted")
	ErrStreamIdle        = errors.New("agent_bridge_stream_idle")
)

// Error never includes URLs, request/response bodies, credentials or raw network
// errors. OutcomeUnknown means a dispatched mutation requires reconciliation
// with the ORIGINAL identity before the caller can infer failure or submit anew.
type Error struct {
	Code           string
	Status         int
	OutcomeUnknown bool
	cause          error
}

func (e *Error) Error() string { return e.Code }
func (e *Error) Unwrap() error { return e.cause }

type Config struct {
	BaseURL           string // Trusted origin only: no path, userinfo, query or fragment.
	Token             string
	RequestTimeout    time.Duration // Default 20s; ordinary JSON calls only.
	HeaderTimeout     time.Duration // Default 10s; response header wait, including SSE.
	StreamIdleTimeout time.Duration // Default 45s; comments count as activity.
}

type Client struct {
	origin            string
	token             string
	http              *http.Client
	transport         *http.Transport
	requestTimeout    time.Duration
	streamIdleTimeout time.Duration
}

func New(cfg Config) (*Client, error) {
	u, err := url.Parse(cfg.BaseURL)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" ||
		u.User != nil || (u.Path != "" && u.Path != "/") || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery ||
		u.Fragment != "" || u.Opaque != "" || !credential(cfg.Token) ||
		cfg.RequestTimeout < 0 || cfg.HeaderTimeout < 0 || cfg.StreamIdleTimeout < 0 {
		return nil, ErrInvalidRequest
	}
	if cfg.RequestTimeout == 0 {
		cfg.RequestTimeout = 20 * time.Second
	}
	if cfg.HeaderTimeout == 0 {
		cfg.HeaderTimeout = 10 * time.Second
	}
	if cfg.StreamIdleTimeout == 0 {
		cfg.StreamIdleTimeout = 45 * time.Second
	}
	transport := &http.Transport{
		// Internal credentials must not follow ambient HTTP_PROXY or redirects.
		Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: cfg.HeaderTimeout,
		IdleConnTimeout: 90 * time.Second, MaxIdleConns: 32, MaxIdleConnsPerHost: 32, MaxConnsPerHost: 64,
		DisableCompression: true, ForceAttemptHTTP2: true,
	}
	client := &http.Client{Transport: transport, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	return &Client{origin: u.Scheme + "://" + u.Host, token: cfg.Token, http: client, transport: transport,
		requestTimeout: cfg.RequestTimeout, streamIdleTimeout: cfg.StreamIdleTimeout}, nil
}

func (c *Client) CloseIdleConnections() { c.transport.CloseIdleConnections() }

func (c *Client) Health(ctx context.Context) error {
	var value struct {
		Status string `json:"status"`
	}
	if err := c.json(ctx, http.MethodGet, "/health", nil, http.StatusOK, &value); err != nil {
		return err
	}
	if value.Status != "ready" {
		return ErrProtocol
	}
	return nil
}

func (c *Client) Capabilities(ctx context.Context) (Capabilities, error) {
	var value Capabilities
	if err := c.json(ctx, http.MethodGet, "/capabilities", nil, http.StatusOK, &value); err != nil {
		return Capabilities{}, err
	}
	if value.BridgeProtocol != 1 {
		return Capabilities{}, ErrIncompatible
	}
	if value.AdapterVersion == "" || value.DSHVersion == "" || value.ModelConfigRevision == "" ||
		(value.Backend != "acp" && value.Backend != "cordis") || value.SessionFormat < 0 || value.EventJournalFormat < 1 {
		return Capabilities{}, ErrProtocol
	}
	return value, nil
}

func (c *Client) CreateSession(ctx context.Context, request SessionCreate) (SessionCreated, error) {
	hash, err := ProvisionHash(request)
	if err != nil || !idPattern.MatchString(request.SessionID) || hash != request.ProvisionRequestHash {
		return SessionCreated{}, ErrInvalidRequest
	}
	var value SessionCreated
	if err := c.json(ctx, http.MethodPost, "/sessions", request, http.StatusOK, &value); err != nil {
		return SessionCreated{}, err
	}
	p := value.RuntimeProvenance
	if value.SessionID != request.SessionID || value.DSHSessionID != request.SessionID || !value.Durable ||
		value.ProvisionRequestHash != hash || value.Provider != request.Provider || value.Model != request.Model || value.Profile != request.Profile ||
		value.CreatedProfileRevision != request.ProfileRevision || value.CreatedModelConfigRevision != request.ModelConfigRevision ||
		p.BridgeProtocol != 1 || !textLength(p.AdapterVersion, 1, 128) || !textLength(p.DSHVersion, 1, 128) ||
		(p.Backend != "acp" && p.Backend != "cordis") || p.SessionFormat < 0 || p.EventJournalFormat != 1 || p.SessionBindingFormat != 1 {
		return SessionCreated{}, failure(ErrProtocol, http.StatusOK, true)
	}
	return value, nil
}

func (c *Client) CloseSession(ctx context.Context, sessionID string) (SessionClosed, error) {
	if !idPattern.MatchString(sessionID) {
		return SessionClosed{}, ErrInvalidRequest
	}
	var value SessionClosed
	if err := c.json(ctx, http.MethodPost, "/sessions/"+sessionID+"/close", EmptyData{}, http.StatusOK, &value); err != nil {
		return SessionClosed{}, err
	}
	if value.SessionID != sessionID || value.DSHSessionID != sessionID || !value.Durable || value.Loaded {
		return SessionClosed{}, failure(ErrProtocol, http.StatusOK, true)
	}
	return value, nil
}

func (c *Client) Prompt(ctx context.Context, request PromptRequest) (PromptAccepted, error) {
	if !idPattern.MatchString(request.SessionID) || !idPattern.MatchString(request.RunID) || !idPattern.MatchString(request.ClientRequestID) ||
		!hashPattern.MatchString(request.RequestHash) || !hashPattern.MatchString(request.ExecutionEnvelopeDigest) || !credential(request.RunCapability) ||
		len(request.Content) != 1 || request.Content[0].Type != "text" || !textLength(request.Content[0].Text, 1, 32000) {
		return PromptAccepted{}, ErrInvalidRequest
	}
	var value PromptAccepted
	if err := c.json(ctx, http.MethodPost, "/sessions/"+request.SessionID+"/prompts", request, http.StatusAccepted, &value); err != nil {
		return PromptAccepted{}, err
	}
	if value.RunID != request.RunID || !textLength(value.MessageID, 1, 128) || !value.Durable || value.State != "accepted" {
		return PromptAccepted{}, failure(ErrProtocol, http.StatusAccepted, true)
	}
	return value, nil
}

// GetRun binds both product identities, even though only runID is in the URL.
func (c *Client) GetRun(ctx context.Context, sessionID, runID string) (Run, error) {
	return c.run(ctx, sessionID, runID, "")
}
func (c *Client) CancelRun(ctx context.Context, sessionID, runID string) (Run, error) {
	return c.run(ctx, sessionID, runID, "cancel")
}
func (c *Client) ReconcileRun(ctx context.Context, sessionID, runID string) (Run, error) {
	return c.run(ctx, sessionID, runID, "reconcile")
}
func (c *Client) run(ctx context.Context, sessionID, runID string, action string) (Run, error) {
	if !idPattern.MatchString(sessionID) || !idPattern.MatchString(runID) {
		return Run{}, ErrInvalidRequest
	}
	method, route := http.MethodGet, "/runs/"+runID
	var body any
	if action != "" {
		method = http.MethodPost
		route += "/" + action
		body = EmptyData{}
	}
	var value Run
	if err := c.json(ctx, method, route, body, http.StatusOK, &value); err != nil {
		return Run{}, err
	}
	if !validRun(value, sessionID, runID) {
		return Run{}, failure(ErrProtocol, http.StatusOK, action != "")
	}
	return value, nil
}

func (c *Client) DecideApproval(ctx context.Context, approvalID string, request ApprovalDecision) (ApprovalAcknowledged, error) {
	if !approvalPattern.MatchString(approvalID) || !idPattern.MatchString(request.RunID) || !ulidPattern.MatchString(request.ToolCallID) ||
		(request.Decision != "allow_once" && request.Decision != "reject") ||
		(request.Decision == "allow_once" && !credential(request.ApprovalReceipt)) ||
		(request.Decision == "reject" && request.ApprovalReceipt != "") {
		return ApprovalAcknowledged{}, ErrInvalidRequest
	}
	var value ApprovalAcknowledged
	if err := c.json(ctx, http.MethodPost, "/approvals/"+url.PathEscape(approvalID)+"/decision", request, http.StatusOK, &value); err != nil {
		return ApprovalAcknowledged{}, err
	}
	if value.RunID != request.RunID || value.ToolCallID != request.ToolCallID || value.Decision != request.Decision {
		return ApprovalAcknowledged{}, failure(ErrProtocol, http.StatusOK, true)
	}
	return value, nil
}

func (c *Client) Transcript(ctx context.Context, sessionID string, query TranscriptQuery) (TranscriptPage, error) {
	if !idPattern.MatchString(sessionID) {
		return TranscriptPage{}, ErrInvalidRequest
	}
	suffix, err := queryString(query)
	if err != nil {
		return TranscriptPage{}, err
	}
	var value TranscriptPage
	if err := c.json(ctx, http.MethodGet, "/sessions/"+sessionID+suffix, nil, http.StatusOK, &value); err != nil {
		return TranscriptPage{}, err
	}
	if !validTranscript(value, sessionID, query) {
		return TranscriptPage{}, ErrProtocol
	}
	return value, nil
}

const maxJSONBytes = 1024 * 1024

func (c *Client) json(ctx context.Context, method, route string, body any, status int, out any) error {
	ctx, cancel := context.WithTimeout(ctx, c.requestTimeout)
	defer cancel()
	response, err := c.send(ctx, method, route, body, "application/json", "")
	if err != nil {
		return err
	}
	defer response.Body.Close()
	mutation := method == http.MethodPost
	if response.StatusCode != status {
		return responseError(ctx, response, mutation)
	}
	if !contentType(response, "application/json") {
		return failure(ErrProtocol, response.StatusCode, mutation)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxJSONBytes+1))
	if err != nil {
		return transportFailure(ctx, mutation)
	}
	if len(data) > maxJSONBytes || decode(data, out) != nil {
		return failure(ErrProtocol, response.StatusCode, mutation)
	}
	return nil
}

func (c *Client) send(ctx context.Context, method, route string, body any, accept, cursor string) (*http.Response, error) {
	var data []byte
	if body != nil {
		var err error
		data, err = wireJSON.Marshal(body)
		if err != nil || len(data) > 256*1024 {
			return nil, ErrInvalidRequest
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, failure(err, 0, false)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.origin+"/q4d/v1"+route, bytes.NewReader(data))
	if err != nil {
		return nil, ErrInvalidRequest
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Accept", accept)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if cursor != "" {
		request.Header.Set("Last-Event-ID", cursor)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, transportFailure(ctx, method == http.MethodPost)
	}
	return response, nil
}

func contentType(response *http.Response, expected string) bool {
	media, params, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	return err == nil && media == expected && (params["charset"] == "" || strings.EqualFold(params["charset"], "utf-8")) && response.Header.Get("Content-Encoding") == ""
}
func failure(cause error, status int, unknown bool) error {
	code := cause.Error()
	if errors.Is(cause, context.Canceled) {
		code = "agent_bridge_cancelled"
	}
	if errors.Is(cause, context.DeadlineExceeded) {
		code = "agent_bridge_timeout"
	}
	return reportError(&Error{Code: code, Status: status, OutcomeUnknown: unknown, cause: cause})
}

func reportError(value *Error) error {
	// Only fixed protocol metadata is observable here, never payloads or URLs.
	if !errors.Is(value, context.Canceled) {
		logger.L().Error("Agent Bridge request failed", zap.String("code", value.Code),
			zap.Int("http_status", value.Status), zap.Bool("outcome_unknown", value.OutcomeUnknown))
	}
	return value
}
func transportFailure(ctx context.Context, unknown bool) error {
	if err := ctx.Err(); err != nil {
		return failure(err, 0, unknown)
	}
	return failure(ErrTransport, 0, unknown)
}

// Unknown/error-page content is never returned to callers, even if it matches
// an agent_* spelling. The server's documented error codes are an explicit list.
var serverCodes = map[string]int{
	"agent_session_not_found": 404, "agent_run_not_found": 404,
	"agent_approval_missing": 409, "agent_request_conflict": 409, "agent_run_in_progress": 409, "agent_run_recovering": 409,
	"agent_invalid_request": 400, "agent_invalid_context": 403, "agent_capability_expired": 403, "agent_capability_rejected": 403,
	"agent_runtime_unavailable": 503, "agent_session_conflict": 409, "agent_session_unbound": 409, "agent_session_provisioning": 409,
	"agent_session_storage_missing": 503, "agent_configuration_unavailable": 503, "agent_configuration_conflict": 409,
	"agent_unauthorized": 401, "agent_request_too_large": 413, "agent_unsupported_media_type": 415, "agent_invalid_json": 400,
	"agent_request_timeout": 408, "agent_operation_timeout": 504, "agent_bridge_busy": 503, "agent_backend_contract_error": 500,
	"agent_internal_error": 500, "agent_event_cursor_expired": 410, "agent_invalid_event_cursor": 400,
	"agent_transcript_item_too_large": 413,
	"agent_invalid_transcript_query":  400, "agent_transcript_snapshot_unavailable": 409,
	"agent_transcript_batch_unsupported": 500, "agent_transcript_unavailable": 500,
	"agent_invalid_query": 400, "agent_request_aborted": 400,
	"agent_method_not_allowed": 405, "agent_not_found": 404,
}

func responseError(ctx context.Context, response *http.Response, mutation bool) error {
	unknown := mutation && (response.StatusCode >= 500 || response.StatusCode == 408)
	if !contentType(response, "application/json") {
		return failure(ErrProtocol, response.StatusCode, mutation)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 8193))
	if err != nil {
		return transportFailure(ctx, mutation)
	}
	var value struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if len(data) > 8192 || decode(data, &value) != nil || serverCodes[value.Error.Code] != response.StatusCode {
		return failure(ErrProtocol, response.StatusCode, mutation)
	}
	return reportError(&Error{Code: value.Error.Code, Status: response.StatusCode, OutcomeUnknown: unknown})
}

// String keeps routine formatting of the client free of the internal token.
func (c *Client) String() string   { return "agentbridge.Client" }
func (c *Client) GoString() string { return c.String() }

func (c Config) String() string             { return "agentbridge.Config" }
func (c Config) GoString() string           { return c.String() }
func (p PromptRequest) String() string      { return "agentbridge.PromptRequest" }
func (p PromptRequest) GoString() string    { return p.String() }
func (a ApprovalDecision) String() string   { return "agentbridge.ApprovalDecision" }
func (a ApprovalDecision) GoString() string { return a.String() }

// Keep numeric HTTP status formatting independent of any response text.
func (e *Error) GoString() string {
	return "agentbridge.Error{code:" + e.Code + ",status:" + strconv.Itoa(e.Status) + "}"
}
