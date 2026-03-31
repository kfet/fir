package providers

import (
	"net/http"
	"strings"

	"github.com/kfet/fir/pkg/ai"
)

// BuildRequestHeaders constructs the final header map for an API request.
//
// The merge order (lowest → highest priority) is:
//  1. authHeaders   – provider-specific auth (e.g. {"Authorization": "Bearer xxx"})
//  2. model.Headers – static headers from model/provider config
//  3. options.Headers – per-request overrides from the caller
//
// skipPrefixes optionally filters keys from options.Headers (e.g. "x-anthropic-thinking-").
func BuildRequestHeaders(
	authHeaders map[string]string,
	model *ai.Model,
	options *ai.StreamOptions,
	skipPrefixes ...string,
) map[string]string {
	headers := make(map[string]string, len(authHeaders)+len(model.Headers)+optionsHeaderLen(options))

	for k, v := range authHeaders {
		headers[k] = v
	}
	for k, v := range model.Headers {
		headers[k] = v
	}
	if options != nil {
		for k, v := range options.Headers {
			if !hasAnyPrefix(k, skipPrefixes) {
				headers[k] = v
			}
		}
	}
	return headers
}

// ApplyHeaders writes all entries from a header map onto an http.Request.
func ApplyHeaders(req *http.Request, headers map[string]string) {
	for k, v := range headers {
		req.Header.Set(k, v)
	}
}

func optionsHeaderLen(options *ai.StreamOptions) int {
	if options == nil {
		return 0
	}
	return len(options.Headers)
}

func hasAnyPrefix(key string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}
