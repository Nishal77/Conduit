package auth

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateAPIKey_Format(t *testing.T) {
	raw, hash, prefix, err := GenerateAPIKey()
	require.NoError(t, err)

	assert.True(t, IsAPIKey(raw), "generated key should satisfy IsAPIKey")
	assert.Len(t, raw, apiKeyTotalLen)
	assert.True(t, len(raw) > 4 && raw[:4] == apiKeyPrefix)
	assert.Equal(t, raw[:12], prefix)
	assert.Equal(t, HashKey(raw), hash)
	assert.Len(t, hash, 64) // hex-encoded SHA-256
}

func TestGenerateAPIKey_Unique(t *testing.T) {
	raw1, _, _, err := GenerateAPIKey()
	require.NoError(t, err)
	raw2, _, _, err := GenerateAPIKey()
	require.NoError(t, err)

	assert.NotEqual(t, raw1, raw2, "two generated keys must not collide")
}

func TestHashKey_Deterministic(t *testing.T) {
	assert.Equal(t, HashKey("cnd_same-input"), HashKey("cnd_same-input"))
	assert.NotEqual(t, HashKey("cnd_input-a"), HashKey("cnd_input-b"))
}

func TestIsAPIKey(t *testing.T) {
	raw, _, _, err := GenerateAPIKey()
	require.NoError(t, err)

	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"valid generated key", raw, true},
		{"missing prefix", "abcdefghijklmnopqrstuvwxyzabcdefghijklmnop12", false},
		{"too short", "cnd_short", false},
		{"too long", raw + "x", false},
		{"empty", "", false},
		{"jwt-shaped token", "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsAPIKey(tt.in))
		})
	}
}

func TestConstantTimeEqual(t *testing.T) {
	assert.True(t, constantTimeEqual("abc123", "abc123"))
	assert.False(t, constantTimeEqual("abc123", "abc124"))
	assert.False(t, constantTimeEqual("abc123", "abc12"))
}
