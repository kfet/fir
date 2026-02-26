package providers

import (
	"net/url"
	"strings"
)

const (
	defaultOpenAIBaseURL    = "https://api.openai.com"
	openAIChatEndpoint      = "/chat/completions"
	openAIResponsesEndpoint = "/responses"
)

func openAIChatCompletionsURL(baseURL string) string {
	return buildOpenAIEndpointURL(baseURL, openAIChatEndpoint)
}

func openAIResponsesURL(baseURL string) string {
	return buildOpenAIEndpointURL(baseURL, openAIResponsesEndpoint)
}

func buildOpenAIEndpointURL(baseURL, endpoint string) string {
	base := strings.TrimRight(baseURL, "/")
	if base == "" {
		base = defaultOpenAIBaseURL
	}

	if hasOpenAIVersionSuffix(base) {
		return base + endpoint
	}
	return base + "/v1" + endpoint
}

func hasOpenAIVersionSuffix(base string) bool {
	parsed, err := url.Parse(base)
	if err != nil {
		return false
	}

	path := strings.Trim(parsed.Path, "/")
	if path == "" {
		return false
	}

	segments := strings.Split(path, "/")
	last := strings.ToLower(segments[len(segments)-1])
	return strings.HasPrefix(last, "v1")
}
