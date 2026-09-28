// Package mcp exposes the shared read-only Tool Catalog over stdio and HTTP.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"strconv"

	"github.com/bytedance/sonic"
	"github.com/quant4dad/internal/application"
	"github.com/quant4dad/internal/service"
	"github.com/quant4dad/internal/utils/strictjson"
	"gorm.io/gorm"
)

const protocolVersion = "2024-11-05"
const maxRequestBytes = 256 << 10
const maxResponseBytes = 512 << 10

// Raw JSON is kept as bytes; sonic's AST/decoder checks the request before use.
type rawJSON []byte

func (v *rawJSON) UnmarshalJSON(data []byte) error { *v = append((*v)[:0], data...); return nil }
func (v rawJSON) MarshalJSON() ([]byte, error) {
	if len(v) == 0 {
		return []byte("null"), nil
	}
	return v, nil
}

type rpcRequest struct {
	JSONRPC string  `json:"jsonrpc"`
	ID      rawJSON `json:"id,omitempty"`
	Method  string  `json:"method"`
	Params  rawJSON `json:"params,omitempty"`
}
type rpcResponse struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      rawJSON   `json:"id"`
	Result  any       `json:"result,omitempty"`
	Error   *rpcError `json:"error,omitempty"`
}
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
)

type Tool = application.ToolDefinition
type Server struct {
	name, version string
	catalog       *application.ToolCatalogApplication
	slots         chan struct{}
}

func NewServer(name, version string) *Server {
	return &Server{name: name, version: version, catalog: application.NewToolCatalogApplication(), slots: make(chan struct{}, 64)}
}

// Registration is startup-only; request handling never mutates the Catalog.
func (s *Server) Register(tool Tool) { s.catalog.Register(tool) }
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 4096), maxRequestBytes+1)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(bytes.TrimSpace(scanner.Bytes())) == 0 {
			continue
		}
		req, err := parseRequest(scanner.Bytes())
		var resp *rpcResponse
		if err != nil {
			resp = &rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: codeInvalidRequest, Message: "invalid request"}}
		} else {
			resp = s.handle(ctx, req)
		}
		if resp == nil {
			continue
		}
		data := encodeResponse(resp)
		if _, err := out.Write(append(data, '\n')); err != nil {
			return err
		}
	}
	if scanner.Err() != nil {
		return errors.New("mcp_request_read_failed")
	}
	return nil
}
func parseRequest(body []byte) (*rpcRequest, error) {
	var req rpcRequest
	if strictjson.DecodeNullable(body, &req, maxRequestBytes) != nil || req.JSONRPC != "2.0" || req.Method == "" || len(req.Method) > 128 {
		return nil, strictjson.ErrInvalid
	}
	if req.Method != "tools/call" && strictjson.Decode(body, &req, maxRequestBytes) != nil {
		return nil, strictjson.ErrInvalid
	}
	if len(req.ID) > 0 {
		if req.ID[0] == '"' {
			var id string
			if sonic.Unmarshal(req.ID, &id) != nil || len(id) > 128 {
				return nil, strictjson.ErrInvalid
			}
		} else {
			n, err := strconv.ParseInt(string(req.ID), 10, 64)
			if err != nil || n < -9007199254740991 || n > 9007199254740991 {
				return nil, strictjson.ErrInvalid
			}
		}
	}
	return &req, nil
}
func (s *Server) handle(ctx context.Context, req *rpcRequest) *rpcResponse {
	// A tools/call notification must never execute business code without a reply.
	if len(req.ID) == 0 {
		return nil
	}
	reply := func(result any, err *rpcError) *rpcResponse {
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result, Error: err}
	}
	switch req.Method {
	case "initialize":
		var in struct {
			ProtocolVersion string         `json:"protocolVersion"`
			Capabilities    map[string]any `json:"capabilities"`
			ClientInfo      map[string]any `json:"clientInfo"`
			Meta            map[string]any `json:"_meta"`
		}
		if len(req.Params) > 0 && strictjson.Decode(req.Params, &in, maxRequestBytes) != nil {
			return reply(nil, &rpcError{codeInvalidParams, "invalid params"})
		}
		negotiated := protocolVersion
		for _, version := range []string{"2024-11-05", "2025-03-26", "2025-06-18", "2025-11-25"} {
			if in.ProtocolVersion == version {
				negotiated = version
			}
		}
		return reply(map[string]any{"protocolVersion": negotiated, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": s.name, "version": s.version}}, nil)
	case "ping":
		return reply(map[string]any{}, nil)
	case "tools/list":
		return reply(map[string]any{"tools": s.listTools()}, nil)
	case "tools/call":
		result, err := s.handleToolCall(ctx, req.Params)
		return reply(result, err)
	default:
		return reply(nil, &rpcError{codeMethodNotFound, "method not found"})
	}
}
func (s *Server) listTools() []map[string]any {
	definitions := s.catalog.Definitions("external")
	return toolWire(definitions, s.catalog.Revision("external"))
}
func toolWire(definitions []application.ToolDefinition, revision string) []map[string]any {
	out := make([]map[string]any, 0, len(definitions))
	for _, t := range definitions {
		out = append(out, map[string]any{"name": t.Name, "description": t.Description, "inputSchema": t.InputSchema, "outputSchema": t.OutputSchema,
			"annotations": map[string]any{"readOnlyHint": t.Risk == "R0", "destructiveHint": t.Risk == "R2" || t.Risk == "R3", "idempotentHint": true, "openWorldHint": false},
			"_meta":       map[string]any{"q4d/risk": t.Risk, "q4d/catalog_revision": revision, "q4d/max_result_bytes": t.MaxResult, "q4d/timeout_ms": t.TimeoutMS, "q4d/deprecated": t.Deprecated}})
	}
	return out
}
func (s *Server) handleToolCall(ctx context.Context, params []byte) (any, *rpcError) {
	var in struct {
		Name      string         `json:"name"`
		Arguments rawJSON        `json:"arguments"`
		Meta      map[string]any `json:"_meta"`
	}
	if strictjson.DecodeNullable(params, &in, maxRequestBytes) != nil || in.Name == "" {
		return nil, &rpcError{codeInvalidParams, "invalid params"}
	}
	data, err := s.catalog.Invoke(ctx, "external", in.Name, in.Arguments)
	if errors.Is(err, application.ErrToolForbidden) {
		return nil, &rpcError{codeMethodNotFound, "tool unavailable"}
	}
	if err != nil {
		code := "tool_unavailable"
		switch {
		case errors.Is(err, service.ErrToolInput):
			code = "tool_invalid_arguments"
		case errors.Is(err, application.ErrToolResultTooLarge):
			code = "tool_result_too_large"
		case errors.Is(err, application.ErrToolTimeout):
			code = "tool_timeout"
		case errors.Is(err, gorm.ErrRecordNotFound):
			code = "tool_not_found"
		}
		return map[string]any{"isError": true, "content": []map[string]any{{"type": "text", "text": code}}}, nil
	}
	return map[string]any{"content": []map[string]any{{"type": "text", "text": string(data)}}, "structuredContent": rawJSON(data), "isError": false}, nil
}
func encodeResponse(resp *rpcResponse) []byte {
	// Raw tool schemas may contain JSON whitespace. Each response must remain
	// one physical line for both stdio and MCP SSE data frames.
	data, err := compactWireJSON.Marshal(resp)
	if err != nil || len(data) > maxResponseBytes {
		data, _ = sonic.Marshal(&rpcResponse{JSONRPC: "2.0", ID: resp.ID, Error: &rpcError{codeInternalError, "response unavailable"}})
	}
	return data
}

var compactWireJSON = sonic.Config{CompactMarshaler: true}.Froze()
