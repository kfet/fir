package core

import "regexp"

// This file holds portable, dependency-free error-classification predicates
// shared by the agent loop and provider retry loops. They operate purely on
// error-message text (no fir-side or pkg/ai types), so they live in core where
// both pkg/agent and pkg/ai consumers can reach them. pkg/ai/ratelimit
// re-exports these for its existing callers.

// rateLimitPattern matches well-known rate-limit phrases in error messages.
var rateLimitPattern = regexp.MustCompile(
	`(?i)` +
		`429\b` + // HTTP 429 Too Many Requests
		`|529\b` + // HTTP 529 Overloaded (Anthropic)
		`|rate[_\s-]?limit` + // "rate limit", "rate_limit", "rate-limit"
		`|resource\s+exhausted` +
		`|quota\s+exceeded` +
		`|usage\s+limit\s+reached` +
		`|too\s+many\s+requests` +
		`|overloaded`,
)

// IsRateLimitText returns true if the given text signals a rate-limit condition.
// It is exported so provider-level retry loops can reuse the same detection logic.
func IsRateLimitText(text string) bool {
	return rateLimitPattern.MatchString(text)
}

// transientServerErrorPattern matches well-known transient server error phrases.
var transientServerErrorPattern = regexp.MustCompile(
	`(?i)` +
		`internal\s+server\s+error` +
		`|502\b` + // Bad Gateway
		`|503\b` + // Service Unavailable
		`|504\b`, // Gateway Timeout
)

// IsTransientServerError returns true if the error text indicates a transient
// server-side failure (e.g. "Internal server error", HTTP 502/503/504) that is
// worth retrying, especially when no tokens have been consumed.
func IsTransientServerError(text string) bool {
	return transientServerErrorPattern.MatchString(text)
}

// transientNetworkErrorPattern matches well-known transient network/transport
// failure phrases that surface from Go's net stack or HTTP client. These are
// connection-level errors (not protocol-level) that are safe to retry when
// streaming has not yet begun.
var transientNetworkErrorPattern = regexp.MustCompile(
	`(?i)` +
		`connection\s+reset\s+by\s+peer` +
		`|broken\s+pipe` +
		`|connection\s+refused` +
		`|connection\s+timed\s+out` +
		`|no\s+such\s+host` +
		`|network\s+is\s+unreachable` +
		`|tls\s+handshake\s+timeout` +
		`|i/o\s+timeout` +
		`|unexpected\s+EOF` +
		`|stream\s+error.*INTERNAL_ERROR` + // HTTP/2 stream resets
		`|http2:\s+server\s+sent\s+GOAWAY` +
		`|use\s+of\s+closed\s+network\s+connection` +
		`|EOF\s*$`, // bare trailing "EOF" from net/http when server hangs up
)

// IsTransientNetworkError returns true if the error text indicates a transient
// transport-level failure (TCP reset, broken pipe, DNS hiccup, TLS handshake
// timeout, HTTP/2 GOAWAY, unexpected EOF, …) that is safe to retry when
// streaming has not yet begun.
func IsTransientNetworkError(text string) bool {
	return transientNetworkErrorPattern.MatchString(text)
}

// IsRetryableError returns true if the error text is a rate-limit condition,
// a transient server error, or a transient network/transport error — i.e.
// safe to retry when streaming has not yet begun.
func IsRetryableError(text string) bool {
	return IsRateLimitText(text) || IsTransientServerError(text) || IsTransientNetworkError(text)
}
