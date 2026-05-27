package ratelimit

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/ai/core"
)

// RateLimitInfo describes a detected rate-limit condition.
type RateLimitInfo struct {
	IsRateLimit bool
	RetryAfter  time.Duration // 0 = unknown / not parseable
	Message     string        // cleaned human-readable error text
}

// durationFromFloat converts a float64 value and unit string ("s" or "ms")
// to a time.Duration, rounding to the nearest nanosecond.
func durationFromFloat(val float64, unit string) time.Duration {
	var ns float64
	if strings.EqualFold(unit, "s") {
		ns = val * float64(time.Second)
	} else {
		ns = val * float64(time.Millisecond)
	}
	return time.Duration(math.Round(ns))
}

// resetAfterRe matches "reset after 18h31m10s", "reset after 39s", "reset after 1h2m3.5s"
var resetAfterRe = regexp.MustCompile(`(?i)reset after (?:(\d+)h)?(?:(\d+)m)?(\d+(?:\.\d+)?)s`)

// retryInRe matches "Please retry in 30s" / "Please retry in 500ms" / "retry after 10s"
var retryInRe = regexp.MustCompile(`(?i)(?:please\s+)?retry\s+(?:in|after)\s+([0-9.]+)(ms|s)`)

// retryDelayRe matches JSON fragment `"retryDelay": "34.074s"`
var retryDelayRe = regexp.MustCompile(`(?i)"retryDelay":\s*"([0-9.]+)(ms|s)"`)

// IsRateLimitText returns true if the given text signals a rate-limit condition.
// It is exported so provider-level retry loops can reuse the same detection logic.
// The implementation lives in pkg/ai/core; this is a re-export for existing callers.
func IsRateLimitText(text string) bool {
	return core.IsRateLimitText(text)
}

// IsTransientServerError returns true if the error text indicates a transient
// server-side failure (e.g. "Internal server error", HTTP 502/503/504) that is
// worth retrying, especially when no tokens have been consumed.
// Re-exports pkg/ai/core.IsTransientServerError.
func IsTransientServerError(text string) bool {
	return core.IsTransientServerError(text)
}

// IsTransientNetworkError returns true if the error text indicates a transient
// transport-level failure (TCP reset, broken pipe, DNS hiccup, TLS handshake
// timeout, HTTP/2 GOAWAY, unexpected EOF, …) that is safe to retry when
// streaming has not yet begun. Re-exports pkg/ai/core.IsTransientNetworkError.
func IsTransientNetworkError(text string) bool {
	return core.IsTransientNetworkError(text)
}

// IsRetryableError returns true if the error text is a rate-limit condition,
// a transient server error, or a transient network/transport error — i.e.
// safe to retry when streaming has not yet begun.
// Re-exports pkg/ai/core.IsRetryableError.
func IsRetryableError(text string) bool {
	return core.IsRetryableError(text)
}

// ExtractRetryDelayFromText parses a retry delay from the error message text alone
// (no HTTP headers). It returns 0 if no recognisable pattern is found.
//
// Supported patterns:
//   - "reset after 18h31m10s" / "reset after 39s"
//   - "Please retry in 30s" / "retry after 30s" / "retry after 500ms"
//   - `"retryDelay": "34.074824224s"`
func ExtractRetryDelayFromText(text string) time.Duration {
	// Pattern 1: "reset after Xh Ym Zs"
	if m := resetAfterRe.FindStringSubmatch(text); m != nil {
		var hours, minutes int
		var secs float64
		if m[1] != "" {
			hours, _ = strconv.Atoi(m[1])
		}
		if m[2] != "" {
			minutes, _ = strconv.Atoi(m[2])
		}
		secs, _ = strconv.ParseFloat(m[3], 64)
		total := time.Duration(hours)*time.Hour +
			time.Duration(minutes)*time.Minute +
			durationFromFloat(secs, "s")
		if total > 0 {
			return total
		}
	}

	// Pattern 2: "Please retry in Xs" / "retry after Xms"
	if m := retryInRe.FindStringSubmatch(text); m != nil {
		val, _ := strconv.ParseFloat(m[1], 64)
		if val > 0 {
			return durationFromFloat(val, m[2])
		}
	}

	// Pattern 3: `"retryDelay": "34.074824224s"`
	if m := retryDelayRe.FindStringSubmatch(text); m != nil {
		val, _ := strconv.ParseFloat(m[1], 64)
		if val > 0 {
			return durationFromFloat(val, m[2])
		}
	}

	return 0
}

// DetectRateLimit checks whether msg represents a rate-limit error and extracts
// any server-indicated retry delay from the error message text.
//
// It returns RateLimitInfo with IsRateLimit=false for nil messages, non-error
// stop reasons, or error messages that do not match any rate-limit pattern.
func DetectRateLimit(msg *ai.AssistantMessage) RateLimitInfo {
	if msg == nil || msg.StopReason != ai.StopReasonError {
		return RateLimitInfo{}
	}
	text := msg.ErrorMessage
	if !IsRateLimitText(text) {
		return RateLimitInfo{}
	}
	return RateLimitInfo{
		IsRateLimit: true,
		RetryAfter:  ExtractRetryDelayFromText(text),
		Message:     text,
	}
}
