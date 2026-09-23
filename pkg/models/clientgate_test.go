package models

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const realGateBody = "Claude Code 2.1.112 does not support this model; version 2.1.251 or newer is required. Run 'claude update', or update the Claude desktop app, then try again."

func TestClassifyClientVersionGate(t *testing.T) {
	tests := []struct {
		name     string
		msg      string
		adv, req string // empty => must NOT classify
	}{
		// Positives: the observed vendor signature, through both of fir's
		// error framings.
		{"http path", "400 " + realGateBody + " (request-id: req_011CedTnPJWKevjCDU62kN63)", "2.1.112", "2.1.251"},
		{"sse event path", realGateBody + " (invalid_request_error)", "2.1.112", "2.1.251"},
		{"bare vendor text", realGateBody, "2.1.112", "2.1.251"},
		{"four components", "Claude Code 2.1.280.1 does not support this model; version 2.2 or newer is required.", "2.1.280.1", "2.2"},

		// Negatives: ordinary failures must never be called a gate.
		{"empty", "", "", ""},
		{"plain 400", "400 messages: text content blocks must be non-empty (request-id: req_1)", "", ""},
		{"context overflow 400", "400 prompt is too long: 250000 tokens > 200000 maximum (invalid_request_error)", "", ""},
		{"invalid model 400", "400 model: claude-foo-9 is not a valid model (invalid_request_error)", "", ""},
		{"401 revoked", "OAuth access token has been revoked (authentication_error)", "", ""},
		{"401 http", "401 invalid x-api-key (request-id: req_2)", "", ""},
		{"403 permission", "403 Your credit balance is too low to access the Anthropic API (permission_error)", "", ""},
		{"429 quota", "429 This request would exceed your account's rate limit. Please try again later. (rate_limit_error)", "", ""},
		{"529 overloaded", "Overloaded (overloaded_error)", "", ""},
		{"network", "Post \"https://api.anthropic.com/v1/messages\": dial tcp: lookup api.anthropic.com: no such host", "", ""},
		{"truncated stream", "Anthropic stream ended before message_stop", "", ""},
		{"claude update alone", "400 please run 'claude update' (invalid_request_error)", "", ""},
		{"version word alone", "400 anthropic-version header is required (invalid_request_error)", "", ""},
		{"required not newer", "Claude Code 2.1.300 does not support this model; version 2.1.251 or newer is required.", "", ""},
		{"required equal", "Claude Code 2.1.251 does not support this model; version 2.1.251 or newer is required.", "", ""},
		{"non-numeric version", "Claude Code 2.1.x does not support this model; version 2.2 or newer is required.", "", ""},
		{"reworded", "Claude Code 2.1.112 is not supported for this model; please upgrade to 2.1.251", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hit := ClassifyClientVersionGate(tt.msg)
			if tt.adv == "" {
				if hit != nil {
					t.Fatalf("false positive: %+v", hit)
				}
				return
			}
			if hit == nil || hit.Advertised != tt.adv || hit.Required != tt.req {
				t.Fatalf("got %+v, want %s/%s", hit, tt.adv, tt.req)
			}
		})
	}
}

func TestGateResolved(t *testing.T) {
	rec := GateRecord{Pin: "2.1.280"}
	for _, tt := range []struct {
		eff  string
		want bool
	}{
		{"2.1.280", false}, // same pin: still warns
		{"2.1.279", false},
		{"", false},
		{"2.1.281", true}, // strictly past: silent
		{"2.2", true},
		{"2.1.280.1", true},
	} {
		if got := GateResolved(rec, tt.eff); got != tt.want {
			t.Errorf("GateResolved(pin 2.1.280, eff %q) = %v, want %v", tt.eff, got, tt.want)
		}
	}
}

func TestGateRecordRoundTripAndAggregate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "doctor.jsonl")
	if recs, err := ReadGateRecords(path); err != nil || recs != nil {
		t.Fatalf("missing file: %v %v", recs, err)
	}
	t0 := float64(time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC).Unix())
	in := []GateRecord{
		{Key: "claudeCode", Pin: "2.1.257", PinSource: PinSourceEmbeddedFloor, Required: "2.1.270", Model: "m1", Host: "a", Timestamp: t0},
		{Key: "claudeCode", Pin: "2.1.280", PinSource: PinSourceOverlay, Required: "2.1.290", Model: "m2", Host: "b", Timestamp: t0 + 3600, VendorError: realGateBody},
		{Key: "claudeCode", Pin: "2.1.280", Required: "2.1.285", Model: "m2", Host: "b", Timestamp: t0 + 60},
	}
	for _, r := range in {
		if err := AppendGateRecord(path, r); err != nil {
			t.Fatal(err)
		}
	}
	// Foreign and malformed lines are skipped, not fatal.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString(`{"type":"session_failure","tool_errors":[]}` + "\n" + `{"type":"client-version-gate",` + "\n" + `{"type":"client-version-gate"}` + "\n" + `{"type":"client-version-gate","pin":"1.0"}` + "\n")
	_ = f.Close()

	got, err := ReadGateRecords(path)
	if err != nil || len(got) != 3 {
		t.Fatalf("read %d records, err %v", len(got), err)
	}
	if got[1].Type != GateRecordType || got[1].PinSource != PinSourceOverlay || got[1].VendorError != realGateBody || got[1].Host != "b" {
		t.Fatalf("round trip lost fields: %+v", got[1])
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `"type":"client-version-gate"`) {
		t.Fatal("doctor_query pattern=client-version-gate would not find the record")
	}

	// Effective 2.1.280: the 2.1.257 record is resolved, both 2.1.280 remain.
	u := UnresolvedGates(got, func(string) string { return "2.1.280" })
	if len(u) != 1 {
		t.Fatalf("want 1 aggregate, got %+v", u)
	}
	g := u[0]
	if g.Count != 2 || g.Pin != "2.1.280" || g.Required != "2.1.290" || g.First.Unix() != int64(t0+60) || g.Last.Unix() != int64(t0+3600) {
		t.Fatalf("bad aggregate %+v", g)
	}
	if len(g.Models) != 1 || len(g.Hosts) != 1 {
		t.Fatalf("dedup failed: %+v", g)
	}
	if s := g.Summary(); strings.Contains(s, "\n") || !strings.Contains(s, "2.1.280") || !strings.Contains(s, "2026-09-20") {
		t.Fatalf("summary: %q", s)
	}
	if l := g.ReportLine(); !strings.HasPrefix(l, "client-version-gate key=claudeCode pin=2.1.280 required=2.1.290") || !strings.Contains(l, "hosts=b") {
		t.Fatalf("report line: %q", l)
	}
	// Bump past the pin: everything self-clears.
	if u := UnresolvedGates(got, func(string) string { return "2.1.281" }); len(u) != 0 {
		t.Fatalf("expected silent, got %+v", u)
	}
}

func TestReadGateRecordsUnreadable(t *testing.T) {
	if _, err := ReadGateRecords(t.TempDir()); err == nil {
		t.Fatal("reading a directory should fail")
	}
}

func TestGateErrorMessage(t *testing.T) {
	for src, phrase := range map[string]string{PinSourceOverlay: "catalog overlay", PinSourceEmbeddedFloor: "embedded floor"} {
		m := GateErrorMessage(GateRecord{Key: "claudeCode", Pin: "2.1.280", PinSource: src, Required: "2.1.300", VendorError: "raw"})
		for _, want := range []string{"2.1.280", phrase, "2.1.300", "doctor_query pattern=client-version-gate", "clientVersions.claudeCode", "raw"} {
			if !strings.Contains(m, want) {
				t.Errorf("%s: %q missing %q", src, m, want)
			}
		}
		if !IsGateErrorMessage(m) || IsGateErrorMessage("raw") {
			t.Error("IsGateErrorMessage")
		}
	}
}

func TestClientVersionSourceAndLocal(t *testing.T) {
	if clientVersionSource("", "2.1.0") != PinSourceEmbeddedFloor ||
		clientVersionSource("2.0", "2.1.0") != PinSourceEmbeddedFloor ||
		clientVersionSource("2.1.0", "2.1.0") != PinSourceEmbeddedFloor ||
		clientVersionSource("2.9", "2.1.0") != PinSourceOverlay {
		t.Fatal("clientVersionSource")
	}
	floor := DefaultClientVersions().Get(ClientVersionKeyClaudeCode)
	dir := t.TempDir()
	if got := LocalClientVersion(dir, ClientVersionKeyClaudeCode); got != floor {
		t.Fatalf("no cache: got %q want floor %q", got, floor)
	}
	_ = os.MkdirAll(filepath.Join(dir, "cache"), 0o755)
	doc := `{"schemaVersion":1,"generatedAt":"2999-01-01T00:00:00Z","providers":{},"clientVersions":{"claudeCode":"99.0.0"}}`
	if err := os.WriteFile(filepath.Join(dir, "cache", catalogFileName), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := LocalClientVersion(dir, ClientVersionKeyClaudeCode); got != "99.0.0" {
		t.Fatalf("cached overlay: got %q", got)
	}
}
