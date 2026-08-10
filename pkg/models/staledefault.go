package models

import (
	"fmt"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/config"
)

// A settings `defaultModel` pin is the lowest-visibility way to choose a
// model: it is written once, lives in a file nobody reads again, and silently
// beats the provider's own current default forever. That is exactly how a host
// sat on claude-opus-4-6 long after the built-in default moved to
// claude-opus-5.
//
// This detects that one situation and says so. It NEVER rewrites the pin — an
// explicit pin is a deliberate act and the operator may well be pinning for
// cost or behaviour reasons.
//
// The bar for warning is deliberately high, because a wrong warning here
// trains the operator to ignore the check, which is worse than no check at
// all. We warn only when the provider ITSELF has moved on to a strictly newer
// generation of the SAME product line. Everything else is silence: an unknown
// model (probably a valid custom models.json entry), a different product line
// (opus→sonnet is a deliberate cost choice), an equal or newer pin, or any id
// pair we cannot order with confidence.

// StaleDefaultPin describes a settings defaultModel pin that its provider has
// since superseded.
type StaleDefaultPin struct {
	Provider string // provider the pin applies to
	Pinned   string // the pinned model id
	Current  string // the provider's current default model id
	Scope    string // config.ScopeProject | config.ScopeGlobal
	Path     string // settings file that carries the pin ("" if not file-backed)
}

// Summary is the one-line form.
func (p *StaleDefaultPin) Summary() string {
	return fmt.Sprintf("defaultModel is pinned to %s/%s, shadowing the newer %s/%s",
		p.Provider, p.Pinned, p.Provider, p.Current)
}

// Remediation names the file and the exact edit. A warning the operator cannot
// act on immediately is noise.
func (p *StaleDefaultPin) Remediation() string {
	where := p.Path
	if where == "" {
		where = p.Scope + " settings.json"
	}
	return fmt.Sprintf(
		"Edit %s: set \"defaultModel\": %q to follow the provider default, or remove the "+
			"\"defaultModel\"/\"defaultProvider\" keys to always track it. Keep the pin if it is deliberate.",
		where, p.Current)
}

// StaleDefaultPinInput is everything the check needs, as plain data, so the
// decision can be tested without a registry or a settings file.
type StaleDefaultPinInput struct {
	// Provider and Pinned are the effective settings pin. Either being empty
	// means "no pin".
	Provider string
	Pinned   string
	// Current is the provider's current default model id (overlay-aware).
	Current string
	// Scope and Path describe where the pin is written.
	Scope string
	Path  string
	// Known reports whether fir knows a model id for this provider. Required;
	// a nil Known means nothing is known and the check stays silent.
	Known func(provider, modelID string) bool
}

// CheckStaleDefaultPin returns a warning only when a pin is demonstrably an
// older generation of the provider's current default, and nil in every other
// case. It is pure — see StaleDefaultPinInput.
func CheckStaleDefaultPin(in StaleDefaultPinInput) *StaleDefaultPin {
	// 1. No pin at all, or half a pin (one key without the other): nothing to
	//    shadow, so nothing to say.
	if in.Provider == "" || in.Pinned == "" || in.Current == "" {
		return nil
	}
	// 2. The pin already IS the provider default.
	if in.Pinned == in.Current {
		return nil
	}
	// 3. An id fir does not know is a different problem — very likely a
	//    perfectly valid custom model from models.json. Both sides must
	//    resolve before we compare them.
	if in.Known == nil || !in.Known(in.Provider, in.Pinned) || !in.Known(in.Provider, in.Current) {
		return nil
	}
	// 4. Same product line only. claude-opus vs claude-sonnet is a deliberate
	//    choice, not staleness.
	lineage := ai.ExtractLineage(in.Pinned)
	if lineage == "" || lineage != ai.ExtractLineage(in.Current) {
		return nil
	}
	// 5. Both ids must carry a comparable generation. kimi-k2-thinking,
	//    deepseek-r1 and friends have none, and guessing is how false
	//    warnings get made.
	pinnedGen, ok := ai.GenerationVector(in.Pinned)
	if !ok {
		return nil
	}
	currentGen, ok := ai.GenerationVector(in.Current)
	if !ok {
		return nil
	}
	// 6. Strictly older. An equal vector means the same generation under a
	//    different spelling (dated snapshot, -preview) and a newer pin means
	//    the operator is ahead of the default — both silent.
	if ai.CompareGenerations(pinnedGen, currentGen) >= 0 {
		return nil
	}
	return &StaleDefaultPin{
		Provider: in.Provider,
		Pinned:   in.Pinned,
		Current:  in.Current,
		Scope:    in.Scope,
		Path:     in.Path,
	}
}

// StaleDefaultPinFor wires the pure check to the live settings and registry:
// the effective pin (project file over global file) versus the provider's
// current default, which the catalog overlay may have moved without a release.
func StaleDefaultPinFor(sm *config.SettingsManager, reg *ModelRegistry) *StaleDefaultPin {
	if sm == nil || reg == nil {
		return nil
	}
	provider, modelID, scope, path := sm.DefaultModelPin()
	if provider == "" || modelID == "" {
		return nil
	}
	return CheckStaleDefaultPin(StaleDefaultPinInput{
		Provider: provider,
		Pinned:   modelID,
		Current:  reg.DefaultModelForProvider(ai.Provider(provider)),
		Scope:    scope,
		Path:     path,
		Known:    func(p, id string) bool { return reg.Find(p, id) != nil },
	})
}
