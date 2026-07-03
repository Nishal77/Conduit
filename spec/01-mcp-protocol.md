# Spec 01 — MCP Protocol

> Phase: P1 | Files: `internal/mcp/types.go`, `internal/mcp/parser.go`, `internal/mcp/parser_fuzz_test.go`

---

## 1. Protocol Overview

MCP uses **JSON-RPC 2.0** messages transported over **Server-Sent Events (SSE)**.

- Client → Server: HTTP POST with JSON body
- Server → Client: SSE stream (`text/event-stream`) with JSON-encoded events
- Each SSE event has `data: <json>\n\n` format
- Connection is long-lived; multiple tool calls share one SSE connection

Conduit sits in the middle: it receives the POST, forwards it to the upstream MCP
server, and pipes the SSE response back to the agent.

---

## 2. JSON-RPC 2.0 Message Format

### Request
```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "tools/call",
  "params": {
    "name": "github/create_issue",
    "arguments": {
      "title": "Bug report",
      "body": "Details here"
    }
  }
}
```

### Success Response
```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "content": [
      { "type": "text", "text": "Issue #42 created" }
    ]
  }
}
```

### Error Response
```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "error": {
    "code": -32601,
    "message": "Method not found",
    "data": {}
  }
}
```

### Notification (no id field)
```json
{
  "jsonrpc": "2.0",
  "method": "notifications/progress",
  "params": { "progressToken": "abc", "progress": 50, "total": 100 }
}
```

---

## 3. Go Types — `internal/mcp/types.go`

```go
package mcp

import "encoding/json"

// Version is the supported MCP protocol version.
const Version = "2025-11-05"

// JSONRPCVersion is always "2.0".
const JSONRPCVersion = "2.0"

// Standard JSON-RPC error codes.
const (
    ErrCodeParseError     = -32700
    ErrCodeInvalidRequest = -32600
    ErrCodeMethodNotFound = -32601
    ErrCodeInvalidParams  = -32602
    ErrCodeInternalError  = -32603
)

// ID can be a string, number, or null (for notifications).
// Use json.RawMessage to defer parsing.
type ID = json.RawMessage

// Message is the base JSON-RPC 2.0 envelope.
// All MCP messages are one of: Request, Response, or Notification.
type Message struct {
    JSONRPC string          `json:"jsonrpc"`
    ID      json.RawMessage `json:"id,omitempty"`   // absent on notifications
    Method  string          `json:"method,omitempty"`
    Params  json.RawMessage `json:"params,omitempty"`
    Result  json.RawMessage `json:"result,omitempty"`
    Error   *RPCError       `json:"error,omitempty"`
}

// IsRequest returns true if the message has both a method and an id.
func (m *Message) IsRequest() bool {
    return m.Method != "" && m.ID != nil
}

// IsNotification returns true if the message has a method but no id.
func (m *Message) IsNotification() bool {
    return m.Method != "" && m.ID == nil
}

// IsResponse returns true if the message has an id but no method.
func (m *Message) IsResponse() bool {
    return m.Method == "" && m.ID != nil
}

// RPCError is a JSON-RPC 2.0 error object.
type RPCError struct {
    Code    int             `json:"code"`
    Message string          `json:"message"`
    Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string { return e.Message }

// --- MCP-specific param types ---

// InitializeParams is sent in the "initialize" request.
type InitializeParams struct {
    ProtocolVersion string             `json:"protocolVersion"`
    Capabilities    ClientCapabilities `json:"capabilities"`
    ClientInfo      ClientInfo         `json:"clientInfo"`
}

type ClientCapabilities struct {
    Roots    *RootCapability    `json:"roots,omitempty"`
    Sampling *SamplingCapability `json:"sampling,omitempty"`
}

type RootCapability struct {
    ListChanged bool `json:"listChanged,omitempty"`
}

type SamplingCapability struct{}

type ClientInfo struct {
    Name    string `json:"name"`
    Version string `json:"version"`
}

// InitializeResult is the response to "initialize".
type InitializeResult struct {
    ProtocolVersion string             `json:"protocolVersion"`
    Capabilities    ServerCapabilities `json:"capabilities"`
    ServerInfo      ServerInfo         `json:"serverInfo"`
}

type ServerCapabilities struct {
    Tools     *ToolsCapability     `json:"tools,omitempty"`
    Resources *ResourcesCapability `json:"resources,omitempty"`
    Prompts   *PromptsCapability   `json:"prompts,omitempty"`
}

type ToolsCapability struct {
    ListChanged bool `json:"listChanged,omitempty"`
}

type ResourcesCapability struct {
    Subscribe   bool `json:"subscribe,omitempty"`
    ListChanged bool `json:"listChanged,omitempty"`
}

type PromptsCapability struct {
    ListChanged bool `json:"listChanged,omitempty"`
}

type ServerInfo struct {
    Name    string `json:"name"`
    Version string `json:"version"`
}

// ToolCallParams is the params for "tools/call".
type ToolCallParams struct {
    Name      string          `json:"name"`
    Arguments json.RawMessage `json:"arguments,omitempty"`
}

// ToolCallResult is the result of "tools/call".
type ToolCallResult struct {
    Content []ContentBlock `json:"content"`
    IsError bool           `json:"isError,omitempty"`
}

// ContentBlock is one item in a tool result.
type ContentBlock struct {
    Type     string `json:"type"`           // "text" | "image" | "resource"
    Text     string `json:"text,omitempty"` // when type == "text"
    Data     string `json:"data,omitempty"` // base64, when type == "image"
    MimeType string `json:"mimeType,omitempty"`
}

// ToolListResult is the result of "tools/list".
type ToolListResult struct {
    Tools      []Tool  `json:"tools"`
    NextCursor *string `json:"nextCursor,omitempty"`
}

// Tool describes a single callable tool.
type Tool struct {
    Name        string          `json:"name"`
    Description string          `json:"description,omitempty"`
    InputSchema json.RawMessage `json:"inputSchema"` // JSON Schema object
}

// CancelParams is sent in "$/cancelRequest".
type CancelParams struct {
    ID json.RawMessage `json:"id"`
}

// ProgressParams is sent in "notifications/progress".
type ProgressParams struct {
    ProgressToken string  `json:"progressToken"`
    Progress      float64 `json:"progress"`
    Total         float64 `json:"total,omitempty"`
}
```

---

## 4. Parser — `internal/mcp/parser.go`

```go
package mcp

import (
    "encoding/json"
    "fmt"
)

// ErrInvalidMessage is returned when a message cannot be parsed.
var ErrInvalidMessage = fmt.Errorf("invalid MCP message")

// ParseMessage parses a raw JSON-RPC 2.0 message.
// Returns ErrInvalidMessage if the envelope is malformed.
// Does NOT validate params — params are parsed lazily by handlers.
func ParseMessage(data []byte) (*Message, error) {
    // Implementation must:
    // 1. json.Unmarshal into Message
    // 2. Verify jsonrpc == "2.0"
    // 3. Verify at least one of method, result, error is non-zero
    // 4. Verify that if error is set, result is absent
    // 5. Return ErrInvalidMessage on any violation
}

// ExtractToolName parses the tool name from a tools/call request.
// Returns "" if the message is not a tools/call request.
func ExtractToolName(msg *Message) string {
    // Parse ToolCallParams from msg.Params
    // Return params.Name
}

// MakeErrorResponse creates a JSON-RPC 2.0 error response.
func MakeErrorResponse(id json.RawMessage, code int, message string) []byte {
    // Returns marshalled Message with Error field set
}

// MakeSuccessResponse creates a JSON-RPC 2.0 success response.
func MakeSuccessResponse(id json.RawMessage, result any) ([]byte, error) {
    // Returns marshalled Message with Result field set
}
```

---

## 5. Fuzz Test — `internal/mcp/parser_fuzz_test.go`

```go
package mcp

import (
    "encoding/json"
    "testing"
)

func FuzzParseMessage(f *testing.F) {
    // Seed corpus — MUST include these exact messages:
    seeds := []string{
        `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"github/create_issue","arguments":{}}}`,
        `{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersion":"2025-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}`,
        `{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"ok"}]}}`,
        `{"jsonrpc":"2.0","method":"notifications/progress","params":{"progressToken":"abc","progress":50}}`,
        `{"jsonrpc":"2.0","id":4,"error":{"code":-32601,"message":"Method not found"}}`,
    }
    for _, s := range seeds {
        f.Add([]byte(s))
    }

    f.Fuzz(func(t *testing.T, data []byte) {
        // Must never panic
        msg, err := ParseMessage(data)
        if err != nil {
            return
        }
        // Round-trip must be stable
        re, err := json.Marshal(msg)
        if err != nil {
            t.Fatalf("marshal failed after successful parse: %v", err)
        }
        msg2, err := ParseMessage(re)
        if err != nil {
            t.Fatalf("re-parse failed: %v", err)
        }
        // Method and ID must be preserved
        if msg.Method != msg2.Method {
            t.Errorf("method changed on round-trip: %q → %q", msg.Method, msg2.Method)
        }
    })
}
```

---

## 6. MCP Methods — Conduit Handling Table

| Method | Has ID | Conduit Action |
|---|---|---|
| `initialize` | Yes | Pass-through; extract `clientInfo.name` as `agent_id` for audit |
| `initialized` | No (notification) | Pass-through; mark session as active |
| `tools/list` | Yes | Pass-through; optionally filter by policy |
| `tools/call` | Yes | **Full middleware chain**: auth→ratelimit→policy→plugin→forward→audit |
| `resources/list` | Yes | Pass-through |
| `resources/read` | Yes | Pass-through; optionally rate-limit per resource |
| `resources/subscribe` | Yes | Pass-through |
| `resources/unsubscribe` | Yes | Pass-through |
| `prompts/list` | Yes | Pass-through |
| `prompts/get` | Yes | Pass-through |
| `logging/setLevel` | Yes | Pass-through |
| `notifications/cancelled` | No | Pass-through; attempt upstream cancel |
| `notifications/progress` | No | Pass-through |
| `notifications/message` | No | Pass-through; log to audit at debug level |
| `notifications/resources/list_changed` | No | Pass-through |
| `notifications/tools/list_changed` | No | Pass-through |
| `$/cancelRequest` | No | Immediately cancel upstream request by ID |
| `ping` | Yes | Respond locally with `{}` — do NOT forward |

### Session lifecycle

```
Agent                    Conduit                 Upstream MCP Server
  │                         │                          │
  │──POST /mcp/t1/github───►│                          │
  │                         │──forward POST──────────►│
  │◄── SSE stream opens ────│◄─── SSE stream opens ───│
  │                         │                          │
  │──SSE: initialize req ──►│──SSE: initialize req ──►│
  │◄─ SSE: initialize res ──│◄─ SSE: initialize res ──│
  │                         │  (session now active)    │
  │──SSE: tools/call req ──►│  (run middleware chain)  │
  │                         │──SSE: tools/call req ──►│
  │◄─ SSE: tools/call res ──│◄─ SSE: tools/call res ──│
  │                         │  (write audit event)     │
  │──SSE: close ───────────►│──SSE: close ───────────►│
  │                         │  (flush audit queue)     │
```

---

## 7. SSE Event Format

Each SSE event from the upstream MCP server is:
```
data: {"jsonrpc":"2.0","id":1,"result":{...}}\n\n
```

Conduit MUST:
- Pass the `data:` prefix through unchanged
- Not buffer full events — stream line-by-line using `bufio.Scanner`
- Set `Content-Type: text/event-stream` on the proxy response
- Set `Cache-Control: no-cache` on the proxy response
- Set `X-Accel-Buffering: no` (disables nginx proxy buffering)
- Not modify any SSE event content (except plugin transforms)
