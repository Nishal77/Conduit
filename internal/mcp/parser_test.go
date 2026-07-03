package mcp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseMessage_Request(t *testing.T) {
	data := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"github/create_issue","arguments":{}}}`)
	msg, err := ParseMessage(data)
	require.NoError(t, err)
	assert.True(t, msg.IsRequest())
	assert.False(t, msg.IsNotification())
	assert.False(t, msg.IsResponse())
	assert.Equal(t, "tools/call", msg.Method)
}

func TestParseMessage_Notification(t *testing.T) {
	data := []byte(`{"jsonrpc":"2.0","method":"notifications/progress","params":{"progressToken":"abc","progress":50}}`)
	msg, err := ParseMessage(data)
	require.NoError(t, err)
	assert.True(t, msg.IsNotification())
	assert.False(t, msg.IsRequest())
}

func TestParseMessage_SuccessResponse(t *testing.T) {
	data := []byte(`{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"ok"}]}}`)
	msg, err := ParseMessage(data)
	require.NoError(t, err)
	assert.True(t, msg.IsResponse())
	assert.Nil(t, msg.Error)
}

func TestParseMessage_ErrorResponse(t *testing.T) {
	data := []byte(`{"jsonrpc":"2.0","id":4,"error":{"code":-32601,"message":"Method not found"}}`)
	msg, err := ParseMessage(data)
	require.NoError(t, err)
	assert.True(t, msg.IsResponse())
	require.NotNil(t, msg.Error)
	assert.Equal(t, ErrCodeMethodNotFound, msg.Error.Code)
}

func TestParseMessage_RejectsWrongVersion(t *testing.T) {
	_, err := ParseMessage([]byte(`{"jsonrpc":"1.0","id":1,"method":"ping"}`))
	assert.ErrorIs(t, err, ErrInvalidMessage)
}

func TestParseMessage_RejectsEmptyEnvelope(t *testing.T) {
	_, err := ParseMessage([]byte(`{"jsonrpc":"2.0","id":1}`))
	assert.ErrorIs(t, err, ErrInvalidMessage)
}

func TestParseMessage_RejectsResultAndErrorTogether(t *testing.T) {
	_, err := ParseMessage([]byte(`{"jsonrpc":"2.0","id":1,"result":{},"error":{"code":-1,"message":"x"}}`))
	assert.ErrorIs(t, err, ErrInvalidMessage)
}

func TestParseMessage_RejectsMalformedJSON(t *testing.T) {
	_, err := ParseMessage([]byte(`not json`))
	assert.ErrorIs(t, err, ErrInvalidMessage)
}

func TestExtractToolName(t *testing.T) {
	msg, err := ParseMessage([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"github/create_issue","arguments":{}}}`))
	require.NoError(t, err)
	assert.Equal(t, "github/create_issue", ExtractToolName(msg))
}

func TestExtractToolName_NotToolCall(t *testing.T) {
	msg, err := ParseMessage([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	require.NoError(t, err)
	assert.Equal(t, "", ExtractToolName(msg))
}

func TestExtractToolName_MalformedParams(t *testing.T) {
	msg, err := ParseMessage([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":"not-an-object"}`))
	require.NoError(t, err)
	assert.Equal(t, "", ExtractToolName(msg))
}

func TestMakeErrorResponse(t *testing.T) {
	out := MakeErrorResponse(json.RawMessage(`1`), ErrCodeMethodNotFound, "Method not found")
	msg, err := ParseMessage(out)
	require.NoError(t, err)
	require.NotNil(t, msg.Error)
	assert.Equal(t, ErrCodeMethodNotFound, msg.Error.Code)
	assert.Equal(t, "Method not found", msg.Error.Message)
}

func TestMakeSuccessResponse(t *testing.T) {
	out, err := MakeSuccessResponse(json.RawMessage(`1`), ToolCallResult{
		Content: []ContentBlock{{Type: "text", Text: "issue #42 created"}},
	})
	require.NoError(t, err)

	msg, err := ParseMessage(out)
	require.NoError(t, err)
	assert.True(t, msg.IsResponse())

	var result ToolCallResult
	require.NoError(t, json.Unmarshal(msg.Result, &result))
	assert.Equal(t, "issue #42 created", result.Content[0].Text)
}
