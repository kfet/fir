package ai

import (
	"strings"
	"testing"
)

// TestBedrockIAMCredsRoundTrip covers the envelope encoding used to smuggle an
// AWS IAM configuration through StreamOptions.ApiKey, which is otherwise an
// opaque bearer-token string.
func TestBedrockIAMCredsRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		creds BedrockIAMCreds
	}{
		{
			name:  "named profile",
			creds: BedrockIAMCreds{Mode: "profile", Profile: "work", Region: "eu-west-1"},
		},
		{
			name: "static keys",
			creds: BedrockIAMCreds{
				Mode:         "keys",
				AccessKey:    "AKIAEXAMPLE",
				SecretKey:    "s3cr3t",
				SessionToken: "tok",
				Region:       "us-east-1",
			},
		},
		{
			name:  "zero value",
			creds: BedrockIAMCreds{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			enc := EncodeBedrockIAMCreds(tt.creds)
			if !strings.HasPrefix(enc, BedrockIAMEnvelopePrefix) {
				t.Fatalf("encoded envelope %q lacks prefix %q", enc, BedrockIAMEnvelopePrefix)
			}
			got, ok := DecodeBedrockIAMCreds(enc)
			if !ok {
				t.Fatalf("DecodeBedrockIAMCreds(%q) reported not-an-envelope", enc)
			}
			if *got != tt.creds {
				t.Errorf("round-trip mismatch: got %+v, want %+v", *got, tt.creds)
			}
		})
	}
}

// TestDecodeBedrockIAMCredsRejects covers the two negative paths the Bedrock
// provider relies on to tell an IAM envelope from a plain bearer token.
func TestDecodeBedrockIAMCredsRejects(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"empty string", ""},
		{"plain bearer token", "ABSK-not-an-envelope"},
		{"prefix but malformed json", BedrockIAMEnvelopePrefix + "{not json"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := DecodeBedrockIAMCreds(tt.in)
			if ok {
				t.Errorf("expected %q to be rejected, got %+v", tt.in, got)
			}
			if got != nil {
				t.Errorf("expected nil creds on rejection, got %+v", got)
			}
		})
	}
}
