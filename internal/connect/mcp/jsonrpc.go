// SPDX-License-Identifier: AGPL-3.0-only

package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/orevcs/ore/internal/connect/gateway"
	"github.com/orevcs/ore/internal/web"
)

// JSON-RPC 2.0 error codes.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

// ProtocolVersion is the newest MCP protocol revision this server speaks;
// SupportedVersions are the ones it negotiates down to.
const ProtocolVersion = "2025-06-18"

var SupportedVersions = []string{"2024-11-05", "2025-03-26", "2025-06-18", "2025-11-25"}

// Server is the MCP endpoint.
type Server struct {
	Web     *web.Server
	Gateway *gateway.Gateway
	// ReadOnly hides and disables the write tools (ORE_MCP_READ_ONLY=1).
	ReadOnly bool
	// Version is reported in serverInfo.
	Version string
	// ListTTL is advertised as _meta.ttlMs on tools/list (default 5 minutes).
	ListTTL time.Duration
	// MaxBody bounds a request body (default 4 MiB).
	MaxBody int64
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError is a JSON-RPC 2.0 error object. Returned by a method to produce a
// protocol-level error instead of a result.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("jsonrpc %d: %s", e.Code, e.Message) }

func rpcErr(code int, format string, a ...any) *RPCError {
	return &RPCError{Code: code, Message: fmt.Sprintf(format, a...)}
}

// ToolError is a tool execution failure: reported as a result with isError
// so the model can read it, not as a protocol error.
type ToolError struct {
	Message string
	Data    any
}

func (e *ToolError) Error() string { return e.Message }

func toolErr(format string, a ...any) *ToolError {
	return &ToolError{Message: fmt.Sprintf(format, a...)}
}

// ServeHTTP implements the stateless Streamable HTTP transport: every call
// is one POST with one JSON-RPC message; notifications get 202; the server
// never opens a stream and issues no session id.
func (m *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
	case http.MethodGet, http.MethodDelete:
		w.Header().Set("Allow", "POST")
		http.Error(w, "stateless MCP server: no server-initiated stream; POST JSON-RPC messages to this endpoint", http.StatusMethodNotAllowed)
		return
	default:
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("WWW-Authenticate", `Bearer realm="ore", error="invalid_token"`)
	p, ok := m.Gateway.Authenticate(m.Web, w, r)
	if !ok {
		return
	}
	w.Header().Del("WWW-Authenticate")
	maxBody := m.MaxBody
	if maxBody <= 0 {
		maxBody = 4 << 20
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil || int64(len(body)) > maxBody {
		writeRPC(w, http.StatusOK, rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: rpcErr(CodeInvalidRequest, "request body too large or unreadable")})
		return
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		writeRPC(w, http.StatusOK, rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: rpcErr(CodeInvalidRequest, "JSON-RPC batches are not supported; send one message per POST")})
		return
	}
	var req rpcRequest
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	if err := dec.Decode(&req); err != nil || dec.More() {
		msg := "parse error"
		if err != nil {
			msg = "parse error: " + err.Error()
		}
		writeRPC(w, http.StatusOK, rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: rpcErr(CodeParseError, "%s", msg)})
		return
	}
	id := req.ID
	if len(id) == 0 || string(id) == "null" {
		id = nil
	}
	if req.JSONRPC != "2.0" || req.Method == "" {
		if id == nil {
			id = json.RawMessage("null")
		}
		writeRPC(w, http.StatusOK, rpcResponse{JSONRPC: "2.0", ID: id, Error: rpcErr(CodeInvalidRequest, "invalid request: jsonrpc must be \"2.0\" and method is required")})
		return
	}
	if id == nil { // a notification (or a stray response): accepted, nothing to answer
		w.WriteHeader(http.StatusAccepted)
		return
	}
	// a closed session is refused for everything, and the refusal is on the ledger
	if p.Session != nil && p.Session.State != "open" {
		if _, err := m.Gateway.RecordRefusal(r.Context(), *p.Session, req.Method, gateway.CodeSessionClosed, "session is closed", map[string]any{"method": req.Method}); err != nil {
			m.Web.Log.Error("mcp: record closed-session use", "err", err)
		}
		w.Header().Set("WWW-Authenticate", `Bearer realm="ore", error="invalid_token", error_description="session closed"`)
		web.JSONError(w, http.StatusUnauthorized, fmt.Errorf("gateway session %s is closed", p.Session.ID))
		return
	}
	result, rerr := m.dispatch(r.Context(), r, p, req)
	resp := rpcResponse{JSONRPC: "2.0", ID: id}
	if rerr != nil {
		resp.Error = rerr
	} else {
		resp.Result = result
	}
	writeRPC(w, http.StatusOK, resp)
}

func writeRPC(w http.ResponseWriter, status int, resp rpcResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.Encode(resp)
}

func (m *Server) dispatch(ctx context.Context, r *http.Request, p gateway.Principal, req rpcRequest) (any, *RPCError) {
	switch req.Method {
	case "initialize":
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &params)
		version := ProtocolVersion
		for _, v := range SupportedVersions {
			if v == params.ProtocolVersion {
				version = v
			}
		}
		return map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "ore", "title": "Ore", "version": m.version()},
			"instructions":    m.instructions(p),
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		ttl := m.ListTTL
		if ttl <= 0 {
			ttl = 5 * time.Minute
		}
		return map[string]any{"tools": m.toolList(), "_meta": map[string]any{"ttlMs": ttl.Milliseconds()}}, nil
	case "tools/call":
		var params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil || params.Name == "" {
			return nil, rpcErr(CodeInvalidParams, "invalid params: name is required")
		}
		t, ok := tools[params.Name]
		if !ok {
			return nil, rpcErr(CodeInvalidParams, "unknown tool: %s", params.Name)
		}
		if params.Arguments == nil {
			params.Arguments = map[string]any{}
		}
		return m.callTool(ctx, r, p, t, params.Arguments), nil
	case "resources/list", "resources/templates/list", "prompts/list", "completion/complete", "logging/setLevel":
		return nil, rpcErr(CodeMethodNotFound, "method not supported: %s (this server exposes tools only)", req.Method)
	}
	return nil, rpcErr(CodeMethodNotFound, "method not found: %s", req.Method)
}

func (m *Server) version() string {
	if m.Version != "" {
		return m.Version
	}
	return "0"
}

func (m *Server) instructions(p gateway.Principal) string {
	var b strings.Builder
	b.WriteString("Ore: the open, autonomous software forge. Tools read repositories, changes, issues, pipeline runs, deployments and the ledger. ")
	switch {
	case m.ReadOnly:
		b.WriteString("This server is read-only (ORE_MCP_READ_ONLY=1): no write tools are available.")
	case p.Session != nil:
		fmt.Fprintf(&b, "This session is scoped to repository %s; the repo argument may be omitted. Writes (issues_open, changes_open, changes_request_review, deployments_request, issues_comment) go through the safe-outputs gateway: validated, rate-limited, never to the protected default branch, and recorded on the ledger with session %s.", p.Session.Repo, p.Session.ID)
	default:
		b.WriteString("Pass repo as org/name. Writes over MCP need a gateway session token (POST /api/v0/gateway/sessions); with the admin token the write tools are listed but refuse.")
	}
	return b.String()
}

// toolList returns the tool descriptors in deterministic (name) order; write
// tools are omitted in read-only mode.
func (m *Server) toolList() []map[string]any {
	names := make([]string, 0, len(tools))
	for n, t := range tools {
		if m.ReadOnly && !t.ReadOnly {
			continue
		}
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]map[string]any, 0, len(names))
	for _, n := range names {
		t := tools[n]
		d := map[string]any{
			"name":        t.Name,
			"title":       t.Title,
			"description": t.Description,
			"inputSchema": t.InputSchema,
			"annotations": map[string]any{"title": t.Title, "readOnlyHint": t.ReadOnly, "destructiveHint": false, "idempotentHint": t.ReadOnly, "openWorldHint": false},
		}
		out = append(out, d)
	}
	return out
}

// callTool runs a tool and shapes the MCP result. Protocol errors were
// handled by the caller; everything here is a result, failed or not.
func (m *Server) callTool(ctx context.Context, r *http.Request, p gateway.Principal, t *Tool, args map[string]any) map[string]any {
	c := &Call{Server: m, Principal: p, Args: args, Request: r}
	if m.ReadOnly && !t.ReadOnly {
		return errorResult(toolErr("read-only mode: %s is disabled (ORE_MCP_READ_ONLY=1)", t.Name))
	}
	res, err := t.Handler(ctx, c)
	if err != nil {
		var te *ToolError
		if errors.As(err, &te) {
			return errorResult(te)
		}
		return errorResult(&ToolError{Message: err.Error()})
	}
	return res.result()
}

func errorResult(e *ToolError) map[string]any {
	out := map[string]any{"isError": true, "content": []map[string]any{{"type": "text", "text": e.Message}}}
	if e.Data != nil {
		out["structuredContent"] = e.Data
	}
	return out
}
