package models

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Client-version gate detection.
//
// Anthropic gates new models on the claude-cli/<version> an OAuth request
// advertises (see CatalogClientVersions). A gate rejection used to surface as
// an opaque provider error that only a human could recognise. This file turns
// it into a named, recorded, self-clearing condition:
//
//  1. ClassifyClientVersionGate recognises the rejection (pure, no I/O).
//  2. AppendGateRecord writes a `client-version-gate` record to the doctor
//     log (~/.config/fir/doctor.jsonl) — where fir already keeps failures.
//  3. UnresolvedGates decides, against the effective pin, which records still
//     matter. It is the ONLY place that decides "resolved", so the session
//     warning, doctor diagnostics and `fir doctor client-version-gates`
//     (fleet converge) cannot disagree.

// GateRecordType is the doctor.jsonl record kind; `doctor_query
// pattern=client-version-gate` finds these records by it.
const GateRecordType = "client-version-gate"

// clientVersionGateRE is the vendor signature, matched verbatim.
//
// The only rejection ever observed (debug.log, 2026-09, req_011CedTnPJW…):
//
//	HTTP 400 {"type":"error","error":{"type":"invalid_request_error",
//	  "message":"Claude Code 2.1.112 does not support this model; version
//	  2.1.251 or newer is required. Run 'claude update', or update the
//	  Claude desktop app, then try again."}}
//
// Why this and nothing looser: the sentence names BOTH the advertised version
// and the required one, which no other Anthropic error does, so it cannot be
// confused with an ordinary 400 (bad request, context overflow), 401/403
// (auth), 429/529 (quota, overload) or a transport error. We deliberately do
// NOT match on status code, error type, "claude update" or "version" alone:
// a false positive teaches people to ignore the warning, whereas a missed
// reworded message merely degrades to today's generic error. We also do not
// anchor on the "400 " / "(invalid_request_error)" framing that fir's two
// error paths (HTTP vs in-stream SSE) add, since that framing is ours, not
// the vendor's, and varies. Versions are constrained to ClientVersionRE's
// grammar, and ClassifyClientVersionGate additionally requires required >
// advertised — a gate that "requires" an older version is not a gate.
var clientVersionGateRE = regexp.MustCompile(
	`Claude Code ([0-9]+(?:\.[0-9]+){0,3}) does not support this model; version ([0-9]+(?:\.[0-9]+){0,3}) or newer is required`)

// ClientVersionGateHit is a recognised gate rejection.
type ClientVersionGateHit struct {
	Advertised string // the version the vendor says we sent
	Required   string // the minimum the vendor says it needs
}

// ClassifyClientVersionGate returns a hit iff errMsg carries the vendor's
// client-version gate signature (see clientVersionGateRE). The caller is
// responsible for only asking about OAuth-mode Anthropic requests.
func ClassifyClientVersionGate(errMsg string) *ClientVersionGateHit {
	m := clientVersionGateRE.FindStringSubmatch(errMsg)
	if m == nil || CompareClientVersions(m[2], m[1]) <= 0 {
		return nil
	}
	return &ClientVersionGateHit{Advertised: m[1], Required: m[2]}
}

// GateRecord is one `client-version-gate` doctor.jsonl record.
type GateRecord struct {
	Type         string  `json:"type"` // always GateRecordType
	Key          string  `json:"key"`  // client version key, e.g. "claudeCode"
	Pin          string  `json:"pin"`  // version the vendor rejected
	PinSource    string  `json:"pinSource"`
	EffectivePin string  `json:"effectivePin"`          // local ClientVersion() at record time
	PinMismatch  bool    `json:"pinMismatch,omitempty"` // Pin != EffectivePin: headers lag the registry
	Required     string  `json:"required"`
	Provider     string  `json:"provider"`
	Model        string  `json:"model"`
	VendorError  string  `json:"vendorError"`
	Host         string  `json:"host"`
	Timestamp    float64 `json:"ts"` // unix seconds, like the other doctor records
}

// Time returns the record timestamp.
func (g GateRecord) Time() time.Time {
	return time.Unix(0, int64(g.Timestamp*float64(time.Second))).UTC()
}

// GateErrorMessage is the user-visible error that replaces the raw vendor
// text. It names the pin, its source and the remedy, and keeps the vendor
// text so nothing is lost.
func GateErrorMessage(rec GateRecord) string {
	return fmt.Sprintf(gateErrorPrefix+" (%s, from the %s) may be gated out — the vendor requires %s or newer. "+
		"Recorded in the doctor log (doctor_query pattern=%s); the fix is a clientVersions.%s bump in the catalog overlay (pkg/models/catalog-v1.json). Vendor error: %s",
		rec.Pin, pinSourcePhrase(rec.PinSource), rec.Required, GateRecordType, rec.Key, rec.VendorError)
}

// gateErrorPrefix starts every GateErrorMessage.
const gateErrorPrefix = "anthropic rejected the request; the advertised claude-cli version"

// IsGateErrorMessage reports whether msg was already rewritten by
// GateErrorMessage, so a second pass never double-wraps it.
func IsGateErrorMessage(msg string) bool { return strings.HasPrefix(msg, gateErrorPrefix) }

func pinSourcePhrase(src string) string {
	if src == PinSourceOverlay {
		return "catalog overlay"
	}
	return "embedded floor compiled into this binary"
}

// DoctorLogPath is the doctor log under a global agent dir (~/.config/fir).
func DoctorLogPath(agentDir string) string {
	return filepath.Join(agentDir, "doctor.jsonl")
}

// AppendGateRecord appends rec as one JSON line. O_APPEND keeps the line
// atomic with respect to the doctor extension's own writes to the same file.
func AppendGateRecord(path string, rec GateRecord) error {
	rec.Type = GateRecordType
	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, werr := f.Write(append(line, '\n'))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

// ReadGateRecords returns every client-version-gate record in the doctor log,
// oldest first. Other record kinds and malformed lines are skipped; a missing
// file is no records.
func ReadGateRecords(path string) ([]GateRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var out []GateRecord
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024) // session_failure lines can be large
	for sc.Scan() {
		line := sc.Bytes()
		if !strings.Contains(string(line), GateRecordType) {
			continue
		}
		var rec GateRecord
		if json.Unmarshal(line, &rec) != nil || rec.Type != GateRecordType || rec.Pin == "" || rec.Timestamp <= 0 {
			continue
		}
		out = append(out, rec)
	}
	return out, sc.Err()
}

// GateResolved is the self-clearing predicate: a record stops mattering once
// the effective pin has moved STRICTLY past the pin that was rejected. No
// "mark as done" step — bumping the pin is the acknowledgement. A PinMismatch
// record whose effective pin was already past Pin when it was written is
// therefore resolved at birth: the next request advertises the newer pin.
// The mismatch itself stays queryable via doctor_query.
func GateResolved(rec GateRecord, effective string) bool {
	return effective != "" && CompareClientVersions(effective, rec.Pin) > 0
}

// UnresolvedGate aggregates the unresolved records for one key.
type UnresolvedGate struct {
	Key       string
	Pin       string // highest rejected pin still not surpassed
	Required  string // highest required version seen
	Effective string
	Models    []string
	Hosts     []string
	Count     int
	First     time.Time
	Last      time.Time
}

// UnresolvedGates filters recs through GateResolved (using effective(key))
// and aggregates the survivors per key, in first-seen key order.
func UnresolvedGates(recs []GateRecord, effective func(key string) string) []UnresolvedGate {
	var out []UnresolvedGate
	idx := map[string]int{}
	for _, r := range recs {
		eff := effective(r.Key)
		if GateResolved(r, eff) {
			continue
		}
		i, ok := idx[r.Key]
		if !ok {
			idx[r.Key] = len(out)
			out = append(out, UnresolvedGate{Key: r.Key, Effective: eff, First: r.Time()})
			i = len(out) - 1
		}
		g := &out[i]
		g.Count++
		if g.Pin == "" || CompareClientVersions(r.Pin, g.Pin) > 0 {
			g.Pin = r.Pin
		}
		if g.Required == "" || CompareClientVersions(r.Required, g.Required) > 0 {
			g.Required = r.Required
		}
		g.Models = addUnique(g.Models, r.Model)
		g.Hosts = addUnique(g.Hosts, r.Host)
		if t := r.Time(); t.Before(g.First) {
			g.First = t
		}
		if t := r.Time(); !t.Before(g.Last) {
			g.Last = t
		}
	}
	return out
}

func addUnique(list []string, v string) []string {
	if v == "" {
		return list
	}
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}

// Summary is the one-line session-start warning.
func (g UnresolvedGate) Summary() string {
	return fmt.Sprintf("claude-cli %s was gated out by anthropic %d× since %s (needs ≥ %s; now advertising %s) — bump clientVersions.%s",
		g.Pin, g.Count, g.First.Format("2006-01-02 15:04Z"), g.Required, g.Effective, g.Key)
}

// ReportLine is one line of `fir doctor client-version-gates` output, for
// fleet aggregation.
func (g UnresolvedGate) ReportLine() string {
	return fmt.Sprintf("client-version-gate key=%s pin=%s required=%s effective=%s count=%d first=%s last=%s hosts=%s models=%s",
		g.Key, g.Pin, g.Required, g.Effective, g.Count,
		g.First.Format(time.RFC3339), g.Last.Format(time.RFC3339),
		strings.Join(g.Hosts, ","), strings.Join(g.Models, ","))
}

// LocalClientVersion is ModelRegistry.ClientVersion without a registry: the
// effective pin from the embedded floor and the cached overlay under
// agentDir. For CLI paths (`fir doctor`) that must not boot a session.
func LocalClientVersion(agentDir, key string) string {
	o, _ := bestLocalOverlay(filepath.Join(agentDir, "cache", catalogFileName))
	var cv *CatalogClientVersions
	if o != nil {
		cv = o.ClientVersions
	}
	return maxClientVersion(cv.Get(key), DefaultClientVersions().Get(key))
}
