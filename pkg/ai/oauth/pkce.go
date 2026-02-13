// Ported from: packages/ai/src/utils/oauth/pkce.ts
// Upstream hash: 1caadb2e
package oauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

// PKCEChallenge holds a PKCE code verifier and its SHA-256 challenge.
type PKCEChallenge struct {
	Verifier  string
	Challenge string
}

// GeneratePKCE generates a PKCE code verifier (32 random bytes, base64url-encoded)
// and its corresponding SHA-256 challenge.
func GeneratePKCE() (*PKCEChallenge, error) {
	// Generate 32 random bytes for the verifier
	verifierBytes := make([]byte, 32)
	if _, err := rand.Read(verifierBytes); err != nil {
		return nil, err
	}
	verifier := base64URLEncode(verifierBytes)

	// Compute SHA-256 of the verifier string
	hash := sha256.Sum256([]byte(verifier))
	challenge := base64URLEncode(hash[:])

	return &PKCEChallenge{
		Verifier:  verifier,
		Challenge: challenge,
	}, nil
}

// base64URLEncode encodes bytes as a base64url string without padding.
func base64URLEncode(data []byte) string {
	return base64.RawURLEncoding.EncodeToString(data)
}
