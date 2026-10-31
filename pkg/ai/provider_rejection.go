package ai

import "regexp"

// providerRejectionRe matches an HTTP 401/402/403 status leading an error
// message, optionally behind "prefix: " wrappers (e.g. "anthropic: 402 ...").
var providerRejectionRe = regexp.MustCompile(`(?:^|: )(40[123]) `)

// ProviderRejectionStatus reports the HTTP status (401, 402 or 403) when msg
// is a non-retryable provider rejection — auth failure, budget/billing
// exhausted, forbidden — and 0 otherwise. Retrying such a request cannot
// succeed, so callers should fail the turn rather than treat it as output.
func ProviderRejectionStatus(msg string) int {
	m := providerRejectionRe.FindStringSubmatch(msg)
	if m == nil {
		return 0
	}
	return int(m[1][2]-'0') + 400
}
