package claudeusage

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// --- test helpers ---

func setUsageEndpoint(t *testing.T, u string) {
	t.Helper()
	old := usageEndpoint
	usageEndpoint = u
	t.Cleanup(func() { usageEndpoint = old })
}

func setNowFunc(t *testing.T, fn func() time.Time) {
	t.Helper()
	old := nowFunc
	nowFunc = fn
	t.Cleanup(func() { nowFunc = old })
}

// --- FetchUsage ---

func TestFetchUsageSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer test-token" {
			t.Errorf("Authorization = %q", auth)
		}
		if ua := r.Header.Get("User-Agent"); ua != userAgent {
			t.Errorf("User-Agent = %q", ua)
		}
		if beta := r.Header.Get("anthropic-beta"); beta != "oauth-2025-04-20" {
			t.Errorf("anthropic-beta = %q", beta)
		}

		reset := "2025-06-01T12:00:00Z"
		data := usageData{
			FiveHour: &windowData{Utilization: 22, ResetsAt: &reset},
			SevenDay: &windowData{Utilization: 5},
		}
		json.NewEncoder(w).Encode(data)
	}))
	defer server.Close()
	setUsageEndpoint(t, server.URL)

	data, err := fetchUsage("test-token")
	if err != nil {
		t.Fatalf("fetchUsage() error: %v", err)
	}
	if data.FiveHour == nil || data.FiveHour.Utilization != 22 {
		t.Errorf("FiveHour.Utilization = %v", data.FiveHour)
	}
	if data.SevenDay == nil || data.SevenDay.Utilization != 5 {
		t.Errorf("SevenDay.Utilization = %v", data.SevenDay)
	}
}

func TestFetchUsage401(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	setUsageEndpoint(t, server.URL)

	_, err := fetchUsage("bad-token")
	if err != errUnauthorized {
		t.Errorf("expected errUnauthorized, got %v", err)
	}
}

func TestFetchUsageServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	}))
	defer server.Close()
	setUsageEndpoint(t, server.URL)

	_, err := fetchUsage("token")
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should mention 500: %v", err)
	}
}

func TestFetchUsageNetworkError(t *testing.T) {
	setUsageEndpoint(t, "http://127.0.0.1:1")
	_, err := fetchUsage("token")
	if err == nil {
		t.Fatal("expected error for unreachable server")
	}
}

func TestFetchUsageInvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	}))
	defer server.Close()
	setUsageEndpoint(t, server.URL)

	_, err := fetchUsage("token")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

// --- ProgressBar ---

func TestProgressBar(t *testing.T) {
	tests := []struct {
		pct   float64
		width int
		want  string
	}{
		{0, 10, "▱▱▱▱▱▱▱▱▱▱"},
		{100, 10, "▰▰▰▰▰▰▰▰▰▰"},
		{50, 10, "▰▰▰▰▰▱▱▱▱▱"},
		{25, 4, "▰▱▱▱"},
		{-10, 5, "▱▱▱▱▱"},
		{150, 5, "▰▰▰▰▰"},
	}
	for _, tt := range tests {
		got := progressBar(tt.pct, tt.width)
		if got != tt.want {
			t.Errorf("progressBar(%.1f, %d) = %q, want %q", tt.pct, tt.width, got, tt.want)
		}
	}
}

// --- TimeUntil ---

func TestTimeUntil(t *testing.T) {
	now := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	setNowFunc(t, func() time.Time { return now })

	// Future: 2h30m
	future := now.Add(2*time.Hour + 30*time.Minute).Format(time.RFC3339)
	if got := timeUntil(future); got != "2h 30m" {
		t.Errorf("timeUntil(+2h30m) = %q, want '2h 30m'", got)
	}

	// Future: 3d2h
	farFuture := now.Add(3*24*time.Hour + 2*time.Hour).Format(time.RFC3339)
	if got := timeUntil(farFuture); got != "3d 2h" {
		t.Errorf("timeUntil(+3d2h) = %q, want '3d 2h'", got)
	}

	// Past
	past := now.Add(-1 * time.Hour).Format(time.RFC3339)
	if got := timeUntil(past); got != "now" {
		t.Errorf("timeUntil(past) = %q, want 'now'", got)
	}

	// Invalid
	if got := timeUntil("bad"); got != "unknown" {
		t.Errorf("timeUntil(bad) = %q, want 'unknown'", got)
	}
}

// --- FormatStatusLine ---

func TestFormatStatusLine(t *testing.T) {
	now := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	setNowFunc(t, func() time.Time { return now })

	reset5h := now.Add(2 * time.Hour).Format(time.RFC3339)
	reset7d := now.Add(3 * 24 * time.Hour).Format(time.RFC3339)

	tests := []struct {
		name     string
		data     *usageData
		contains []string
		absent   []string
	}{
		{
			"low usage with resets",
			&usageData{
				FiveHour: &windowData{Utilization: 24, ResetsAt: &reset5h},
				SevenDay: &windowData{Utilization: 11, ResetsAt: &reset7d},
			},
			[]string{"◎", "5h:24%", "7d:11%", "↻"},
			[]string{"⚠️", "‼️", "🟢"},
		},
		{
			"5h warn",
			&usageData{
				FiveHour: &windowData{Utilization: 88},
				SevenDay: &windowData{Utilization: 30},
			},
			[]string{"◎", "⚠️5h:88%", "7d:30%"},
			[]string{"‼️", "🟢"},
		},
		{
			"5h urgent",
			&usageData{
				FiveHour: &windowData{Utilization: 96},
				SevenDay: &windowData{Utilization: 30},
			},
			[]string{"◎", "‼️5h:96%", "7d:30%"},
			[]string{"🟢"},
		},
		{
			"7d urgent",
			&usageData{
				FiveHour: &windowData{Utilization: 10},
				SevenDay: &windowData{Utilization: 97},
			},
			[]string{"◎", "5h:10%", "‼️7d:97%"},
			[]string{"⚠️", "🟢"},
		},
		{
			"both warn",
			&usageData{
				FiveHour: &windowData{Utilization: 90},
				SevenDay: &windowData{Utilization: 87},
			},
			[]string{"⚠️5h:90%", "⚠️7d:87%"},
			[]string{"‼️", "🟢"},
		},
		{
			"nil windows",
			&usageData{},
			[]string{"◎", "5h:—", "7d:—"},
			[]string{"⚠️", "‼️", "🟢"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := formatStatusLine(tt.data)
			for _, want := range tt.contains {
				if !strings.Contains(s, want) {
					t.Errorf("formatStatusLine() = %q, should contain %q", s, want)
				}
			}
			for _, absent := range tt.absent {
				if strings.Contains(s, absent) {
					t.Errorf("formatStatusLine() = %q, should NOT contain %q", s, absent)
				}
			}
		})
	}
}

// --- WarnIcon ---

func TestWarnIcon(t *testing.T) {
	tests := []struct {
		pct  float64
		want string
	}{
		{0, ""},
		{84.9, ""},
		{85, "⚠️"},
		{94.9, "⚠️"},
		{95, "‼️"},
		{100, "‼️"},
	}
	for _, tt := range tests {
		got := warnIcon(tt.pct)
		if got != tt.want {
			t.Errorf("warnIcon(%.1f) = %q, want %q", tt.pct, got, tt.want)
		}
	}
}

// --- FormatDuration ---

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{2*time.Hour + 30*time.Minute, "2h 30m"},
		{30 * time.Minute, "30m"},
		{3*24*time.Hour + 2*time.Hour, "3d 2h"},
		{2 * 24 * time.Hour, "2d"},
		{5 * time.Minute, "5m"},
	}
	for _, tt := range tests {
		got := formatDuration(tt.d)
		if got != tt.want {
			t.Errorf("formatDuration(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

// --- ResetClock ---

func TestResetClock(t *testing.T) {
	now := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	setNowFunc(t, func() time.Time { return now })

	// Within 24h — should have AM/PM but no day name
	soon := now.Add(3 * time.Hour).Format(time.RFC3339)
	got := resetClock(soon)
	if got == "" {
		t.Error("resetClock() should not be empty for valid time")
	}
	if !strings.Contains(got, "AM") && !strings.Contains(got, "PM") {
		t.Errorf("resetClock(<24h) = %q, should contain AM or PM", got)
	}

	// Beyond 24h — should include day name
	far := now.Add(3 * 24 * time.Hour)
	farStr := far.Format(time.RFC3339)
	got = resetClock(farStr)
	expectedDay := far.Local().Format("Mon")
	if !strings.Contains(got, expectedDay) {
		t.Errorf("resetClock(>24h) = %q, should contain %q", got, expectedDay)
	}

	// Invalid
	if got := resetClock("bad"); got != "" {
		t.Errorf("resetClock(bad) = %q, want empty", got)
	}
}

// --- FormatWindowStatus ---

func TestFormatWindowStatus(t *testing.T) {
	now := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	setNowFunc(t, func() time.Time { return now })

	reset := now.Add(2 * time.Hour).Format(time.RFC3339)

	// Normal with reset
	got := formatWindowStatus("5h", &windowData{Utilization: 30, ResetsAt: &reset})
	if !strings.Contains(got, "5h:30%") || !strings.Contains(got, "↻") {
		t.Errorf("formatWindowStatus() = %q, want 5h:30%% with ↻", got)
	}
	if strings.Contains(got, "🟡") || strings.Contains(got, "🔴") {
		t.Errorf("normal usage should have no icon, got %q", got)
	}
	if !strings.Contains(got, "(") {
		t.Errorf("should contain clock time in parens, got %q", got)
	}

	// Warn without reset
	got = formatWindowStatus("7d", &windowData{Utilization: 88})
	if !strings.Contains(got, "⚠️7d:88%") {
		t.Errorf("formatWindowStatus() = %q, want ⚠️7d:88%%", got)
	}
	if strings.Contains(got, "↻") {
		t.Errorf("no reset time should have no ↻, got %q", got)
	}

	// Nil window
	got = formatWindowStatus("5h", nil)
	if got != "5h:—" {
		t.Errorf("nil window = %q, want '5h:—'", got)
	}
}

// --- FormatWindowSection ---

func TestFormatWindowSection(t *testing.T) {
	resetAt := time.Now().Add(3 * time.Hour).Format(time.RFC3339)
	w := &windowData{Utilization: 50, ResetsAt: &resetAt}
	lines := formatWindowSection("5-Hour", w)

	if len(lines) < 3 {
		t.Fatalf("expected at least 3 lines, got %d", len(lines))
	}
	if !strings.Contains(lines[0], "5-Hour") {
		t.Errorf("header should contain window name, got %q", lines[0])
	}
	if !strings.Contains(lines[0], "50.0%") {
		t.Errorf("header should contain percentage, got %q", lines[0])
	}
	if !strings.Contains(lines[1], "▰") {
		t.Errorf("bar line should contain filled blocks, got %q", lines[1])
	}
	if !strings.Contains(lines[2], "Resets in") {
		t.Errorf("reset line should contain 'Resets in', got %q", lines[2])
	}
}

func TestFormatWindowSectionNil(t *testing.T) {
	lines := formatWindowSection("Test", nil)
	if len(lines) != 1 {
		t.Fatalf("expected 1 line for nil window, got %d", len(lines))
	}
	if !strings.Contains(lines[0], "n/a") {
		t.Errorf("nil window should show 'n/a', got %q", lines[0])
	}
}

// --- FormatFull ---

func TestFormatFull(t *testing.T) {
	data := &usageData{
		FiveHour: &windowData{Utilization: 24},
		SevenDay: &windowData{Utilization: 11},
	}
	output := formatFull(data)
	for _, want := range []string{"5-Hour", "7-Day", "24.0%", "11.0%"} {
		if !strings.Contains(output, want) {
			t.Errorf("full output should contain %q", want)
		}
	}
}

func TestFormatFullAllNil(t *testing.T) {
	data := &usageData{}
	output := formatFull(data)
	if !strings.Contains(output, "n/a") {
		t.Errorf("should show n/a for nil windows")
	}
}

// --- JSON Parsing ---

func TestUsageDataJSONParsing(t *testing.T) {
	raw := `{
		"five_hour": {"utilization": 24, "resets_at": "2025-01-01T12:00:00Z"},
		"seven_day": {"utilization": 11}
	}`
	var data usageData
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if data.FiveHour == nil || data.FiveHour.Utilization != 24 {
		t.Error("FiveHour parsing failed")
	}
	if data.SevenDay == nil || data.SevenDay.Utilization != 11 {
		t.Error("SevenDay parsing failed")
	}
}
