// Package mcp is a port: it speaks Model Context Protocol over stdio so a
// coding agent can ask Jasper a question mid-task instead of finding out at
// commit time.
//
// It holds no business logic — every handler calls exactly one service method,
// the same rule the CLI port follows.
//
// The transport is JSON-RPC 2.0, newline-delimited, on stdin/stdout. It is
// hand-written against encoding/json because DEC-003 caps Jasper's dependency
// list at one entry, and the protocol needed here is three methods wide.
//
// stdout carries protocol frames only. Anything diagnostic goes to stderr, or
// it corrupts the stream.
package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/rohan/jasper/internal/service"
)

const (
	// ServerVersion is this port's version, reported in initialize.
	ServerVersion = "0.1.0"

	// defaultProtocol is the MCP revision this server implements. A client
	// asking for a revision we know is echoed back; anything else gets this.
	defaultProtocol = "2025-06-18"
)

var knownProtocols = map[string]bool{
	"2024-11-05": true,
	"2025-03-26": true,
	"2025-06-18": true,
}

// instructions is handed to the client during initialize. It is the only place
// Jasper gets to explain itself to an agent that has never seen it, so it says
// when to call rather than what exists.
const instructions = `Jasper holds this project's recorded architectural decisions and can tell you
whether a change honors them.

Call architecture_brief once before you start working in an unfamiliar area.
Call can_import before adding an import that crosses a module boundary, and
can_add_dependency before adding any package — both answer for code that does
not exist yet, so ask before you write, not after.

If a call comes back DENIED, prefer changing your approach: the finding names
the decision and suggests a repair. If you believe the decision itself is wrong
or stale, call explain_decision to read the rationale, and only then use
propose_decision to put an alternative in front of a human. A proposed decision
does not take effect and does not unblock you.`

// request is an incoming JSON-RPC 2.0 message. A message with no id is a
// notification and gets no reply.
type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// JSON-RPC 2.0 reserved codes.
const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternal       = -32603
)

// Server wires the protocol to one Jasper service.
type Server struct {
	svc *service.Service
	log io.Writer // stderr; never stdout
}

func NewServer(svc *service.Service, logw io.Writer) *Server {
	return &Server{svc: svc, log: logw}
}

// Serve reads frames until in is exhausted. It returns nil on clean EOF, which
// is how a client shutting down looks.
//
// Framing is one JSON object per line, which is what MCP's stdio transport
// specifies. That matters for more than tidiness: a json.Decoder cannot
// resynchronise after a syntax error — it returns the same error forever — so
// streaming straight into one would spin on a single malformed frame. Reading
// a line at a time means a bad frame costs exactly one frame.
func (s *Server) Serve(in io.Reader, out io.Writer) error {
	// ReadString grows as needed, so there is no maximum frame size.
	rd := bufio.NewReader(in)
	enc := json.NewEncoder(out)

	for {
		line, readErr := rd.ReadString('\n')

		// A final frame without a trailing newline arrives together with EOF,
		// so the payload is handled before the error is considered.
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			if err := s.handleFrame([]byte(trimmed), enc); err != nil {
				return err
			}
		}

		if readErr != nil {
			if readErr == io.EOF {
				return nil
			}
			return readErr
		}
	}
}

// handleFrame processes one frame. The returned error is a write failure only;
// a bad request is answered, not propagated.
func (s *Server) handleFrame(frame []byte, enc *json.Encoder) error {
	var req request
	if err := json.Unmarshal(frame, &req); err != nil {
		return enc.Encode(response{
			JSONRPC: "2.0",
			ID:      json.RawMessage("null"),
			Error:   &rpcError{codeParse, "parse error: " + err.Error()},
		})
	}

	result, rerr := s.dispatch(req)

	// Notifications (no id) are never answered, even on error.
	if len(req.ID) == 0 || string(req.ID) == "null" {
		if rerr != nil {
			fmt.Fprintf(s.log, "jasper mcp: notification %s: %s\n", req.Method, rerr.Message)
		}
		return nil
	}

	resp := response{JSONRPC: "2.0", ID: req.ID}
	if rerr != nil {
		resp.Error = rerr
	} else {
		resp.Result = result
	}
	return enc.Encode(resp)
}

func (s *Server) dispatch(req request) (any, *rpcError) {
	if req.JSONRPC != "" && req.JSONRPC != "2.0" {
		return nil, &rpcError{codeInvalidRequest, "jsonrpc must be \"2.0\""}
	}
	switch req.Method {
	case "initialize":
		return s.initialize(req.Params)
	case "notifications/initialized", "notifications/cancelled":
		return nil, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": toolDefs()}, nil
	case "tools/call":
		return s.callTool(req.Params)
	default:
		return nil, &rpcError{codeMethodNotFound, fmt.Sprintf("unknown method %q", req.Method)}
	}
}

func (s *Server) initialize(raw json.RawMessage) (any, *rpcError) {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &p)
	}
	proto := defaultProtocol
	if knownProtocols[p.ProtocolVersion] {
		proto = p.ProtocolVersion
	}
	return map[string]any{
		"protocolVersion": proto,
		"capabilities": map[string]any{
			"tools": map[string]any{"listChanged": false},
		},
		"serverInfo": map[string]any{
			"name":    "jasper",
			"version": ServerVersion,
		},
		"instructions": instructions,
	}, nil
}
