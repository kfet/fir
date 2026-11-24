package main

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

// Google Antigravity (agy) is an extension-registered provider
// (pkg/resources/builtin_extensions/antigravity_auth.py) with no public model
// list and no credential CI can hold. Two public sources split the job:
//
//   - router-for-me/models lists the model IDs Antigravity actually serves
//     (key "antigravity"). It decides MEMBERSHIP only, plus a cap on limits:
//     Antigravity can serve a smaller window than the public API.
//   - models.dev supplies the numbers and capabilities of the underlying
//     google / anthropic model.
//
// Cost is always zero: Antigravity is subscription-billed, so per-token API
// prices would overstate what a session costs.
//
// New IDs land in the catalog overlay's extensionProviders section (never in
// the compiled catalog: the provider does not exist until its extension
// registers it). Nothing is ever removed, and additions are capped per run.

const (
	antigravityProvider  = "google-antigravity"
	antigravitySource    = "router-for-me/antigravity"
	antigravityModelsURL = "https://raw.githubusercontent.com/router-for-me/models/main/models.json"
	// antigravityAddCap bounds overlay additions per run. The rest are
	// reported as deferred and picked up by later runs.
	antigravityAddCap = 8
)

// extensionProviders are providers whose models live in the overlay's
// extensionProviders section rather than providers.
var extensionProviders = map[string]bool{antigravityProvider: true}

// routerModel is one entry of router-for-me's "antigravity" array.
type routerModel struct {
	ID                       string   `json:"id"`
	DisplayName              string   `json:"display_name"`
	ContextLength            int      `json:"context_length"`
	MaxCompletionTokens      int      `json:"max_completion_tokens"`
	SupportedInputModalities []string `json:"supportedInputModalities"`
	Thinking                 *struct {
		Levels []string `json:"levels"`
	} `json:"thinking"`
}

// fetchAntigravityModels fetches the Antigravity ID list.
func fetchAntigravityModels() ([]routerModel, error) {
	var doc map[string][]routerModel
	if err := fetchJSON(antigravityModelsURL, &doc); err != nil {
		return nil, err
	}
	return validateRouterModels(doc)
}

// validateRouterModels rejects a document whose shape changed: a schema break
// must read as a failed source, never as "Antigravity has no models".
func validateRouterModels(doc map[string][]routerModel) ([]routerModel, error) {
	list, ok := doc["antigravity"]
	if !ok || len(list) == 0 {
		return nil, fmt.Errorf("no %q models in document", "antigravity")
	}
	for i, m := range list {
		if strings.TrimSpace(m.ID) == "" {
			return nil, fmt.Errorf("antigravity[%d]: empty id", i)
		}
	}
	return list, nil
}

// agyTierSuffix is the Antigravity-specific tier/effort suffix on an ID.
var agyTierSuffix = regexp.MustCompile(`-(minimal|low|medium|high|xhigh|thinking|tiered)$`)

// antigravityBase maps an Antigravity ID to its models.dev provider and the
// candidate models.dev IDs of the underlying model, best first.
func antigravityBase(id string) (provider string, candidates []string) {
	base := agyTierSuffix.ReplaceAllString(id, "")
	switch {
	case strings.HasPrefix(base, "gemini-"):
		provider = "google"
	case strings.HasPrefix(base, "claude-"):
		provider = "anthropic"
		// Dotted versions (claude-opus-5.5) are written with dashes on
		// models.dev (claude-opus-5-5).
		base = strings.ReplaceAll(base, ".", "-")
	default:
		return "", nil
	}
	return provider, []string{base, base + "-preview"}
}

// minPositive returns the smaller positive value; 0 when neither is.
func minPositive(a, b int) int {
	switch {
	case a <= 0:
		return max(b, 0)
	case b <= 0:
		return a
	default:
		return min(a, b)
	}
}

// antigravitySpecs turns router entries into overlay-ready specs, taking
// numbers from the matching models.dev model in all. Limits are the smaller
// of both sources. notes records entries that need a human's eye: no
// models.dev match (router numbers used) or no usable limits (skipped).
func antigravitySpecs(router []routerModel, all []modelSpec) (specs []modelSpec, notes map[modelKey]string) {
	notes = map[modelKey]string{}
	index := map[modelKey]*modelSpec{}
	for i := range all {
		index[modelKey{all[i].Provider, all[i].ID}] = &all[i]
	}
	for _, r := range router {
		var md *modelSpec
		provider, candidates := antigravityBase(r.ID)
		for _, c := range candidates {
			if m := index[modelKey{provider, c}]; m != nil {
				md = m
				break
			}
		}

		spec := modelSpec{
			ID:       r.ID,
			Name:     antigravityName(r),
			API:      antigravityProvider,
			Provider: antigravityProvider,
		}
		if md != nil {
			spec.ContextWindow = minPositive(md.ContextWindow, r.ContextLength)
			spec.MaxTokens = minPositive(md.MaxTokens, r.MaxCompletionTokens)
			spec.Reasoning = md.Reasoning
			spec.Input = append([]string(nil), md.Input...)
			spec.SWEScore, spec.SWEInferred = md.SWEScore, md.SWEInferred
		} else {
			spec.ContextWindow = max(r.ContextLength, 0)
			spec.MaxTokens = max(r.MaxCompletionTokens, 0)
			spec.Reasoning = (r.Thinking != nil && len(r.Thinking.Levels) > 0) || agyTierSuffix.MatchString(r.ID)
			spec.Input = textImageOnly(r.SupportedInputModalities)
			notes[modelKey{antigravityProvider, r.ID}] = "no models.dev match — limits from router-for-me, " +
				"capabilities guessed; check before merging"
		}
		if spec.ContextWindow == 0 || spec.MaxTokens == 0 {
			notes[modelKey{antigravityProvider, r.ID}] = "skipped — no context window / max tokens (router-for-me lists none and fir keeps no models.dev entry for it)"
			continue
		}
		specs = append(specs, spec)
	}
	return specs, notes
}

// antigravityName is the display name, tagged like the hand-written entries.
func antigravityName(r routerModel) string {
	name := strings.TrimSpace(r.DisplayName)
	if name == "" {
		name = r.ID
	}
	if !strings.Contains(name, "Antigravity") {
		name += " (Antigravity)"
	}
	return name
}

// textImageOnly keeps the input modalities fir models (text, image).
func textImageOnly(in []string) []string {
	out := []string{}
	for _, m := range in {
		if m == "text" || m == "image" {
			out = append(out, m)
		}
	}
	if len(out) == 0 {
		out = []string{"text"}
	}
	return out
}

// extensionDeclaredRE matches the model IDs hand-declared in
// antigravity_auth.py: `_antigravity_model(\n    "<id>",`.
var extensionDeclaredRE = regexp.MustCompile(`_antigravity_model\(\s*"([^"]+)"`)

// extensionDeclaredIDs returns the Antigravity IDs the extension already
// declares. Those are known models, not new ones.
func extensionDeclaredIDs(path string) (map[string]bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	ids := map[string]bool{}
	for _, m := range extensionDeclaredRE.FindAllSubmatch(raw, -1) {
		ids[string(m[1])] = true
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("%s: no _antigravity_model declarations found", path)
	}
	return ids, nil
}

// dropDeclared removes specs (and their notes) whose ID the extension
// already declares.
func dropDeclared(specs []modelSpec, notes map[modelKey]string, declared map[string]bool) []modelSpec {
	for id := range declared {
		delete(notes, modelKey{antigravityProvider, id})
	}
	out := specs[:0:0]
	for _, s := range specs {
		if !declared[s.ID] {
			out = append(out, s)
		}
	}
	return out
}

// capExtensionAdditions keeps at most antigravityAddCap qualifying specs per
// extension provider (in their sorted order) and returns the deferred keys.
func capExtensionAdditions(specs []modelSpec) (kept []modelSpec, deferred []string) {
	count := map[string]int{}
	for _, s := range specs {
		if extensionProviders[s.Provider] {
			if count[s.Provider] >= antigravityAddCap {
				deferred = append(deferred, modelKey{s.Provider, s.ID}.String())
				continue
			}
			count[s.Provider]++
		}
		kept = append(kept, s)
	}
	sort.Strings(deferred)
	return kept, deferred
}

// extensionAnomaly guards the Antigravity source on its own: a new set far
// larger than what fir already knows for the provider means an id-scheme
// change upstream, not a model launch. Returns "" when the set looks sane.
func extensionAnomaly(added []modelSpec, deferred []string, known map[modelKey]bool) string {
	newCount := len(added) + len(deferred)
	if newCount == 0 {
		return ""
	}
	have := 0
	for k := range known {
		if extensionProviders[k.Provider] {
			have++
		}
	}
	if newCount > 3*max(have, antigravityAddCap) {
		return fmt.Sprintf("%d new %s models vs %d known — upstream anomaly, not proposing them",
			newCount, antigravityProvider, have)
	}
	return ""
}

// joinAnomaly combines two anomaly messages.
func joinAnomaly(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; " + b
}
