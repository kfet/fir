package ratelimit

import (
	"testing"
	"time"

	"github.com/kfet/fir/pkg/ai"
)

// --- helpers ---

func makeErrMsg(text string) *ai.AssistantMessage {
	return &ai.AssistantMessage{
		Role:         ai.RoleAssistant,
		StopReason:   ai.StopReasonError,
		ErrorMessage: text,
	}
}

// --- IsRateLimitText ---

func TestIsRateLimitText(t *testing.T) {
	hits := []string{
		"429 Too Many Requests",
		"HTTP 429",
		"Error 429: quota exceeded",
		"529 Overloaded",
		"HTTP 529",
		"rate limit exceeded",
		"rate_limit exceeded",
		"rate-limit exceeded",
		"Rate Limit Reached",
		"resource exhausted",
		"Resource Exhausted",
		"RESOURCE_EXHAUSTED: quota exceeded",
		"quota exceeded for model",
		"usage limit reached",
		"too many requests",
		"Too Many Requests",
		"model is overloaded",
		"The model is overloaded",
	}
	for _, tc := range hits {
		if !IsRateLimitText(tc) {
			t.Errorf("IsRateLimitText(%q) = false, want true", tc)
		}
	}

	misses := []string{
		"internal server error",
		"context deadline exceeded",
		"invalid API key",
		"bad request: missing field",
		"500 Internal Server Error",
	}
	for _, tc := range misses {
		if IsRateLimitText(tc) {
			t.Errorf("IsRateLimitText(%q) = true, want false", tc)
		}
	}
}

// --- ExtractRetryDelayFromText ---

func TestExtractRetryDelayFromText_NoDelay(t *testing.T) {
	cases := []string{
		"rate limit exceeded",
		"429 Too Many Requests",
		"quota exceeded",
		"",
	}
	for _, tc := range cases {
		if d := ExtractRetryDelayFromText(tc); d != 0 {
			t.Errorf("ExtractRetryDelayFromText(%q) = %v, want 0", tc, d)
		}
	}
}

func TestExtractRetryDelayFromText_ResetAfter(t *testing.T) {
	cases := []struct {
		input string
		want  time.Duration
	}{
		{
			"rate limit, reset after 39s",
			39 * time.Second,
		},
		{
			"quota exceeded, reset after 5m30s",
			5*time.Minute + 30*time.Second,
		},
		{
			"resource exhausted; reset after 18h31m10s",
			18*time.Hour + 31*time.Minute + 10*time.Second,
		},
		{
			"reset after 1h2m3.5s",
			1*time.Hour + 2*time.Minute + time.Duration(3.5*float64(time.Second)),
		},
		{
			"RESET AFTER 2h0m0s", // case-insensitive
			2 * time.Hour,
		},
	}
	for _, tc := range cases {
		got := ExtractRetryDelayFromText(tc.input)
		if got != tc.want {
			t.Errorf("ExtractRetryDelayFromText(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

func TestExtractRetryDelayFromText_RetryIn(t *testing.T) {
	cases := []struct {
		input string
		want  time.Duration
	}{
		{
			"Please retry in 30s",
			30 * time.Second,
		},
		{
			"please retry in 500ms",
			500 * time.Millisecond,
		},
		{
			"retry after 10s",
			10 * time.Second,
		},
		{
			"retry after 2000ms",
			2000 * time.Millisecond,
		},
		{
			"Retry in 1.5s",
			time.Duration(1.5 * float64(time.Second)),
		},
	}
	for _, tc := range cases {
		got := ExtractRetryDelayFromText(tc.input)
		if got != tc.want {
			t.Errorf("ExtractRetryDelayFromText(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

func TestExtractRetryDelayFromText_RetryDelayJSON(t *testing.T) {
	cases := []struct {
		input string
		want  time.Duration
	}{
		{
			`{"error":{"retryDelay": "34.074824224s"}}`,
			time.Duration(34074824224) * time.Nanosecond, // 34.074824224s
		},
		{
			`"retryDelay": "5s"`,
			5 * time.Second,
		},
		{
			`"retryDelay": "250ms"`,
			250 * time.Millisecond,
		},
		{
			`"RETRYDELAY": "10s"`, // case-insensitive key
			10 * time.Second,
		},
	}
	for _, tc := range cases {
		got := ExtractRetryDelayFromText(tc.input)
		if got != tc.want {
			t.Errorf("ExtractRetryDelayFromText(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

// Priority: resetAfter > retryIn > retryDelayJSON
func TestExtractRetryDelayFromText_Priority(t *testing.T) {
	// resetAfter should win over retryIn
	input := `reset after 20s. Please retry in 5s`
	got := ExtractRetryDelayFromText(input)
	if got != 20*time.Second {
		t.Errorf("priority test got %v, want 20s", got)
	}
}

// --- DetectRateLimit ---

func TestDetectRateLimit_NilMessage(t *testing.T) {
	info := DetectRateLimit(nil)
	if info.IsRateLimit {
		t.Error("DetectRateLimit(nil).IsRateLimit = true, want false")
	}
}

func TestDetectRateLimit_NonErrorStopReason(t *testing.T) {
	msg := &ai.AssistantMessage{
		Role:         ai.RoleAssistant,
		StopReason:   ai.StopReasonStop,
		ErrorMessage: "rate limit exceeded",
	}
	info := DetectRateLimit(msg)
	if info.IsRateLimit {
		t.Error("DetectRateLimit with ai.StopReasonStop should return IsRateLimit=false")
	}
}

func TestDetectRateLimit_Non429Error(t *testing.T) {
	cases := []string{
		"internal server error",
		"context deadline exceeded",
		"invalid API key",
		"504 Gateway Timeout",
	}
	for _, errMsg := range cases {
		info := DetectRateLimit(makeErrMsg(errMsg))
		if info.IsRateLimit {
			t.Errorf("DetectRateLimit(%q).IsRateLimit = true, want false", errMsg)
		}
	}
}

func TestDetectRateLimit_429(t *testing.T) {
	info := DetectRateLimit(makeErrMsg("API error 429 Too Many Requests"))
	if !info.IsRateLimit {
		t.Error("expected IsRateLimit=true for 429 error")
	}
	if info.RetryAfter != 0 {
		t.Errorf("expected RetryAfter=0 (no delay in message), got %v", info.RetryAfter)
	}
}

func TestDetectRateLimit_RateLimit(t *testing.T) {
	for _, phrase := range []string{"rate limit exceeded", "Rate_Limit hit", "rate-limit"} {
		info := DetectRateLimit(makeErrMsg(phrase))
		if !info.IsRateLimit {
			t.Errorf("DetectRateLimit(%q).IsRateLimit = false, want true", phrase)
		}
	}
}

func TestDetectRateLimit_ResourceExhausted(t *testing.T) {
	info := DetectRateLimit(makeErrMsg("RESOURCE_EXHAUSTED: daily quota exceeded"))
	if !info.IsRateLimit {
		t.Error("expected IsRateLimit=true for resource exhausted")
	}
}

func TestDetectRateLimit_QuotaExceeded(t *testing.T) {
	info := DetectRateLimit(makeErrMsg("quota exceeded for this model"))
	if !info.IsRateLimit {
		t.Error("expected IsRateLimit=true for quota exceeded")
	}
}

func TestDetectRateLimit_UsageLimitReached(t *testing.T) {
	info := DetectRateLimit(makeErrMsg("usage limit reached for today"))
	if !info.IsRateLimit {
		t.Error("expected IsRateLimit=true for usage limit reached")
	}
}

func TestDetectRateLimit_TooManyRequests(t *testing.T) {
	info := DetectRateLimit(makeErrMsg("too many requests, please slow down"))
	if !info.IsRateLimit {
		t.Error("expected IsRateLimit=true for too many requests")
	}
}

func TestDetectRateLimit_Overloaded(t *testing.T) {
	info := DetectRateLimit(makeErrMsg("The model is currently overloaded"))
	if !info.IsRateLimit {
		t.Error("expected IsRateLimit=true for overloaded")
	}
}

func TestDetectRateLimit_WithRetryDelay_ResetAfter(t *testing.T) {
	info := DetectRateLimit(makeErrMsg("rate limit; reset after 1h30m0s"))
	if !info.IsRateLimit {
		t.Fatal("expected IsRateLimit=true")
	}
	want := 1*time.Hour + 30*time.Minute
	if info.RetryAfter != want {
		t.Errorf("RetryAfter = %v, want %v", info.RetryAfter, want)
	}
}

func TestDetectRateLimit_WithRetryDelay_RetryIn(t *testing.T) {
	info := DetectRateLimit(makeErrMsg("429 Too Many Requests. Please retry in 60s"))
	if !info.IsRateLimit {
		t.Fatal("expected IsRateLimit=true")
	}
	if info.RetryAfter != 60*time.Second {
		t.Errorf("RetryAfter = %v, want 60s", info.RetryAfter)
	}
}

func TestDetectRateLimit_WithRetryDelay_JSON(t *testing.T) {
	info := DetectRateLimit(makeErrMsg(`quota exceeded {"retryDelay": "45s"}`))
	if !info.IsRateLimit {
		t.Fatal("expected IsRateLimit=true")
	}
	if info.RetryAfter != 45*time.Second {
		t.Errorf("RetryAfter = %v, want 45s", info.RetryAfter)
	}
}

func TestDetectRateLimit_UnknownDelay(t *testing.T) {
	info := DetectRateLimit(makeErrMsg("rate limit exceeded"))
	if !info.IsRateLimit {
		t.Fatal("expected IsRateLimit=true")
	}
	if info.RetryAfter != 0 {
		t.Errorf("RetryAfter should be 0 when delay is unknown, got %v", info.RetryAfter)
	}
}

func TestDetectRateLimit_MessagePreserved(t *testing.T) {
	errText := "quota exceeded: please retry in 10s"
	info := DetectRateLimit(makeErrMsg(errText))
	if info.Message != errText {
		t.Errorf("Message = %q, want %q", info.Message, errText)
	}
}

func TestDetectRateLimit_AbortedStopReason(t *testing.T) {
	msg := &ai.AssistantMessage{
		Role:         ai.RoleAssistant,
		StopReason:   ai.StopReasonAborted,
		ErrorMessage: "rate limit exceeded",
	}
	info := DetectRateLimit(msg)
	if info.IsRateLimit {
		t.Error("AbortedStopReason should not be treated as rate limit")
	}
}

func TestDetectRateLimit_ToolUseStopReason(t *testing.T) {
	msg := &ai.AssistantMessage{
		Role:         ai.RoleAssistant,
		StopReason:   ai.StopReasonToolUse,
		ErrorMessage: "rate limit exceeded",
	}
	info := DetectRateLimit(msg)
	if info.IsRateLimit {
		t.Error("ToolUseStopReason should not be treated as rate limit")
	}
}

func TestIsTransientServerError(t *testing.T) {
	positives := []string{
		"Internal server error",
		"internal server error",
		"500 Internal server error",
		"502 Bad Gateway",
		"503 Service Unavailable",
	}
	for _, tc := range positives {
		if !IsTransientServerError(tc) {
			t.Errorf("IsTransientServerError(%q) = false, want true", tc)
		}
	}

	negatives := []string{
		"invalid_api_key",
		"bad request",
		"context canceled",
	}
	for _, tc := range negatives {
		if IsTransientServerError(tc) {
			t.Errorf("IsTransientServerError(%q) = true, want false", tc)
		}
	}
}

func TestIsRetryableError(t *testing.T) {
	// Rate limits
	if !IsRetryableError("429 Too Many Requests") {
		t.Error("expected rate limit to be retryable")
	}
	// Transient server errors
	if !IsRetryableError("Internal server error") {
		t.Error("expected server error to be retryable")
	}
	// Not retryable
	if IsRetryableError("invalid_api_key") {
		t.Error("expected invalid_api_key to NOT be retryable")
	}
}
