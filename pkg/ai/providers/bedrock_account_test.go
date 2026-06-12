package providers

import (
	"testing"

	"github.com/kfet/fir/pkg/ai"
)

func TestChooseBedrockAuth(t *testing.T) {
	bearer := "abcd.bearer.token"
	profileEnv := ai.EncodeBedrockIAMCreds(ai.BedrockIAMCreds{Mode: "profile", Profile: "work", Region: "eu-west-1"})
	keysEnv := ai.EncodeBedrockIAMCreds(ai.BedrockIAMCreds{Mode: "keys", AccessKey: "AK", SecretKey: "SK", SessionToken: "ST", Region: "us-west-2"})
	profileNoName := ai.EncodeBedrockIAMCreds(ai.BedrockIAMCreds{Mode: "profile", Region: "ap-south-1"})

	tests := []struct {
		name     string
		apiKey   string
		skipAuth bool
		want     bedrockAuthKind
		region   string
	}{
		{"skip-auth wins", bearer, true, bedrockAuthSkip, ""},
		{"bearer token", bearer, false, bedrockAuthBearer, ""},
		{"iam profile", profileEnv, false, bedrockAuthIAMProfile, "eu-west-1"},
		{"iam keys", keysEnv, false, bedrockAuthIAMKeys, "us-west-2"},
		{"iam profile missing name -> default chain", profileNoName, false, bedrockAuthDefaultChain, "ap-south-1"},
		{"empty -> default chain", "", false, bedrockAuthDefaultChain, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := chooseBedrockAuth(tc.apiKey, tc.skipAuth)
			if got.Kind != tc.want {
				t.Errorf("Kind = %v want %v", got.Kind, tc.want)
			}
			if got.Region != tc.region {
				t.Errorf("Region = %q want %q", got.Region, tc.region)
			}
		})
	}
}

func TestChooseBedrockAuth_KeysCarried(t *testing.T) {
	env := ai.EncodeBedrockIAMCreds(ai.BedrockIAMCreds{Mode: "keys", AccessKey: "AK", SecretKey: "SK", SessionToken: "ST", Region: "us-west-2"})
	c := chooseBedrockAuth(env, false)
	if c.AccessKey != "AK" || c.SecretKey != "SK" || c.SessionToken != "ST" {
		t.Errorf("static creds not carried: %+v", c)
	}
}

func TestBedrockIAMEnvelope_NotConfusedWithBearer(t *testing.T) {
	if _, ok := ai.DecodeBedrockIAMCreds("an-opaque-bearer-token"); ok {
		t.Error("bearer token wrongly decoded as IAM envelope")
	}
	enc := ai.EncodeBedrockIAMCreds(ai.BedrockIAMCreds{Mode: "profile", Profile: "p"})
	dec, ok := ai.DecodeBedrockIAMCreds(enc)
	if !ok || dec.Profile != "p" {
		t.Errorf("round-trip failed: %v %+v", ok, dec)
	}
}
