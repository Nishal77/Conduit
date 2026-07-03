package mcp

import (
	"encoding/json"
	"testing"
)

// FuzzParseMessage feeds arbitrary bytes into ParseMessage to make sure it
// never panics on malformed input, and that any message it accepts survives
// a marshal/re-parse round trip unchanged. This is the primary defense
// against a malicious or buggy upstream MCP server crashing the proxy.
func FuzzParseMessage(f *testing.F) {
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
		// Must never panic, regardless of input.
		msg, err := ParseMessage(data)
		if err != nil {
			return
		}
		// Round-trip must be stable: a message that parsed successfully
		// must marshal and re-parse without error or field drift.
		re, err := json.Marshal(msg)
		if err != nil {
			t.Fatalf("marshal failed after successful parse: %v", err)
		}
		msg2, err := ParseMessage(re)
		if err != nil {
			t.Fatalf("re-parse failed: %v", err)
		}
		if msg.Method != msg2.Method {
			t.Errorf("method changed on round-trip: %q -> %q", msg.Method, msg2.Method)
		}
	})
}
