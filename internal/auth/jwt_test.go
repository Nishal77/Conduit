package auth

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestJWTValidator_NotYetSupported(t *testing.T) {
	v := NewJWTValidator("some-secret-at-least-32-characters-long", "https://conduit")
	_, err := v.Validate(context.Background(), "any-token")
	assert.ErrorIs(t, err, ErrJWTNotSupported)
}
