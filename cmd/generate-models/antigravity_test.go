package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kfet/fir/pkg/models"
)

func TestValidateRouterModels(t *testing.T) {
	if _, err := validateRouterModels(map[string][]routerModel{"other": {{ID: "x"}}}); err == nil {
		t.Error("missing antigravity key must be a failed source, not an empty list")
	}
	if _, err := validateRouterModels(map[string][]routerModel{"antigravity": {}}); err == nil {
		t.Error("empty antigravity list must be a failed source")
	}
	if _, err := validateRouterModels(map[string][]routerModel{"antigravity": {{ID: " "}}}); err == nil {
		t.Error("empty id must be rejected")
	}
	got, err := validateRouterModels(map[string][]routerModel{"antigravity": {{ID: "gemini-3-flash"}}})
	if err != nil || len(got) != 1 {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestAntigravityBase(t *testing.T) {
	cases := map[string][2]string{
		"gemini-4-neon-xhigh":      {"google", "gemini-4-neon"},
		"gemini-3.8-flash-tiered":  {"google", "gemini-3.8-flash"},
		"gemini-3-flash":           {"google", "gemini-3-flash"},
		"claude-opus-4-6-thinking": {"anthropic", "claude-opus-4-6"},
		"claude-opus-5.5-high":     {"anthropic", "claude-opus-5-5"},
		"gpt-oss-120b-medium":      {"", ""},
	}
	for id, want := range cases {
		p, c := antigravityBase(id)
		first := ""
		if len(c) > 0 {
			first = c[0]
		}
		if p != want[0] || first != want[1] {
			t.Errorf("%s: got %s %v, want %v", id, p, c, want)
		}
	}
}

func TestAntigravitySpecs(t *testing.T) {
	md := []modelSpec{
		{Provider: "google", ID: "gemini-4-neon", ContextWindow: 1_048_576, MaxTokens: 65_536,
			Reasoning: true, Input: []string{"text", "image"}, CostInput: 2, CostOutput: 12, SWEScore: 85},
		{Provider: "google", ID: "gemini-3-flash-preview", ContextWindow: 1_000_000, MaxTokens: 65_536, Reasoning: true},
		{Provider: "anthropic", ID: "claude-opus-5-5", ContextWindow: 1_000_000, MaxTokens: 128_000, CostInput: 5},
	}
	router := []routerModel{
		// Antigravity caps below the public API: the smaller window wins.
		{ID: "gemini-4-neon-xhigh", DisplayName: "Gemini 4 Neon", ContextLength: 524_288, MaxCompletionTokens: 65_536},
		// Only the -preview id exists on models.dev; router omits limits.
		{ID: "gemini-3-flash"},
		{ID: "claude-opus-5-5-high", ContextLength: 2_000_000, MaxCompletionTokens: 64_000},
		{ID: "gpt-oss-120b-medium", ContextLength: 114_000, MaxCompletionTokens: 32_768,
			SupportedInputModalities: []string{"text", "audio"}},
		{ID: "gemini-9-image"},
	}
	specs, notes := antigravitySpecs(router, md)
	byID := map[string]modelSpec{}
	for _, s := range specs {
		byID[s.ID] = s
		if s.Provider != antigravityProvider || s.API != antigravityProvider {
			t.Errorf("%s: provider/api = %s/%s", s.ID, s.Provider, s.API)
		}
		if s.CostInput != 0 || s.CostOutput != 0 || s.CostCacheRead != 0 || s.CostCacheWrite != 0 {
			t.Errorf("%s: cost must be zero (subscription-billed): %+v", s.ID, s)
		}
	}

	neon := byID["gemini-4-neon-xhigh"]
	if neon.ContextWindow != 524_288 || neon.MaxTokens != 65_536 || !neon.Reasoning || neon.SWEScore != 85 {
		t.Errorf("neon = %+v", neon)
	}
	if neon.Name != "Gemini 4 Neon (Antigravity)" {
		t.Errorf("name = %q", neon.Name)
	}
	if f := byID["gemini-3-flash"]; f.ContextWindow != 1_000_000 || f.MaxTokens != 65_536 {
		t.Errorf("flash = %+v (want models.dev -preview numbers)", f)
	}
	if o := byID["claude-opus-5-5-high"]; o.ContextWindow != 1_000_000 || o.MaxTokens != 64_000 {
		t.Errorf("opus = %+v (want the smaller of each limit)", o)
	}
	oss := byID["gpt-oss-120b-medium"]
	if oss.ContextWindow != 114_000 || strings.Join(oss.Input, ",") != "text" {
		t.Errorf("gpt-oss = %+v (want router numbers, text only)", oss)
	}
	if _, ok := byID["gemini-9-image"]; ok {
		t.Error("a model with no limits anywhere must be skipped")
	}
	if !strings.Contains(notes[modelKey{antigravityProvider, "gpt-oss-120b-medium"}], "no models.dev match") {
		t.Errorf("notes = %v", notes)
	}
	if !strings.Contains(notes[modelKey{antigravityProvider, "gemini-9-image"}], "skipped") {
		t.Errorf("notes = %v", notes)
	}
	if _, ok := notes[modelKey{antigravityProvider, "gemini-4-neon-xhigh"}]; ok {
		t.Error("a clean match needs no note")
	}
}

func TestExtensionDeclaredIDsMatchesTheExtension(t *testing.T) {
	ids, err := extensionDeclaredIDs(filepath.Join(repoRoot(), "pkg", "resources", "builtin_extensions", "antigravity_auth.py"))
	if err != nil {
		t.Fatal(err)
	}
	if !ids["gemini-3-flash"] || !ids["claude-opus-4-6-thinking"] {
		t.Fatalf("ids = %v", ids)
	}

	empty := filepath.Join(t.TempDir(), "x.py")
	if err := os.WriteFile(empty, []byte("print(1)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := extensionDeclaredIDs(empty); err == nil {
		t.Error("a file with no declarations must be an error, or every id looks new")
	}

	notes := map[modelKey]string{{antigravityProvider, "a"}: "n", {antigravityProvider, "b"}: "n"}
	out := dropDeclared([]modelSpec{spec(antigravityProvider, "a"), spec(antigravityProvider, "b")}, notes, map[string]bool{"a": true})
	if len(out) != 1 || out[0].ID != "b" || len(notes) != 1 {
		t.Fatalf("out = %v notes = %v", out, notes)
	}
}

func TestAntigravityWatchToOverlay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog-v1.json")
	seed := `{
  "schemaVersion": 1,
  "generatedAt": "2020-01-01T00:00:00Z",
  "providers": {},
  "extensionProviders": {
    "google-antigravity": {"models": [{"id": "already-there", "contextWindow": 10, "maxTokens": 5}]}
  }
}`
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}

	var fresh []modelSpec
	for i := 0; i < antigravityAddCap+2; i++ {
		s := spec(antigravityProvider, fmt.Sprintf("gemini-x-%02d", i))
		s.API, s.ContextWindow, s.MaxTokens = antigravityProvider, 1000, 100
		fresh = append(fresh, s)
	}
	fresh = append(fresh, spec(antigravityProvider, "already-there"))

	res := compareCatalogs(fresh, path, triggerCurated)
	if len(res.Qualifying) != antigravityAddCap {
		t.Fatalf("qualifying = %d, want the per-run cap %d", len(res.Qualifying), antigravityAddCap)
	}
	if len(res.Deferred) != 2 {
		t.Fatalf("deferred = %v", res.Deferred)
	}
	for _, q := range res.Qualifying {
		if q == antigravityProvider+"/already-there" {
			t.Fatal("an overlay extensionProviders model must count as known")
		}
	}
	for _, r := range res.Removed {
		if strings.HasPrefix(r, antigravityProvider+"/") {
			t.Fatalf("extension models must never be reported removed: %v", r)
		}
	}

	added, _, err := updateOverlay(path, res.specs, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != antigravityAddCap {
		t.Fatalf("added = %v", added)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	overlay, err := models.ParseCatalogOverlay(raw)
	if err != nil {
		t.Fatalf("written overlay must parse (provider not registered here): %v", err)
	}
	if _, ok := overlay.Providers[antigravityProvider]; ok {
		t.Fatal("extension-provider models must not go into providers: old binaries reject the whole document")
	}
	got := overlay.ExtensionProviders[antigravityProvider].Models
	if len(got) != antigravityAddCap+1 || got[0].ID != "already-there" {
		t.Fatalf("extensionProviders models = %v", got)
	}
	if c := got[1].Cost; c == nil || *c.Input != 0 || *c.Output != 0 {
		t.Fatalf("cost = %+v", c)
	}
}

func TestReportListsAntigravityNotes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.md")
	s := spec(antigravityProvider, "gemini-x")
	res := &watchResult{
		Trigger:  triggerCurated,
		specs:    []modelSpec{s},
		Notes:    []string{"`google-antigravity/gemini-x`: no models.dev match"},
		Deferred: []string{"google-antigravity/gemini-y"},
	}
	if err := writeReport(path, res, nil); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	for _, want := range []string{"## Needs a human", "no models.dev match", "gemini-y", "cost zero"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("report lacks %q:\n%s", want, raw)
		}
	}
}

func agySpecs(n int) []modelSpec {
	var out []modelSpec
	for i := 0; i < n; i++ {
		s := spec(antigravityProvider, fmt.Sprintf("gemini-y-%02d", i))
		s.API, s.ContextWindow, s.MaxTokens = antigravityProvider, 1000, 100
		out = append(out, s)
	}
	return out
}

// Aggregator churn past the global cap must not starve Antigravity: the run
// becomes overlay-only, carrying just the extension-provider models.
func TestGlobalAnomalyYieldsOverlayOnlyPR(t *testing.T) {
	var fresh []modelSpec
	for i := 0; i <= qualifyingCap; i++ {
		fresh = append(fresh, spec("watch-flood", fmt.Sprintf("floodmodel-%d", i)))
	}
	fresh = append(fresh, agySpecs(2)...)
	res := compareCatalogs(fresh, "", triggerCurated)
	if !res.OverlayOnly || !res.OpenPR || res.Anomaly == "" {
		t.Fatalf("overlay_only=%v open_pr=%v anomaly=%q", res.OverlayOnly, res.OpenPR, res.Anomaly)
	}
	if len(res.Qualifying) != 2 {
		t.Fatalf("qualifying = %v, want only the Antigravity models", res.Qualifying)
	}
	for _, m := range res.specs {
		if m.Provider != antigravityProvider {
			t.Fatalf("overlay-only run would write %s/%s into the overlay", m.Provider, m.ID)
		}
	}
}

// A flood from router-for-me reads as an id-scheme change: Antigravity is
// skipped for the run, the rest proceeds.
func TestAntigravityAnomalyGuard(t *testing.T) {
	registerCompiled(t, "watch-native", "watchmodel-4-6")
	fresh := append(agySpecs(3*antigravityAddCap+1), spec("watch-native", "watchmodel-5"))
	res := compareCatalogs(fresh, "", triggerCurated)
	if !strings.Contains(res.Anomaly, antigravityProvider) {
		t.Fatalf("anomaly = %q", res.Anomaly)
	}
	if len(res.Qualifying) != 1 || res.Qualifying[0] != "watch-native/watchmodel-5" || len(res.Deferred) != 0 {
		t.Fatalf("qualifying = %v deferred = %v", res.Qualifying, res.Deferred)
	}
	if !res.OpenPR || res.OverlayOnly {
		t.Fatalf("open_pr=%v overlay_only=%v", res.OpenPR, res.OverlayOnly)
	}
}
