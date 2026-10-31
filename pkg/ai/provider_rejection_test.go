package ai

import "testing"

func TestProviderRejectionStatus(t *testing.T) {
	cases := map[string]int{
		"402 Budget exceeded: vk_model_config * budget exceeded": 402,
		"401 invalid x-api-key (request-id: abc)":                401,
		"anthropic: 403 forbidden":                               403,
		"429 rate limited":                                       0,
		"500 internal":                                           0,
		"529 overloaded":                                         0,
		"context canceled":                                       0,
		"read 4021 bytes":                                        0,
		"":                                                       0,
	}
	for msg, want := range cases {
		if got := ProviderRejectionStatus(msg); got != want {
			t.Errorf("ProviderRejectionStatus(%q) = %d, want %d", msg, got, want)
		}
	}
}
