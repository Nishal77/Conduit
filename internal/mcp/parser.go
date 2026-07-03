package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ErrInvalidMessage is returned when a message cannot be parsed as a valid
// JSON-RPC 2.0 envelope. Callers should respond with a parse-error response
// (see MakeErrorResponse with ErrCodeParseError) rather than crash.
var ErrInvalidMessage = errors.New("invalid MCP message")

// ParseMessage parses a raw JSON-RPC 2.0 message and validates the envelope.
//
// It intentionally does NOT validate the shape of params/result — those are
// method-specific and parsed lazily by whichever handler needs them (see
// ExtractToolName for an example). This keeps the hot-path parse cheap: one
// json.Unmarshal plus four field checks, no matter how large params is.
func ParseMessage(data []byte) (*Message, error) {
	var msg Message
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidMessage, err)
	}

	if msg.JSONRPC != JSONRPCVersion {
		return nil, fmt.Errorf("%w: jsonrpc version must be %q, got %q", ErrInvalidMessage, JSONRPCVersion, msg.JSONRPC)
	}

	// A well-formed message is a request/notification (method set), a
	// success response (result set), or an error response (error set).
	// An empty envelope with none of these is not valid JSON-RPC.
	if msg.Method == "" && msg.Result == nil && msg.Error == nil {
		return nil, fmt.Errorf("%w: must have one of method, result, or error", ErrInvalidMessage)
	}

	// Per the JSON-RPC 2.0 spec, result and error are mutually exclusive.
	if msg.Error != nil && msg.Result != nil {
		return nil, fmt.Errorf("%w: result and error must not both be set", ErrInvalidMessage)
	}

	return &msg, nil
}

// ExtractToolName returns the tool name from a "tools/call" request, or ""
// if msg is not a tools/call request (including malformed params — this is
// a best-effort lookup used for policy/audit labeling, not validation).
func ExtractToolName(msg *Message) string {
	if msg.Method != "tools/call" {
		return ""
	}
	var params ToolCallParams
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		return ""
	}
	return params.Name
}

// MakeErrorResponse builds a JSON-RPC 2.0 error response. id may be nil,
// which is valid when the failing request itself could not be parsed far
// enough to recover its id (e.g. ErrCodeParseError).
func MakeErrorResponse(id json.RawMessage, code int, message string) []byte {
	msg := Message{
		JSONRPC: JSONRPCVersion,
		ID:      id,
		Error: &RPCError{
			Code:    code,
			Message: message,
		},
	}
	// Message and RPCError are both trivially marshalable (no channels,
	// funcs, or cycles), so this can never fail.
	out, _ := json.Marshal(msg)
	return out
}

// MakeSuccessResponse builds a JSON-RPC 2.0 success response carrying result.
func MakeSuccessResponse(id json.RawMessage, result any) ([]byte, error) {
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("marshal result: %w", err)
	}
	msg := Message{
		JSONRPC: JSONRPCVersion,
		ID:      id,
		Result:  resultJSON,
	}
	out, err := json.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("marshal response: %w", err)
	}
	return out, nil
}
