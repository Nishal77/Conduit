package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// ciphertextKey is the sentinel key EncryptAuthConfig wraps ciphertext
// under, so an encrypted auth_config value is still valid JSON (and thus
// round-trips through the jsonb column and pgx's map[string]any scanning
// without any custom SQL type) while being unambiguous to detect: a
// legitimate auth_config never legitimately needs a field named this.
const ciphertextKey = "_ciphertext"

// DeriveCredentialKey derives a 32-byte AES-256 key from Conduit's JWT
// signing secret via SHA-256, rather than requiring a second secret to
// configure and rotate. This is a standard key-derivation pattern (a
// single high-entropy secret, several purpose-specific keys derived from
// it) — auth.jwt_secret is already required to be 32+ random bytes
// (config.Validate), so it has enough entropy to derive from safely.
func DeriveCredentialKey(jwtSecret string) []byte {
	sum := sha256.Sum256([]byte("conduit-credential-encryption:" + jwtSecret))
	return sum[:]
}

// EncryptAuthConfig encrypts an mcp_servers.auth_config value with
// AES-256-GCM before it reaches PostgreSQL (spec/13-multitenant.md §5:
// "auth_config on upstream servers is encrypted at rest"). A nil or empty
// config encrypts to nil — there's nothing to protect if there's no
// credential, and auth_type "none" always has an empty config.
func EncryptAuthConfig(key []byte, config map[string]any) (map[string]any, error) {
	if len(config) == 0 {
		return nil, nil
	}

	plaintext, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("marshal auth_config: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("init cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("init gcm: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}

	sealed := gcm.Seal(nonce, nonce, plaintext, nil)
	return map[string]any{ciphertextKey: base64.StdEncoding.EncodeToString(sealed)}, nil
}

// DecryptAuthConfig reverses EncryptAuthConfig. A config that isn't in the
// encrypted wrapper shape is returned unchanged — this keeps rows written
// before encryption was enabled (or by a direct SQL insert during
// development) readable rather than failing outright.
func DecryptAuthConfig(key []byte, config map[string]any) (map[string]any, error) {
	if len(config) == 0 {
		return config, nil
	}

	rawCiphertext, ok := config[ciphertextKey].(string)
	if !ok {
		return config, nil
	}

	sealed, err := base64.StdEncoding.DecodeString(rawCiphertext)
	if err != nil {
		return nil, fmt.Errorf("decode auth_config ciphertext: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("init cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("init gcm: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(sealed) < nonceSize {
		return nil, fmt.Errorf("auth_config ciphertext is too short")
	}
	nonce, ciphertext := sealed[:nonceSize], sealed[nonceSize:]

	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt auth_config: %w", err)
	}

	var result map[string]any
	if err := json.Unmarshal(plaintext, &result); err != nil {
		return nil, fmt.Errorf("unmarshal decrypted auth_config: %w", err)
	}
	return result, nil
}
