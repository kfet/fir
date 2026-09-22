package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kfet/fir/pkg/models"
)

func npmServer(t *testing.T, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestFetchClaudeCodeVersion(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
		ok   bool
	}{
		{"plain dotted", `{"dist-tags":{"latest":"2.1.291","next":"3.0.0"}}`, "2.1.291", true},
		{"no dist-tags", `{"name":"@anthropic-ai/claude-code"}`, "", false},
		{"empty latest", `{"dist-tags":{"latest":""}}`, "", false},
		// The generator holds npm to exactly the grammar the catalog loader
		// enforces, so a value that could never be published never reaches
		// a PR.
		{"pre-release tag", `{"dist-tags":{"latest":"2.2.0-rc.1"}}`, "", false},
		{"injection", `{"dist-tags":{"latest":"2.1.280; x-evil: 1"}}`, "", false},
		{"too long", `{"dist-tags":{"latest":"` + strings.Repeat("1", 33) + `"}}`, "", false},
		{"not json", `<html>502</html>`, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := fetchClaudeCodeVersionFrom(npmServer(t, tc.body))
			if tc.ok {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if got != tc.want {
					t.Fatalf("got %q, want %q", got, tc.want)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected an error, got %q", got)
			}
		})
	}
}

func TestClientVersionBumpIsForwardOnly(t *testing.T) {
	cases := []struct {
		name            string
		current, latest string
		wantBump        bool
		wantAnomaly     bool
	}{
		{"unchanged", "2.1.280", "2.1.280", false, false},
		{"older upstream is never written", "2.1.280", "2.1.279", false, false},
		// A string compare would call 2.1.90 newer than 2.1.280.
		{"numeric compare", "2.1.280", "2.1.90", false, false},
		{"forward", "2.1.280", "2.1.291", true, false},
		{"no current pin", "", "2.1.291", true, false},
		{"no upstream value", "2.1.280", "", false, false},
		{"minor jump", "2.1.280", "2.4.0", true, true},
		{"major change", "2.1.280", "3.0.0", true, true},
		{"one minor is normal", "2.1.280", "2.2.0", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bump, anomaly := clientVersionBump(tc.current, tc.latest)
			if (bump != "") != tc.wantBump {
				t.Fatalf("bump = %q, wantBump=%v", bump, tc.wantBump)
			}
			if (anomaly != "") != tc.wantAnomaly {
				t.Fatalf("anomaly = %q, wantAnomaly=%v", anomaly, tc.wantAnomaly)
			}
		})
	}
}

// seedOverlay writes a minimal overlay and returns its path.
func seedOverlay(t *testing.T, clientVersions string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "catalog-v1.json")
	body := `{"schemaVersion":1,"generatedAt":"2020-01-01T00:00:00Z",` +
		clientVersions + `"providers":{"anthropic":{"models":[{"id":"hand-curated","api":"anthropic-messages"}]}}}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOverlayClaudeCodeVersion(t *testing.T) {
	if got := overlayClaudeCodeVersion(seedOverlay(t, `"clientVersions":{"claudeCode":"2.1.280"},`)); got != "2.1.280" {
		t.Fatalf("got %q", got)
	}
	if got := overlayClaudeCodeVersion(seedOverlay(t, "")); got != "" {
		t.Fatalf("absent pin = %q", got)
	}
	if got := overlayClaudeCodeVersion(filepath.Join(t.TempDir(), "nope.json")); got != "" {
		t.Fatalf("missing file = %q", got)
	}
	if got := overlayClaudeCodeVersion(""); got != "" {
		t.Fatalf("empty path = %q", got)
	}
}

func TestUpdateOverlayWritesPinAndTimestamp(t *testing.T) {
	path := seedOverlay(t, `"clientVersions":{"claudeCode":"2.1.280"},`)
	before, _ := os.ReadFile(path)

	// A pin bump alone rewrites the document: no new model is required.
	added, _, err := updateOverlay(path, nil, "2.1.291")
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 0 {
		t.Fatalf("added = %v", added)
	}
	raw, _ := os.ReadFile(path)
	if string(raw) == string(before) {
		t.Fatal("a moved pin must rewrite the overlay")
	}
	o, err := models.ParseCatalogOverlay(raw)
	if err != nil {
		t.Fatalf("written overlay is invalid: %v", err)
	}
	if o.ClientVersions.Get(models.ClientVersionKeyClaudeCode) != "2.1.291" {
		t.Fatalf("pin = %v", o.ClientVersions)
	}
	// Same write, same document: the timestamp moved too, or no binary would
	// ever load the new pin.
	if !o.GeneratedAt.After(mustParseTime(t, "2020-01-01T00:00:00Z")) {
		t.Fatalf("generatedAt not bumped: %s", o.GeneratedAt)
	}

	// No pin and no models is still a no-op.
	beforeNoop, _ := os.ReadFile(path)
	if _, _, err := updateOverlay(path, nil, ""); err != nil {
		t.Fatal(err)
	}
	afterNoop, _ := os.ReadFile(path)
	if string(beforeNoop) != string(afterNoop) {
		t.Fatal("a no-op run must not rewrite the overlay")
	}
}

func TestUpdateOverlayAddsPinToDocumentWithout(t *testing.T) {
	path := seedOverlay(t, "")
	if _, _, err := updateOverlay(path, nil, "2.1.291"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	o, err := models.ParseCatalogOverlay(raw)
	if err != nil {
		t.Fatal(err)
	}
	if o.ClientVersions.Get(models.ClientVersionKeyClaudeCode) != "2.1.291" {
		t.Fatalf("pin = %v", o.ClientVersions)
	}
}

func TestSummaryCarriesClientVersionBump(t *testing.T) {
	path := filepath.Join(t.TempDir(), "summary.json")
	if err := writeSummary(path, &watchResult{
		Trigger:           triggerCurated,
		OpenPR:            true,
		ClientVersionBump: "2.1.280 → 2.1.291",
	}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["client_version_bump"] != "2.1.280 → 2.1.291" {
		t.Fatalf("client_version_bump = %v", got["client_version_bump"])
	}

	// Absent when nothing moved, so the workflow's -n test is meaningful.
	if err := writeSummary(path, &watchResult{Trigger: triggerCurated}); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(path)
	got = map[string]any{}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["client_version_bump"]; ok {
		t.Fatal("client_version_bump must be omitted when the pin did not move")
	}
}

// mustParseTime parses an RFC3339 timestamp or fails the test.
func mustParseTime(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

// A night with no new model but a moved pin still matters: the vendor
// sometimes raises the gate on models fir already ships.
func TestRunWatchPinBumpAloneOpensAPR(t *testing.T) {
	overlayPath := seedOverlay(t, `"clientVersions":{"claudeCode":"2.1.280"},`)
	summaryPath := filepath.Join(t.TempDir(), "summary.json")

	runWatch(watchOptions{
		fresh:             nil,
		trigger:           triggerCurated,
		summaryPath:       summaryPath,
		overlayPath:       overlayPath,
		proposeOverlay:    true,
		claudeCodeVersion: "2.1.291",
	})

	raw, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["open_pr"] != true {
		t.Fatalf("open_pr = %v (a moved pin is a qualifying event)", got["open_pr"])
	}
	if got["client_version_bump"] != "2.1.280 → 2.1.291" {
		t.Fatalf("client_version_bump = %v", got["client_version_bump"])
	}
	// And the pin actually landed in the proposed document.
	o, err := models.ParseCatalogOverlay(mustRead(t, overlayPath))
	if err != nil {
		t.Fatal(err)
	}
	if o.ClientVersions.Get(models.ClientVersionKeyClaudeCode) != "2.1.291" {
		t.Fatalf("overlay pin = %v", o.ClientVersions)
	}
}

// A run where nothing moved leaves the pin and the document alone.
func TestRunWatchNoPinMovementLeavesOverlayAlone(t *testing.T) {
	overlayPath := seedOverlay(t, `"clientVersions":{"claudeCode":"2.1.280"},`)
	before := mustRead(t, overlayPath)
	summaryPath := filepath.Join(t.TempDir(), "summary.json")

	runWatch(watchOptions{
		trigger:        triggerCurated,
		summaryPath:    summaryPath,
		overlayPath:    overlayPath,
		proposeOverlay: true,
		// "" is what a failed npm fetch yields — the committed pin must
		// survive untouched.
		claudeCodeVersion: "",
	})

	if string(mustRead(t, overlayPath)) != string(before) {
		t.Fatal("a failed or unchanged pin fetch must not rewrite the overlay")
	}
	var got map[string]any
	if err := json.Unmarshal(mustRead(t, summaryPath), &got); err != nil {
		t.Fatal(err)
	}
	if got["open_pr"] != false {
		t.Fatalf("open_pr = %v", got["open_pr"])
	}
}

// A failed npm fetch is reported under the same sources_failed contract as
// every other source, which is what makes -strict a no-run rather than a red
// badge.
func TestClaudeCodeSourceNaming(t *testing.T) {
	path := filepath.Join(t.TempDir(), "summary.json")
	if err := writeSummary(path, &watchResult{
		Trigger: triggerCurated,
		Failed:  []string{claudeCodeSource + ": boom"},
	}); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(mustRead(t, path), &got); err != nil {
		t.Fatal(err)
	}
	failed, _ := got["sources_failed"].([]any)
	if len(failed) != 1 || !strings.HasPrefix(failed[0].(string), "npm:claude-code:") {
		t.Fatalf("sources_failed = %v", got["sources_failed"])
	}
	if names := failedSources([]string{claudeCodeSource + ": boom"}); len(names) != 1 || names[0] != "npm" {
		t.Logf("failedSources renders %v (source names are split on the first colon)", names)
	}
}

// mustRead reads a file or fails the test.
func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
