// Package mcp implements the Model Context Protocol (MCP) message types.
//
// MCP transports JSON-RPC 2.0 messages over Server-Sent Events (SSE). This
// file defines the wire format shared by every subsystem that touches an
// MCP message: the proxy (pass-through forwarding), the policy engine
// (tool_name matching), and the audit writer (recording tool calls).
package mcp

import "encoding/json"

// Version is the MCP protocol version Conduit speaks to upstream servers.
const Version = "2025-11-05"

// JSONRPCVersion is the JSON-RPC version used by every MCP message.
const JSONRPCVersion = "2.0"

// Standard JSON-RPC 2.0 error codes (https://www.jsonrpc.org/specification#error_object).
const (
	ErrCodeParseError     = -32700
	ErrCodeInvalidRequest = -32600
	ErrCodeMethodNotFound = -32601
	ErrCodeInvalidParams  = -32602
	ErrCodeInternalError  = -32603
)

// ID can be a string, a number, or null (notifications omit it entirely).
// We defer parsing with json.RawMessage so we never need to guess its type.
type ID = json.RawMessage

// Message is the base JSON-RPC 2.0 envelope. Every MCP message on the wire —
// request, response, or notification — unmarshals into this one struct;
// which fields are populated determines which kind it is (see IsRequest,
// IsNotification, IsResponse below).
type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"` // absent on notifications
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// IsRequest reports whether the message has both a method and an id.
func (m *Message) IsRequest() bool {
	return m.Method != "" && m.ID != nil
}

// IsNotification reports whether the message has a method but no id.
func (m *Message) IsNotification() bool {
	return m.Method != "" && m.ID == nil
}

// IsResponse reports whether the message has an id but no method
// (i.e. it is either a success result or an error response).
func (m *Message) IsResponse() bool {
	return m.Method == "" && m.ID != nil
}

// RPCError is a JSON-RPC 2.0 error object.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Error implements the error interface so RPCError can be returned/wrapped
// like any other Go error.
func (e *RPCError) Error() string { return e.Message }

// --- MCP-specific param/result types ---

// InitializeParams is sent by the agent in the "initialize" request that
// opens an MCP session.
type InitializeParams struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    ClientCapabilities `json:"capabilities"`
	ClientInfo      ClientInfo         `json:"clientInfo"`
}

// ClientCapabilities advertises what the connecting agent supports.
type ClientCapabilities struct {
	Roots    *RootCapability     `json:"roots,omitempty"`
	Sampling *SamplingCapability `json:"sampling,omitempty"`
}

// RootCapability indicates the agent can expose filesystem "roots".
type RootCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// SamplingCapability indicates the agent supports server-initiated sampling.
// It carries no fields today; its presence alone is the signal.
type SamplingCapability struct{}

// ClientInfo identifies the connecting agent (used as audit.agent_id).
type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// InitializeResult is the upstream server's response to "initialize".
type InitializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    ServerCapabilities `json:"capabilities"`
	ServerInfo      ServerInfo         `json:"serverInfo"`
}

// ServerCapabilities advertises what the upstream MCP server supports.
type ServerCapabilities struct {
	Tools     *ToolsCapability     `json:"tools,omitempty"`
	Resources *ResourcesCapability `json:"resources,omitempty"`
	Prompts   *PromptsCapability   `json:"prompts,omitempty"`
}

// ToolsCapability describes the server's tools/* support.
type ToolsCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// ResourcesCapability describes the server's resources/* support.
type ResourcesCapability struct {
	Subscribe   bool `json:"subscribe,omitempty"`
	ListChanged bool `json:"listChanged,omitempty"`
}

// PromptsCapability describes the server's prompts/* support.
type PromptsCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// ServerInfo identifies the upstream MCP server.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ToolCallParams is the params object for a "tools/call" request. Name is
// the fully-qualified tool name (e.g. "github/create_issue") that policy
// rules and audit events key off of.
type ToolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// ToolCallResult is the result object for a "tools/call" response.
type ToolCallResult struct {
	Content []ContentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

// ContentBlock is one item in a tool call result. Type determines which of
// Text/Data is populated.
type ContentBlock struct {
	Type     string `json:"type"`           // "text" | "image" | "resource"
	Text     string `json:"text,omitempty"` // when type == "text"
	Data     string `json:"data,omitempty"` // base64, when type == "image"
	MimeType string `json:"mimeType,omitempty"`
}

// ToolListResult is the result object for a "tools/list" request.
type ToolListResult struct {
	Tools      []Tool  `json:"tools"`
	NextCursor *string `json:"nextCursor,omitempty"`
}

// Tool describes a single callable tool exposed by an upstream MCP server.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema"` // JSON Schema object
}

// CancelParams is the params object for a "$/cancelRequest" notification.
type CancelParams struct {
	ID json.RawMessage `json:"id"`
}

// ProgressParams is the params object for a "notifications/progress" message.
type ProgressParams struct {
	ProgressToken string  `json:"progressToken"`
	Progress      float64 `json:"progress"`
	Total         float64 `json:"total,omitempty"`
}
