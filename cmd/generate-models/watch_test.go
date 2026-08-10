package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/models"
)

// registerCompiled adds models to the compiled-in catalog for the duration of
// a test, mimicking what models_generated.go's init does.
func registerCompiled(t *testing.T, provider string, ids ...string) {
	t.Helper()
	for _, id := range ids {
		ai.RegisterModel(&ai.Model{
			ID: id, Name: id, API: ai.ApiOpenAICompletions, Provider: ai.Provider(provider),
			BaseURL: "https://example.invalid", Input: []ai.InputModality{ai.InputText},
			ContextWindow: 1000, MaxTokens: 100,
		})
	}
	t.Cleanup(func() {
		for _, id := range ids {
			ai.UnregisterModel(ai.Provider(provider), id)
		}
	})
}

func spec(provider, id string) modelSpec {
	return modelSpec{ID: id, Name: id, API: "openai-completions", Provider: provider,
		Input: []string{"text"}, ContextWindow: 1000, MaxTokens: 100}
}

func TestCompareCatalogsTriggers(t *testing.T) {
	registerCompiled(t, "watch-native", "watchmodel-4-6")
	registerCompiled(t, "openrouter", "vendor/watchline-4-6")

	tests := []struct {
		name    string
		fresh   modelSpec
		trigger string
		want    bool // qualifies (and therefore opens a PR)
	}{
		{
			name:    "new model on a first-party provider",
			fresh:   spec("watch-native", "watchmodel-5"),
			trigger: triggerCurated, want: true,
		},
		{
			name:    "aggregator junk with an unknown lineage stays silent",
			fresh:   spec("openrouter", "somebody/random-finetune-v2"),
			trigger: triggerCurated, want: false,
		},
		{
			name:    "aggregator model of a lineage fir already ships",
			fresh:   spec("openrouter", "vendor/watchline-5"),
			trigger: triggerCurated, want: true,
		},
		{
			name:    "all-new takes the junk too",
			fresh:   spec("openrouter", "somebody/random-finetune-v2"),
			trigger: triggerAllNew, want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := compareCatalogs([]modelSpec{tt.fresh}, "", tt.trigger)
			key := tt.fresh.Provider + "/" + tt.fresh.ID
			if got := len(res.New) > 0 && res.New[0] == key; !got {
				t.Fatalf("expected %s to be reported as new, got %v", key, res.New)
			}
			qualified := len(res.Qualifying) == 1 && res.Qualifying[0] == key
			if qualified != tt.want {
				t.Errorf("qualifying = %v (%v), want %v", qualified, res.Qualifying, tt.want)
			}
			if res.OpenPR != tt.want {
				t.Errorf("OpenPR = %v, want %v", res.OpenPR, tt.want)
			}
		})
	}
}

// Metadata churn is the reason this feature dies of noise if it triggers on
// everything: it must be reported and never open a PR on its own.
func TestCompareCatalogsChurnDoesNotTrigger(t *testing.T) {
	registerCompiled(t, "watch-churn", "churnmodel-1")
	fresh := spec("watch-churn", "churnmodel-1")
	fresh.CostInput = 42

	res := compareCatalogs([]modelSpec{fresh}, "", triggerCurated)
	if res.Changed != 1 {
		t.Fatalf("Changed = %d, want 1", res.Changed)
	}
	if res.OpenPR {
		t.Error("pricing churn must not open a PR")
	}
	if len(res.Removed) == 0 {
		t.Error("expected the rest of the compiled catalog to be reported as removed")
	}

	// ...but the manual any-diff trigger exists precisely to see it.
	if res := compareCatalogs([]modelSpec{fresh}, "", triggerAnyDiff); !res.OpenPR {
		t.Error("any-diff must trigger on churn")
	}

	// An identical model is not a change.
	if res := compareCatalogs([]modelSpec{spec("watch-churn", "churnmodel-1")}, "", triggerCurated); res.Changed != 0 {
		t.Errorf("Changed = %d for an unchanged model, want 0", res.Changed)
	}
}

func TestCompareCatalogsAnomalyCap(t *testing.T) {
	var fresh []modelSpec
	for i := 0; i <= qualifyingCap; i++ {
		fresh = append(fresh, spec("watch-flood", fmt.Sprintf("floodmodel-%d", i)))
	}
	res := compareCatalogs(fresh, "", triggerCurated)
	if res.OpenPR {
		t.Error("an upstream anomaly must not open a PR")
	}
	if res.Anomaly == "" {
		t.Error("expected the anomaly to be reported")
	}
}

func TestFlagshipAndReport(t *testing.T) {
	ai.RegisterModel(&ai.Model{
		ID: "flagmodel-4-6", Name: "Flag 4.6", API: ai.ApiOpenAICompletions, Provider: "watch-flag",
		Input: []ai.InputModality{ai.InputText}, ContextWindow: 1000, MaxTokens: 100,
		Compaction: true, AdaptiveThinking: true, SWEScore: 80.5,
	})
	t.Cleanup(func() { ai.UnregisterModel("watch-flag", "flagmodel-4-6") })

	fresh := spec("watch-flag", "flagmodel-5")
	res := compareCatalogs([]modelSpec{fresh}, "", triggerCurated)
	if !res.OpenPR {
		t.Fatal("expected the new flagship to qualify")
	}

	key, f := flagship(fresh, compiledCatalog())
	if f == nil || key.ID != "flagmodel-4-6" {
		t.Fatalf("flagship = %v, want watch-flag/flagmodel-4-6", key)
	}

	path := filepath.Join(t.TempDir(), "report.md")
	if err := writeReport(path, res, []string{"watch-flag/flagmodel-5"}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	report := string(body)
	// The report has to tell a human what is still missing on the new model.
	for _, want := range []string{"flagmodel-5", "compaction", "adaptiveThinking", "sweScore",
		"flagmodel-4-6", "Catalog overlay"} {
		if !strings.Contains(report, want) {
			t.Errorf("report is missing %q:\n%s", want, report)
		}
	}
}

func TestOverlayDefinitionCarriesOnlyAssertedFields(t *testing.T) {
	m := spec("anthropic", "claude-watch-5")
	m.BaseURL = "https://api.anthropic.com"
	m.Headers = map[string]string{"x-secret": "1"}
	m.SWEScore, m.SWEInferred = 81.0, true // inherited from a relative, not measured
	m.Reasoning = true

	def := overlayDefinition(m)
	if def.BaseURL != "" || def.Headers != nil {
		t.Error("overlay entries must never carry baseUrl or headers")
	}
	if def.SWEScore != nil {
		t.Error("an inferred SWE score must not be published")
	}
	if def.ID != m.ID || def.Reasoning == nil || !*def.Reasoning || *def.ContextWindow != 1000 {
		t.Errorf("upstream-asserted fields not carried through: %+v", def)
	}
}

func TestUpdateOverlay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog-v1.json")
	seed := `{
  "schemaVersion": 1,
  "generatedAt": "2020-01-01T00:00:00Z",
  "providers": {
    "anthropic": {"models": [{"id": "hand-curated", "api": "anthropic-messages", "compaction": true}]}
  }
}`
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}

	newModel := spec("anthropic", "claude-watch-5")
	// A model whose behaviour the overlay schema cannot express must be
	// skipped rather than published in a quietly weaker form.
	unexpressible := spec("anthropic", "claude-watch-6")
	unexpressible.Compat = &compatSpec{ZaiToolStream: boolPtr(true)}

	added, skipped, err := updateOverlay(path, []modelSpec{newModel, unexpressible})
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 1 || added[0] != "anthropic/claude-watch-5" {
		t.Fatalf("added = %v", added)
	}
	if len(skipped) != 1 || !strings.Contains(skipped[0], "zaiToolStream") {
		t.Fatalf("skipped = %v", skipped)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	overlay, err := models.ParseCatalogOverlay(raw)
	if err != nil {
		t.Fatalf("the written overlay must load with the production parser: %v", err)
	}
	if overlay.GeneratedAt.Year() == 2020 {
		t.Error("generatedAt must be refreshed, or the fleet ignores the document")
	}
	ids := []string{}
	for _, m := range overlay.Providers["anthropic"].Models {
		ids = append(ids, m.ID)
	}
	if len(ids) != 2 || ids[0] != "hand-curated" {
		t.Fatalf("hand-curated entries must survive untouched, got %v", ids)
	}

	// Re-running adds nothing and leaves the document alone.
	before, _ := os.ReadFile(path)
	added, _, err = updateOverlay(path, []modelSpec{newModel})
	if err != nil || len(added) != 0 {
		t.Fatalf("second run: added=%v err=%v", added, err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Error("a no-op run must not rewrite the overlay")
	}
}

func TestWriteSummaryShape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "summary.json")
	if err := writeSummary(path, &watchResult{Trigger: triggerCurated, Failed: []string{"poe: boom"}}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	// The workflow branches on open_pr and reports sources_failed.
	if got["open_pr"] != false {
		t.Errorf("open_pr = %v", got["open_pr"])
	}
	if _, ok := got["sources_failed"]; !ok {
		t.Error("sources_failed must be present when a source failed")
	}
}

// Models shipped by the catalog overlay already reached the fleet, so they are
// not news — without this the same handful is reported every single night.
func TestCatalogOverlayIsPartOfTheBaseline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog-v1.json")
	seed := `{"schemaVersion":1,"generatedAt":"2026-01-01T00:00:00Z","providers":
	  {"anthropic":{"models":[{"id":"claude-overlay-5","api":"anthropic-messages"}]}}}`
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}

	fresh := spec("anthropic", "claude-overlay-5")
	res := compareCatalogs([]modelSpec{fresh}, path, triggerCurated)
	if len(res.New) != 0 || res.OpenPR {
		t.Errorf("overlay-shipped model reported as new: %v", res.New)
	}

	// A missing/invalid overlay degrades to compiled-only rather than failing.
	if res := compareCatalogs([]modelSpec{fresh}, filepath.Join(t.TempDir(), "nope.json"), triggerCurated); len(res.New) != 1 {
		t.Errorf("expected the model to be new without an overlay, got %v", res.New)
	}
}

// OpenRouter enables suffix variants (:batch, :free, :exacto) in job lots —
// dozens of "new" ids for models fir already ships.
func TestAggregatorVariantSuffixIsNotNews(t *testing.T) {
	registerCompiled(t, "openrouter", "vendor/varline-5")

	variant := spec("openrouter", "vendor/varline-5:batch")
	res := compareCatalogs([]modelSpec{variant}, "", triggerCurated)
	if len(res.New) != 1 {
		t.Fatalf("the variant is still reported as new: %v", res.New)
	}
	if len(res.Qualifying) != 0 || res.OpenPR {
		t.Errorf("a variant of a known model must not open a PR: %v", res.Qualifying)
	}

	// A variant of an id fir does NOT have still qualifies on lineage — that
	// is a genuinely new generation appearing on the aggregator first.
	next := spec("openrouter", "vendor/varline-6:batch")
	if res := compareCatalogs([]modelSpec{next}, "", triggerCurated); !res.OpenPR {
		t.Errorf("a new generation must qualify even with a variant suffix: %+v", res.Qualifying)
	}
}

// The report must read the same every night: a flagship picked out of map
// iteration order would make it flap between equally-new relatives.
func TestFlagshipIsDeterministicAndPrefersSameProvider(t *testing.T) {
	registerCompiled(t, "watch-b", "detmodel-4-6")
	registerCompiled(t, "watch-a", "detmodel-4-6")
	registerCompiled(t, "watch-a", "detmodel-4-5")

	newModel := spec("watch-b", "detmodel-5")
	first, _ := flagship(newModel, compiledCatalog())
	for range 20 {
		if got, _ := flagship(newModel, compiledCatalog()); got != first {
			t.Fatalf("flagship flapped: %v vs %v", got, first)
		}
	}
	// Newest generation wins, and the pinned model's own provider breaks the tie.
	if first.Provider != "watch-b" || first.ID != "detmodel-4-6" {
		t.Errorf("flagship = %v, want watch-b/detmodel-4-6", first)
	}
}
