package ai

import (
	"encoding/json"
	"strings"
)

// Bedrock account credentials.
//
// Amazon Bedrock accounts come in three flavours, all expressed as a single
// resolved string that fir's auth layer hands to the Bedrock provider via
// StreamOptions.ApiKey:
//
//   - bearer token  (CredentialType api_key)  — a plain string, used as the
//     Bedrock API key (skipstone WithBearerToken, suppresses SigV4).
//   - AWS IAM       (CredentialType aws_iam)   — a JSON envelope (prefixed with
//     BedrockIAMEnvelopePrefix) describing how skipstone should obtain SigV4
//     credentials: a named profile, or explicit static keys, plus a region.
//
// The envelope prefix lets the Bedrock provider distinguish an IAM config from
// an opaque bearer token without any out-of-band signalling.

// BedrockIAMEnvelopePrefix marks a resolved API-key string as a JSON-encoded
// BedrockIAMCreds envelope rather than an opaque bearer token. Bearer tokens
// never contain this prefix.
const BedrockIAMEnvelopePrefix = "fir-bedrock-iam:"

// BedrockIAMCreds carries the per-account AWS IAM configuration used to build a
// SigV4-signing Bedrock client.
type BedrockIAMCreds struct {
	// Mode is "profile" (resolve via a named ~/.aws profile) or "keys"
	// (explicit static credentials).
	Mode string `json:"mode"`
	// Profile is the ~/.aws profile name (Mode == "profile").
	Profile string `json:"profile,omitempty"`
	// AccessKey/SecretKey/SessionToken are explicit static credentials
	// (Mode == "keys").
	AccessKey    string `json:"accessKey,omitempty"`
	SecretKey    string `json:"secretKey,omitempty"`
	SessionToken string `json:"sessionToken,omitempty"`
	// Region is the AWS region for signing and endpoint selection.
	Region string `json:"region,omitempty"`
}

// EncodeBedrockIAMCreds serialises an IAM config into the prefixed envelope
// string carried in StreamOptions.ApiKey.
func EncodeBedrockIAMCreds(c BedrockIAMCreds) string {
	b, _ := json.Marshal(c)
	return BedrockIAMEnvelopePrefix + string(b)
}

// DecodeBedrockIAMCreds parses a resolved API-key string. ok is true only when
// the string is a well-formed IAM envelope; a plain bearer token returns
// (nil, false).
func DecodeBedrockIAMCreds(s string) (*BedrockIAMCreds, bool) {
	if !strings.HasPrefix(s, BedrockIAMEnvelopePrefix) {
		return nil, false
	}
	payload := strings.TrimPrefix(s, BedrockIAMEnvelopePrefix)
	var c BedrockIAMCreds
	if err := json.Unmarshal([]byte(payload), &c); err != nil {
		return nil, false
	}
	return &c, true
}
