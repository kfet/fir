package session

import (
	"os"
	"time"

	"github.com/kfet/fir/pkg/ai"
	firlog "github.com/kfet/fir/pkg/log"
	"github.com/kfet/fir/pkg/models"
)

// anthropicOAuthHeader marks a model as served through the anthropic-auth
// (Claude Pro/Max) OAuth path — the only path that advertises claude-cli/<pin>
// and therefore the only one a client-version gate can reject. Same marker
// the anthropic provider keys on (isOAuthModel).
const anthropicOAuthHeader = "x-anthropic-oauth-beta-prefix"

// classifyClientVersionGate rewrites a gate rejection on an OAuth-mode
// Anthropic assistant error into the explicit, actionable message and records
// it in the doctor log. It runs before the message_end event is emitted, so
// every mode displays and persists the rewritten text. No network: every
// field recorded is known locally or quoted from the vendor error.
func (s *AgentSession) classifyClientVersionGate(msg *ai.AssistantMessage) {
	if msg == nil || msg.StopReason != ai.StopReasonError || s.modelRegistry == nil || models.IsGateErrorMessage(msg.ErrorMessage) {
		return
	}
	m := s.modelRegistry.Find(string(msg.Provider), msg.Model)
	if m == nil || m.Headers[anthropicOAuthHeader] == "" {
		return
	}
	hit := models.ClassifyClientVersionGate(msg.ErrorMessage)
	if hit == nil {
		return
	}
	key := models.ClientVersionKeyClaudeCode
	effective := s.modelRegistry.ClientVersion(key)
	host, _ := os.Hostname()
	rec := models.GateRecord{
		Type:         models.GateRecordType,
		Key:          key,
		Pin:          hit.Advertised,
		PinSource:    s.modelRegistry.ClientVersionSource(key),
		EffectivePin: effective,
		PinMismatch:  hit.Advertised != effective,
		Required:     hit.Required,
		Provider:     string(msg.Provider),
		Model:        msg.Model,
		VendorError:  msg.ErrorMessage,
		Host:         host,
		Timestamp:    float64(time.Now().UnixNano()) / float64(time.Second),
	}
	msg.ErrorMessage = models.GateErrorMessage(rec)

	// One record per (pin, required, model) per session: a stuck loop
	// retrying a gated model must not turn the doctor log into the loop.
	dedup := rec.Pin + "|" + rec.Required + "|" + rec.Provider + "/" + rec.Model
	if s.doctorLogPath == "" {
		return
	}
	if _, seen := s.gateRecorded.LoadOrStore(dedup, struct{}{}); seen {
		return
	}
	if err := models.AppendGateRecord(s.doctorLogPath, rec); err != nil {
		firlog.Warn("doctor: cannot record client-version-gate: %v", err)
	}
}

// clientVersionGateDiagnostics reports unresolved gate records as
// diagnostics — surfaced at session start by the doctor extension and by
// doctor_summary. They clear themselves once the effective pin moves past
// the rejected one (models.GateResolved).
func (s *AgentSession) clientVersionGateDiagnostics() []Diagnostic {
	if s.modelRegistry == nil || s.doctorLogPath == "" {
		return nil
	}
	recs, err := models.ReadGateRecords(s.doctorLogPath)
	if err != nil {
		firlog.Debug("doctor: reading gate records: %v", err)
	}
	var out []Diagnostic
	for _, g := range models.UnresolvedGates(recs, s.modelRegistry.ClientVersion) {
		out = append(out, Diagnostic{
			Code:        "client_version_gate",
			Severity:    "warning",
			Summary:     g.Summary(),
			Remediation: "publish clientVersions." + g.Key + " ≥ " + g.Required + " in the catalog overlay (`fir update` only helps if a newer release already carries it); see doctor_query pattern=" + models.GateRecordType,
		})
	}
	return out
}
