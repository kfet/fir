package main

// Nightly model-watch: notice that upstream has models fir does not know
// about, and report it in a form a human can act on.
//
// The compiled-in catalog (pkg/ai/models_generated.go, loaded via the ai
// package's init) IS the baseline — the checkout the workflow runs against is
// by definition the previous generation, so there is no snapshot file to keep
// and no Go source to parse.
//
// The hard part is not detection, it is SILENCE. Upstream churns pricing and
// metadata constantly and the aggregators (OpenRouter, Vercel AI Gateway, Poe)
// add finetunes and bots daily. A PR that fires every night is ignored within
// a week, which makes the whole feature worthless. So a PR is opened only for
// a new model that is plausibly interesting — see qualifies().

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/models"
)

// Trigger modes, selected with -trigger. Explicit and configurable because
// the right noise threshold is a judgement call that may need revisiting from
// a workflow_dispatch without a code change.
const (
	// triggerCurated opens a PR only for a new model on a first-party
	// provider, or one whose product line fir already ships. The default.
	triggerCurated = "curated"
	// triggerAllNew opens a PR for any new id anywhere, aggregators included.
	triggerAllNew = "all-new"
	// triggerAnyDiff also opens a PR for pure metadata churn. For testing the
	// pipeline end to end; never for the schedule.
	triggerAnyDiff = "any-diff"
)

// aggregatorProviders resell other people's models and list everything that
// exists, including junk. A new id here is not evidence that anything
// happened — unless it belongs to a product line fir already ships.
var aggregatorProviders = map[string]bool{
	"openrouter":        true,
	"vercel-ai-gateway": true,
	"poe":               true,
}

// qualifyingCap guards against an upstream anomaly — an id-scheme change or a
// schema break can make hundreds of models look new at once. Past this many
// qualifying models we report an anomaly and open nothing: a human reading a
// 400-model PR learns nothing.
const qualifyingCap = 50

// modelKey identifies a model across the fresh fetch and the compiled catalog.
type modelKey struct{ Provider, ID string }

func (k modelKey) String() string { return k.Provider + "/" + k.ID }

// watchResult is the outcome of one nightly comparison.
type watchResult struct {
	Trigger    string      `json:"trigger"`
	OpenPR     bool        `json:"open_pr"`
	New        []string    `json:"new"`
	Qualifying []string    `json:"qualifying"`
	Removed    []string    `json:"removed"`
	Changed    int         `json:"changed"`
	Anomaly    string      `json:"anomaly,omitempty"`
	Failed     []string    `json:"sources_failed,omitempty"`
	specs      []modelSpec // qualifying specs, in report order
}

// compiledCatalog snapshots the models compiled into this binary.
func compiledCatalog() map[modelKey]*ai.Model {
	out := map[modelKey]*ai.Model{}
	for _, p := range ai.GetProviders() {
		for _, m := range ai.GetModels(p) {
			out[modelKey{string(p), m.ID}] = m
		}
	}
	return out
}

// knownModels is the real "does fir know this model" baseline: the compiled-in
// catalog PLUS the committed catalog overlay. Leaving the overlay out would
// re-report every overlay-shipped model as new for eternity — they are exactly
// the models that reached the fleet without a release.
func knownModels(compiled map[modelKey]*ai.Model, overlayPath string) map[modelKey]bool {
	known := make(map[modelKey]bool, len(compiled))
	for k := range compiled {
		known[k] = true
	}
	if overlayPath == "" {
		return known
	}
	raw, err := os.ReadFile(overlayPath)
	if err != nil {
		log.Printf("Warning: catalog overlay unreadable (%v); treating overlay models as unknown", err)
		return known
	}
	overlay, err := models.ParseCatalogOverlay(raw)
	if err != nil {
		log.Printf("Warning: catalog overlay invalid (%v); treating overlay models as unknown", err)
		return known
	}
	for provider, pc := range overlay.Providers {
		for _, m := range pc.Models {
			known[modelKey{provider, m.ID}] = true
		}
	}
	return known
}

// qualifies decides whether a new model is worth waking a human for.
//
//   - A first-party provider's catalogue is small and human-curated upstream,
//     so any new entry there is signal.
//   - On an aggregator, only a model whose product line fir already ships
//     counts — that is the "Claude Opus 5 shows up on OpenRouter first" case,
//     and it needs the generation-STRIPPED lineage: claude-opus-5 is a new
//     *family* but the same *lineage* as claude-opus-4-6.
func qualifies(m modelSpec, known map[modelKey]bool, lineages map[string]bool, trigger string) bool {
	if trigger != triggerCurated {
		return true
	}
	if !aggregatorProviders[m.Provider] {
		return true
	}
	// "<known id>:variant" — :batch, :free, :exacto, :nitro — is a routing
	// variant of a model fir already ships, not a new model. Upstream adds
	// these in job lots (OpenRouter turned on :batch for ~55 ids in one go),
	// which is precisely the flood that would make the PR worthless.
	if base, _, found := strings.Cut(m.ID, ":"); found && known[modelKey{m.Provider, base}] {
		return false
	}
	return lineages[ai.ExtractLineage(m.ID)]
}

// compareCatalogs diffs a fresh fetch against the compiled-in catalog.
func compareCatalogs(fresh []modelSpec, overlayPath, trigger string) *watchResult {
	compiled := compiledCatalog()
	known := knownModels(compiled, overlayPath)

	lineages := map[string]bool{}
	for k := range known {
		if l := ai.ExtractLineage(k.ID); l != "" {
			lineages[l] = true
		}
	}

	res := &watchResult{Trigger: trigger}
	seen := map[modelKey]bool{}
	for _, m := range fresh {
		key := modelKey{m.Provider, m.ID}
		seen[key] = true
		if !known[key] {
			res.New = append(res.New, key.String())
			if qualifies(m, known, lineages, trigger) {
				res.specs = append(res.specs, m)
			}
			continue
		}
		// Churn is measured against the compiled-in catalog only: that is
		// what a release would change. Overlay-only models have no compiled
		// counterpart to differ from.
		if old, ok := compiled[key]; ok && metadataChanged(m, old) {
			res.Changed++
		}
	}
	// Removals likewise track the compiled-in catalog.
	for key := range compiled {
		if !seen[key] {
			res.Removed = append(res.Removed, key.String())
		}
	}
	sort.Strings(res.New)
	sort.Strings(res.Removed)
	sort.Slice(res.specs, func(i, j int) bool {
		return modelKey{res.specs[i].Provider, res.specs[i].ID}.String() <
			modelKey{res.specs[j].Provider, res.specs[j].ID}.String()
	})
	// Qualifying is the reported view of specs — one list, one order.
	for _, m := range res.specs {
		res.Qualifying = append(res.Qualifying, modelKey{m.Provider, m.ID}.String())
	}

	switch {
	case len(res.Qualifying) > qualifyingCap:
		res.Anomaly = fmt.Sprintf("%d qualifying new models (cap %d) — upstream anomaly, not opening a PR",
			len(res.Qualifying), qualifyingCap)
	case trigger == triggerAnyDiff:
		res.OpenPR = len(res.Qualifying) > 0 || res.Changed > 0 || len(res.Removed) > 0
	default:
		res.OpenPR = len(res.Qualifying) > 0
	}
	return res
}

// metadataChanged reports whether upstream moved anything fir records about a
// model it already knows. Deliberately narrow: these are the fields that come
// straight from upstream, so a difference is upstream news rather than a
// curation decision of ours.
//
// Costs are compared at the precision the generator EMITS: the aggregators
// hand us $/token × 1e6, so a freshly fetched 0.30000000000000004 would differ
// forever from the 0.3 in the generated file and report ~220 phantom changes
// every night.
func metadataChanged(m modelSpec, old *ai.Model) bool {
	return roundEmitted(m.CostInput) != old.Cost.Input ||
		roundEmitted(m.CostOutput) != old.Cost.Output ||
		roundEmitted(m.CostCacheRead) != old.Cost.CacheRead ||
		roundEmitted(m.CostCacheWrite) != old.Cost.CacheWrite ||
		m.ContextWindow != old.ContextWindow ||
		m.MaxTokens != old.MaxTokens ||
		m.Reasoning != old.Reasoning
}

// curatedFields are the fir-specific fields that upstream never supplies: they
// are hand-maintained in applyOverridesAndAdditions(). A new model always
// arrives without them, and the point of the report is to say which ones its
// closest relative has, so the human knows what is left to do.
//
// An inferred SWE score does not count as curated — it was guessed from a
// relative, so reporting it as "missing" would ask a human to copy a number
// nobody measured.
func curatedFields(compaction, adaptive bool, effort []string, swe float64, inferred, hasCompat bool) map[string]string {
	out := map[string]string{}
	if compaction {
		out["compaction"] = "true"
	}
	if adaptive {
		out["adaptiveThinking"] = "true"
	}
	if len(effort) > 0 {
		out["reasoningEffortValues"] = strings.Join(effort, ",")
	}
	if swe > 0 && !inferred {
		out["sweScore"] = fmt.Sprintf("%.1f", swe)
	}
	if hasCompat {
		out["compat"] = "set"
	}
	return out
}

// flagship returns the newest compiled-in model of the same lineage — the best
// available answer to "what does a fully curated sibling of this look like".
// Reuses the shared lineage/generation heuristics rather than inventing a
// second notion of relatedness.
func flagship(m modelSpec, compiled map[modelKey]*ai.Model) (modelKey, *ai.Model) {
	lineage := ai.ExtractLineage(m.ID)
	if lineage == "" {
		return modelKey{}, nil
	}
	// Sorted so that a tie between two equally-new relatives resolves the
	// same way every night; map order would make the report flap.
	keys := make([]modelKey, 0, len(compiled))
	for k := range compiled {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })

	var bestKey modelKey
	var best *ai.Model
	var bestGen []int
	for _, k := range keys {
		if ai.ExtractLineage(k.ID) != lineage {
			continue
		}
		gen, ok := ai.GenerationVector(k.ID)
		if !ok {
			continue
		}
		cmp := 0
		if best != nil {
			cmp = ai.CompareGenerations(gen, bestGen)
		}
		// Newer wins; on a tie prefer the same provider, so the comparison
		// is like-for-like.
		if best == nil || cmp > 0 || (cmp == 0 && k.Provider == m.Provider && bestKey.Provider != m.Provider) {
			bestKey, best, bestGen = k, compiled[k], gen
		}
	}
	return bestKey, best
}

// writeSummary writes the machine-readable verdict the workflow branches on.
func writeSummary(path string, res *watchResult) error {
	data, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// writeReport writes the PR body: what is new, what merely churned, and — for
// each new model — which curated fields its closest relative has and it does
// not.
func writeReport(path string, res *watchResult, overlayAdded []string) error {
	compiled := compiledCatalog()
	var b strings.Builder

	fmt.Fprintf(&b, "Automated model-catalog refresh (`make generate-models`, trigger `%s`).\n\n", res.Trigger)
	if res.Anomaly != "" {
		fmt.Fprintf(&b, "> **Anomaly:** %s\n\n", res.Anomaly)
	}
	fmt.Fprintf(&b, "- **%d new** model(s) upstream, **%d qualifying**\n", len(res.New), len(res.Qualifying))
	fmt.Fprintf(&b, "- %d model(s) with changed pricing/limits\n", res.Changed)
	fmt.Fprintf(&b, "- %d model(s) no longer listed upstream (reported only — never a trigger, "+
		"because a partial upstream outage looks exactly like a mass removal)\n\n", len(res.Removed))

	if len(res.specs) > 0 {
		b.WriteString("## New models\n\n")
	}
	for _, m := range res.specs {
		key := modelKey{m.Provider, m.ID}
		fmt.Fprintf(&b, "### `%s`\n\n", key)
		fmt.Fprintf(&b, "- name: %s\n- context: %d, max tokens: %d\n- cost: in %.2f / out %.2f per Mtok\n- reasoning: %v\n",
			m.Name, m.ContextWindow, m.MaxTokens, m.CostInput, m.CostOutput, m.Reasoning)

		fKey, f := flagship(m, compiled)
		if f == nil {
			fmt.Fprintf(&b, "- no compiled-in relative of lineage `%s` to compare against — "+
				"curated fields must be decided from scratch\n", ai.ExtractLineage(m.ID))
		} else {
			have := curatedFields(m.Compaction, m.AdaptiveThinking, m.ReasoningEffortValues, m.SWEScore, m.SWEInferred, m.Compat != nil)
			want := curatedFields(f.Compaction, f.AdaptiveThinking, f.ReasoningEffortValues, f.SWEScore, f.SWEInferred, f.Compat != nil)
			var missing []string
			for field, val := range want {
				if _, ok := have[field]; !ok {
					missing = append(missing, fmt.Sprintf("`%s` (`%s` has %s)", field, fKey, val))
				}
			}
			sort.Strings(missing)
			if len(missing) == 0 {
				fmt.Fprintf(&b, "- curated fields match its closest relative `%s`\n", fKey)
			} else {
				fmt.Fprintf(&b, "- **curated fields to review** (hand-maintained in "+
					"`applyOverridesAndAdditions()` in `cmd/generate-models/main.go`): %s\n",
					strings.Join(missing, ", "))
			}
		}
		b.WriteString("\n")
	}

	if len(overlayAdded) > 0 {
		b.WriteString("## Catalog overlay\n\n")
		fmt.Fprintf(&b, "`pkg/models/catalog-v1.json` gained %d entry(ies): %s.\n\n",
			len(overlayAdded), "`"+strings.Join(overlayAdded, "`, `")+"`")
		b.WriteString("Merging this PR publishes them to the fleet within the catalog TTL — " +
			"**without** waiting for a release. Each entry holds exactly what the generator " +
			"produced for that model — the same values the next release would compile in — minus " +
			"`baseUrl`/`headers` (the overlay validator forbids them) and inferred SWE scores; " +
			"nothing behavioural is guessed from a sibling model. Delete the hunk if you would " +
			"rather wait for a release.\n\n" +
			"Note: an overlay entry redefines a model wholesale, so once curated fields are added " +
			"for these ids in `applyOverridesAndAdditions()`, update or drop the overlay entry too — " +
			"otherwise it keeps shadowing the richer built-in definition.\n\n")
	}

	if res.Changed > 0 || len(res.Removed) > 0 {
		b.WriteString("## Churn\n\n")
		fmt.Fprintf(&b, "Pricing/limit changes are folded into this PR but never open one on their own.\n")
		if len(res.Removed) > 0 {
			fmt.Fprintf(&b, "\nNo longer listed upstream: `%s`\n", strings.Join(res.Removed, "`, `"))
		}
	}

	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// --- catalog overlay proposal ---

// overlayUnexpressible lists compat fields the generator can produce but the
// overlay schema cannot carry. A model needing one of them must wait for a
// release rather than ship a quietly weaker definition to the fleet.
func overlayUnexpressible(c *compatSpec) []string {
	if c == nil {
		return nil
	}
	var out []string
	if c.ZaiToolStream != nil {
		out = append(out, "zaiToolStream")
	}
	if c.RequiresReasoningContentOnAssistantMessages != nil {
		out = append(out, "requiresReasoningContentOnAssistantMessages")
	}
	if len(c.ReasoningEffortMap) > 0 {
		out = append(out, "reasoningEffortMap")
	}
	return out
}

// overlayDefinition renders a model spec as a catalog-overlay entry. It copies
// what the generator produced for THIS model — the same values the next
// release would compile in, never anything inherited from a relative — with
// two deliberate exclusions: baseUrl/headers (the overlay
// validator forbids them: an overlay may change what a request looks like,
// never where it goes) and an inferred SWE score (that one IS guessed from a
// family, and would order the model by a number nobody measured).
func overlayDefinition(m modelSpec) models.ModelDefinition {
	def := models.ModelDefinition{
		ID:                    m.ID,
		Name:                  m.Name,
		Api:                   m.API,
		Reasoning:             boolPtr(m.Reasoning),
		ReasoningEffortValues: m.ReasoningEffortValues,
		Input:                 m.Input,
		Cost: &models.ModelCostConfig{
			Input: &m.CostInput, Output: &m.CostOutput,
			CacheRead: &m.CostCacheRead, CacheWrite: &m.CostCacheWrite,
		},
		ContextWindow: &m.ContextWindow,
		MaxTokens:     &m.MaxTokens,
		ServerTools:   m.ServerTools,
	}
	if m.Compaction {
		def.Compaction = boolPtr(true)
	}
	if m.AdaptiveThinking {
		def.AdaptiveThinking = boolPtr(true)
	}
	if m.SWEScore > 0 && !m.SWEInferred {
		score := m.SWEScore
		def.SWEScore = &score
	}
	if c := m.Compat; c != nil {
		def.Compat = &models.CompatConfig{
			SupportsStore:           c.SupportsStore,
			SupportsDeveloperRole:   c.SupportsDeveloperRole,
			SupportsReasoningEffort: c.SupportsReasoningEffort,
			ThinkingFormat:          c.ThinkingFormat,
		}
	}
	return def
}

// updateOverlay adds the qualifying new models to the catalog overlay so that
// merging the PR reaches the fleet without a release — the whole point of the
// overlay. Existing entries are never touched: several are hand-curated, and
// clobbering them from a scheduled job is exactly the unattended-publish
// failure this design avoids.
//
// Returns the ids added and the ids skipped-with-reason.
func updateOverlay(path string, specs []modelSpec) (added, skipped []string, err error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	overlay, err := models.ParseCatalogOverlay(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("existing overlay is invalid: %w", err)
	}

	for _, m := range specs {
		if bad := overlayUnexpressible(m.Compat); len(bad) > 0 {
			skipped = append(skipped, fmt.Sprintf("%s/%s (overlay cannot express %s)",
				m.Provider, m.ID, strings.Join(bad, ", ")))
			continue
		}
		pc := overlay.Providers[m.Provider]
		if slices.ContainsFunc(pc.Models, func(d models.ModelDefinition) bool { return d.ID == m.ID }) {
			continue
		}
		pc.Models = append(pc.Models, overlayDefinition(m))
		overlay.Providers[m.Provider] = pc
		added = append(added, m.Provider+"/"+m.ID)
	}
	if len(added) == 0 {
		return nil, skipped, nil
	}

	overlay.GeneratedAt = time.Now().UTC().Truncate(time.Second)
	out, err := models.MarshalCatalogOverlay(overlay)
	if err != nil {
		return nil, skipped, err
	}
	// Validate with the very code that will load it in production, so a
	// document that could not be used never reaches a PR.
	if _, err := models.ParseCatalogOverlay(out); err != nil {
		return nil, skipped, fmt.Errorf("proposed overlay is invalid: %w", err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return nil, skipped, err
	}
	return added, skipped, nil
}

// watchOptions is the nightly-watch half of a generator run.
type watchOptions struct {
	fresh       []modelSpec
	trigger     string
	reportPath  string
	summaryPath string
	// overlayPath is the committed catalog overlay. It is always part of the
	// "already known" baseline; it is only WRITTEN when proposeOverlay is set.
	overlayPath    string
	proposeOverlay bool
}

// runWatch compares the fresh fetch with the compiled-in catalog and writes
// whatever outputs were asked for. A no-op when no watch flag is set, so
// `make generate-models` is unaffected.
func runWatch(opts watchOptions) {
	if opts.reportPath == "" && opts.summaryPath == "" && !opts.proposeOverlay {
		return
	}
	switch opts.trigger {
	case triggerCurated, triggerAllNew, triggerAnyDiff:
	default:
		log.Fatalf("unknown -trigger %q (want %s, %s or %s)",
			opts.trigger, triggerCurated, triggerAllNew, triggerAnyDiff)
	}

	res := compareCatalogs(opts.fresh, opts.overlayPath, opts.trigger)
	log.Printf("Model watch: %d new (%d qualifying), %d changed, %d removed; open_pr=%v %s",
		len(res.New), len(res.Qualifying), res.Changed, len(res.Removed), res.OpenPR, res.Anomaly)

	// The overlay is only proposed for a run that is actually opening a PR:
	// merging it publishes to the whole fleet within the catalog TTL, so it
	// must never be a side effect of a run nobody looks at.
	var added []string
	if opts.proposeOverlay && res.OpenPR {
		var skipped []string
		var err error
		added, skipped, err = updateOverlay(opts.overlayPath, res.specs)
		if err != nil {
			log.Fatalf("catalog overlay: %v", err)
		}
		for _, s := range skipped {
			log.Printf("Model watch: overlay entry skipped for %s", s)
		}
	}

	if opts.reportPath != "" {
		if err := writeReport(opts.reportPath, res, added); err != nil {
			log.Fatalf("write report: %v", err)
		}
	}
	if opts.summaryPath != "" {
		if err := writeSummary(opts.summaryPath, res); err != nil {
			log.Fatalf("write summary: %v", err)
		}
	}
}
