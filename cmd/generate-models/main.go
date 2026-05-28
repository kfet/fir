// Ported from: packages/ai/scripts/generate-models.ts
// Upstream hash: 48aa882
//
// Command generate-models fetches model data from external APIs and generates
// pkg/ai/models_generated.go. It is a Go port of scripts/generate-models.ts.
//
// When generate-models.ts changes upstream:
//  1. Apply equivalent logic changes to this file (cmd/generate-models/main.go)
//  2. Run: make generate-models
//  3. Verify: go build ./... && go test ./pkg/ai/...
//  4. Update the upstream hash above and in sync/.baseline-hashes
//
// Usage:
//
//	go run ./cmd/generate-models/ [-out pkg/ai/models_generated.go]
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

// modelSpec is the internal representation of a model used during generation.
type modelSpec struct {
	ID             string
	Name           string
	API            string
	Provider       string
	BaseURL        string
	Reasoning      bool
	Input          []string // "text", "image"
	CostInput      float64
	CostOutput     float64
	CostCacheRead  float64
	CostCacheWrite float64
	ContextWindow  int
	MaxTokens      int
	Headers        map[string]string
	Compat         *compatSpec
	ServerTools    []string // "web_search", "web_fetch", "code_execution"
	Compaction     bool
	SWEScore       float64 // best known SWE-bench Verified score (0–100 %)
	SWEInferred    bool    // true when SWEScore is inherited from family, not directly benchmarked

	// ReasoningEffortValues is the allowed enum for reasoning.effort / reasoning_effort,
	// when advertised by the upstream catalog (e.g. Poe's parameters[].schema.enum).
	// Empty means "no known restriction".
	ReasoningEffortValues []string
}

// compatSpec represents OpenAICompletionsCompat fields used in models.
type compatSpec struct {
	SupportsStore                               *bool
	SupportsDeveloperRole                       *bool
	SupportsReasoningEffort                     *bool
	ThinkingFormat                              string
	ZaiToolStream                               *bool
	RequiresReasoningContentOnAssistantMessages *bool
	ReasoningEffortMap                          map[string]string
}

// boolPtr returns a pointer to b.
func boolPtr(b bool) *bool { return &b }

// --- models.dev types ---

type modelsDevModel struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	ToolCall  bool   `json:"tool_call"`
	Reasoning bool   `json:"reasoning"`
	Status    string `json:"status"`
	Limit     struct {
		Context int `json:"context"`
		Output  int `json:"output"`
	} `json:"limit"`
	Cost struct {
		Input      float64 `json:"input"`
		Output     float64 `json:"output"`
		CacheRead  float64 `json:"cache_read"`
		CacheWrite float64 `json:"cache_write"`
	} `json:"cost"`
	Modalities struct {
		Input []string `json:"input"`
	} `json:"modalities"`
	Provider struct {
		NPM string `json:"npm"`
	} `json:"provider"`
}

type modelsDevProvider struct {
	Models map[string]modelsDevModel `json:"models"`
}

type modelsDevData map[string]modelsDevProvider

// --- OpenRouter types ---

type openRouterModel struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Architecture struct {
		Modality string `json:"modality"`
	} `json:"architecture"`
	Pricing struct {
		Prompt          string `json:"prompt"`
		Completion      string `json:"completion"`
		InputCacheRead  string `json:"input_cache_read"`
		InputCacheWrite string `json:"input_cache_write"`
	} `json:"pricing"`
	ContextLength int `json:"context_length"`
	TopProvider   struct {
		MaxCompletionTokens int `json:"max_completion_tokens"`
	} `json:"top_provider"`
	SupportedParameters []string `json:"supported_parameters"`
}

type openRouterResponse struct {
	Data []openRouterModel `json:"data"`
}

// --- Vercel AI Gateway types ---

type aiGatewayModel struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	ContextWindow int      `json:"context_window"`
	MaxTokens     int      `json:"max_tokens"`
	Tags          []string `json:"tags"`
	Pricing       struct {
		Input           any `json:"input"`
		Output          any `json:"output"`
		InputCacheRead  any `json:"input_cache_read"`
		InputCacheWrite any `json:"input_cache_write"`
	} `json:"pricing"`
}

type aiGatewayResponse struct {
	Data []aiGatewayModel `json:"data"`
}

// --- HTTP helper ---

func fetchJSON(url string, target any) error {
	resp, err := http.Get(url) //nolint:gosec // urls are constants
	if err != nil {
		return fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read %s: %w", url, err)
	}
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("unmarshal %s: %w", url, err)
	}
	return nil
}

// toFloat converts a JSON number/string to float64.
func toFloat(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case string:
		var f float64
		fmt.Sscanf(x, "%f", &f)
		return f
	}
	return 0
}

// parseFloat parses a string as float64; returns 0 on error.
func parseFloat(s string) float64 {
	var f float64
	fmt.Sscanf(s, "%f", &f)
	return f
}

func hasString(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

// --- SWE-bench types ---

type sweBenchResult struct {
	Name     string  `json:"name"`
	Resolved float64 `json:"resolved"`
}

type sweBenchLeaderboard struct {
	Name    string           `json:"name"`
	Results []sweBenchResult `json:"results"`
}

type sweBenchData struct {
	Leaderboards []sweBenchLeaderboard `json:"leaderboards"`
}

// swePattern maps a model-ID substring to a canonical model key and baseline score.
type swePattern struct {
	contains string  // substring to match in model ID (case-sensitive)
	modelKey string  // canonical key for live-leaderboard lookup
	score    float64 // curated baseline SWE-bench Verified % (best known)
}

// sweModelPatterns maps model ID substrings to SWE-bench Verified scores.
// These are the industry-standard provider-reported scores from system cards,
// blog posts, and official announcements. Where available, we use Vals AI's
// independent evaluation as a cross-reference.
//
// Sources:
//   - Anthropic system cards: anthropic.com/claude-{model}-system-card
//   - OpenAI announcements: openai.com/index/introducing-{model}
//   - Google model cards: deepmind.google/models/model-cards/{model}
//   - Vals AI independent eval: vals.ai/benchmarks/swebench
//
// Patterns MUST be ordered most-specific to least-specific within each model
// family so that the first match wins (e.g. "claude-opus-4-6" before "claude-opus-4").
//
// init() validates this ordering at startup.
var sweModelPatterns = []swePattern{
	// --- Claude Opus 4.8 ---
	// Source: Anthropic announcement — improved over Opus 4.7; use the latest
	// known Opus score until the public system-card table is wired in.
	{"claude-opus-4-8", "claude-opus-4-8", 80.9},
	{"claude-opus-4.8", "claude-opus-4-8", 80.9},
	// --- Claude Opus 4.7 ---
	// Source: Anthropic announcement / codebase baseline.
	{"claude-opus-4-7", "claude-opus-4-7", 80.9},
	{"claude-opus-4.7", "claude-opus-4-7", 80.9},
	// --- Claude Opus 4.6 ---
	// Source: Anthropic system card — 80.8% SWE-bench Verified
	{"claude-opus-4-6", "claude-opus-4-6", 80.8},
	{"claude-opus-4.6", "claude-opus-4-6", 80.8},
	// --- Claude Opus 4.5 ---
	// Source: Anthropic announcement — 80.9% SWE-bench Verified (custom harness)
	{"claude-opus-4-5", "claude-opus-4-5", 80.9},
	{"claude-opus-4.5", "claude-opus-4-5", 80.9},
	// --- Claude Opus 4.1 ---
	// Source: Anthropic — 73.0% SWE-bench Verified (estimated from agent-assisted runs)
	{"claude-opus-4-1", "claude-opus-4-1", 73.0},
	{"claude-opus-4.1", "claude-opus-4-1", 73.0},
	// --- Claude Opus 4 (base) ---
	// Source: bash-only leaderboard "Claude 4 Opus (20250514)" = 67.6%
	{"claude-opus-4-0", "claude-opus-4-0", 67.6},
	{"claude-opus-4-20250514", "claude-opus-4-0", 67.6},
	{"claude-opus-4", "claude-opus-4-0", 67.6},
	// --- Claude Sonnet 4.6 ---
	// Source: Anthropic system card — 79.6% SWE-bench Verified
	{"claude-sonnet-4-6", "claude-sonnet-4-6", 79.6},
	{"claude-sonnet-4.6", "claude-sonnet-4-6", 79.6},
	// --- Claude Sonnet 4.5 ---
	// Source: Anthropic — 77.2% SWE-bench Verified
	{"claude-sonnet-4-5", "claude-sonnet-4-5", 77.2},
	{"claude-sonnet-4.5", "claude-sonnet-4-5", 77.2},
	// --- Claude Sonnet 4 (base, no sub-version) ---
	// Source: bash-only leaderboard "Claude 4 Sonnet (20250514)" = 64.9%
	{"claude-sonnet-4", "claude-sonnet-4", 64.9},
	// --- Claude Haiku 4.5 ---
	// Source: bash-only leaderboard "Claude 4.5 Haiku (high reasoning)" = 66.6%
	{"claude-haiku-4-5", "claude-haiku-4-5", 66.6},
	{"claude-haiku-4.5", "claude-haiku-4-5", 66.6},
	// --- GPT-5.x (most specific first) ---
	// Source: OpenAI announcement — GPT-5.4 matches GPT-5.3-Codex on SWE-Bench Pro
	// Source: Vals AI independent eval — 77.2% SWE-bench Verified
	{"gpt-5.4-pro", "gpt-5.4-pro", 77.2},
	{"gpt-5.4", "gpt-5.4", 77.2},
	// Source: OpenAI — GPT-5.3-Codex 80.0% SWE-bench Verified (provider-reported)
	{"gpt-5.3-codex", "gpt-5.3-codex", 80.0},
	// Source: bash-only leaderboard "GPT-5-2 Codex" = 72.8%
	{"gpt-5.2-codex", "gpt-5.2-codex", 72.8},
	// Source: bash-only leaderboard "GPT-5-2 (high reasoning)" = 72.8%
	{"gpt-5.2", "gpt-5.2", 72.8},
	// Source: bash-only "GPT-5.1-codex (medium reasoning)" = 66.0%
	{"gpt-5.1-codex-max", "gpt-5.1-codex-max", 67.0},
	{"gpt-5.1-codex", "gpt-5.1-codex", 66.0},
	// Source: bash-only "GPT-5.1 (2025-11-13) (medium reasoning)" = 66.0%
	{"gpt-5.1", "gpt-5.1", 66.0},
	// Source: bash-only "GPT-5 Mini" = 56.2%
	{"gpt-5-mini", "gpt-5-mini", 56.2},
	// --- Gemini 3.x (most specific first) ---
	// Source: Google model card / multiple reviews — 80.6% SWE-bench Verified
	{"gemini-3.1-pro", "gemini-3.1-pro-preview", 80.6},
	// Source: Google announcement — 78% SWE-bench Verified (agentic coding)
	// Source: Vals AI independent eval — 76.2%
	{"gemini-3-flash", "gemini-3-flash-preview", 76.2},
	// Source: Google announcement — 76.2% SWE-bench Verified
	{"gemini-3-pro", "gemini-3-pro-preview", 76.2},
	// --- Gemini 2.x ---
	// Source: Google provider-reported SWE-bench Verified scores
	{"gemini-2.5-pro", "gemini-2.5-pro", 57.6},
	{"gemini-2.5-flash", "gemini-2.5-flash", 47.3},
	{"gemini-2.0-flash", "gemini-2.0-flash", 42.1},
	// --- MiniMax ---
	// Source: bash-only "MiniMax M2.5 (high reasoning)" = 75.8%
	{"minimax-m2.5", "minimax-m2.5", 75.8},
	// --- DeepSeek V3.2 ---
	// Source: bash-only "DeepSeek V3.2 (high reasoning)" = 70.0%
	{"DeepSeek-V3.2", "deepseek-v3.2", 70.0},
	{"deepseek-v3.2", "deepseek-v3.2", 70.0},
	{"deepseek.v3.2", "deepseek-v3.2", 70.0},
	// --- Kimi K2.5 / K2 Thinking ---
	// Source: bash-only
	{"kimi-k2-thinking", "kimi-k2-thinking", 63.4},
	{"kimi-k2.5", "k2p5", 70.8},
	{"k2p5", "k2p5", 70.8},
	// --- Grok 4 ---
	// Source: provider-reported ~72% SWE-bench Verified
	{"grok-4", "grok-4", 72.0},
}

func init() {
	// Validate sweModelPatterns ordering: if pattern A appears before pattern B and
	// B.contains is a substring of A.contains, then B can never match (A always wins).
	// The correct order is most-specific (longest) first.
	for i := 0; i < len(sweModelPatterns); i++ {
		for j := i + 1; j < len(sweModelPatterns); j++ {
			a, b := sweModelPatterns[i], sweModelPatterns[j]
			if strings.Contains(b.contains, a.contains) && a.contains != b.contains {
				log.Fatalf("sweModelPatterns misordered: pattern %d %q is a substring of pattern %d %q — move the more-specific pattern first",
					i, a.contains, j, b.contains)
			}
		}
	}
}

// sweLeaderboardPatterns maps normalised substrings of SWE-bench bash-only
// leaderboard entry names to canonical model keys so that live-fetched scores
// can update baselines.
// Names are normalised by normaliseSWEName before matching: punctuation is stripped,
// runs of whitespace collapsed, so only one entry per model is needed.
// Must be ordered most-specific to least-specific within each family.
var sweLeaderboardPatterns = []struct {
	contains string
	modelKey string
}{
	// Claude — specific versions first
	// bash-only names: "Claude Opus 4.8", "Claude Opus 4.6", "Claude 4.5 Opus (high reasoning)",
	//   "Claude 4.5 Opus medium", "Claude 4 Opus (20250514)",
	//   "Claude 4.5 Sonnet (high reasoning)", "Claude 4.5 Sonnet (20250929)",
	//   "Claude 4 Sonnet (20250514)", "Claude 4.5 Haiku (high reasoning)"
	{"claude opus 48", "claude-opus-4-8"},
	{"claude 48 opus", "claude-opus-4-8"},
	{"claude opus 47", "claude-opus-4-7"},
	{"claude 47 opus", "claude-opus-4-7"},
	{"claude opus 46", "claude-opus-4-6"},
	{"claude 46 opus", "claude-opus-4-6"},
	{"claude 45 opus", "claude-opus-4-5"},
	{"claude opus 45", "claude-opus-4-5"},
	{"claude opus 41", "claude-opus-4-1"},
	{"claude 41 opus", "claude-opus-4-1"},
	{"claude 4 opus", "claude-opus-4-0"},
	{"claude opus 40", "claude-opus-4-0"},
	{"claude 40 opus", "claude-opus-4-0"},
	{"claude sonnet 46", "claude-sonnet-4-6"},
	{"claude 46 sonnet", "claude-sonnet-4-6"},
	{"claude 45 sonnet", "claude-sonnet-4-5"},
	{"claude sonnet 45", "claude-sonnet-4-5"},
	{"claude 45 haiku", "claude-haiku-4-5"},
	{"claude haiku 45", "claude-haiku-4-5"},
	{"claude 4 sonnet", "claude-sonnet-4"},
	// GPT — specific first
	// bash-only names: "GPT-5-2 Codex", "GPT-5-2 (high reasoning)",
	//   "GPT-5.2 (2025-12-11)", "GPT-5.1-codex (medium reasoning)",
	//   "GPT-5.1 (2025-11-13)", "GPT-5 (2025-08-07)", "GPT-5 Mini"
	{"gpt 54 pro", "gpt-5.4-pro"},
	{"gpt 54", "gpt-5.4"},
	{"gpt 53 codex", "gpt-5.3-codex"},
	{"gpt 52 codex", "gpt-5.2-codex"},
	{"gpt 52", "gpt-5.2"},
	{"gpt 51 codex max", "gpt-5.1-codex-max"},
	{"gpt 51 codex", "gpt-5.1-codex"},
	{"gpt 51", "gpt-5.1"},
	{"gpt 5 mini", "gpt-5-mini"},
	// Gemini
	// bash-only names: "Gemini 3 Flash (high reasoning)",
	//   "Gemini 3 Pro Preview (2025-11-18)", "Gemini 3 Pro",
	//   "Gemini 2.5 Pro (2025-05-06)", "Gemini 2.5 Flash", "Gemini 2.0 flash"
	{"gemini 31 pro", "gemini-3.1-pro-preview"},
	{"gemini 3 flash", "gemini-3-flash-preview"},
	{"gemini 3 pro", "gemini-3-pro-preview"},
	{"gemini 25 pro", "gemini-2.5-pro"},
	{"gemini 25 flash", "gemini-2.5-flash"},
	{"gemini 20 flash", "gemini-2.0-flash"},
	// MiniMax
	// bash-only: "MiniMax M2.5 (high reasoning)"
	{"minimax m25", "minimax-m2.5"},
	// DeepSeek
	// bash-only: "DeepSeek V3.2 (high reasoning)"
	{"deepseek v32", "deepseek-v3.2"},
	// Kimi
	// bash-only: "Kimi K2.5 (high reasoning)", "Kimi K2 Thinking"
	{"kimi k25", "k2p5"},
	{"kimi k2 thinking", "kimi-k2-thinking"},
	// Grok
	{"grok 4", "grok-4"},
}

// fetchSWEBenchScores fetches the official SWE-bench "bash-only" leaderboard JSON from
// GitHub. The bash-only leaderboard contains bare-model scores (no agent scaffolding),
// which is what we want for model-level comparisons.
// Returns a map of lowercased entry name → best resolved score.
// On failure it logs a warning and returns nil so generation proceeds without live data.
func fetchSWEBenchScores() map[string]float64 {
	const url = "https://raw.githubusercontent.com/SWE-bench/swe-bench.github.io/master/data/leaderboards.json"

	log.Println("Fetching SWE-bench bash-only leaderboard...")
	var data sweBenchData
	if err := fetchJSON(url, &data); err != nil {
		log.Printf("Warning: SWE-bench fetch failed (curated baseline scores will be used): %v", err)
		return nil
	}
	scores := make(map[string]float64)
	for _, lb := range data.Leaderboards {
		if lb.Name != "bash-only" {
			continue
		}
		for _, r := range lb.Results {
			key := strings.ToLower(r.Name)
			if existing, ok := scores[key]; !ok || r.Resolved > existing {
				scores[key] = r.Resolved
			}
		}
	}
	log.Printf("Loaded %d SWE-bench bash-only leaderboard entries", len(scores))
	return scores
}

// applySWEScores populates SWEScore on every model spec.
//
// Strategy:
//  1. Start with the curated baseline scores embedded in sweModelPatterns.
//  2. If live leaderboard data is available, update baselines for known models
//     when the live score is higher (using sweLeaderboardPatterns for name→key matching).
//  3. Apply the final per-key scores to all model specs whose IDs contain the
//     corresponding pattern substring. The first (most-specific) match wins.
func applySWEScores(all []modelSpec, leaderboard map[string]float64) []modelSpec {
	// Build working copy of patterns so we can update scores without mutating the package var.
	patterns := make([]swePattern, len(sweModelPatterns))
	copy(patterns, sweModelPatterns)

	// Index patterns by modelKey for fast live-score updates.
	keyToIdx := make(map[string]int, len(patterns))
	for i, p := range patterns {
		if _, dup := keyToIdx[p.modelKey]; !dup {
			keyToIdx[p.modelKey] = i
		}
	}

	// Apply live leaderboard scores — only fill in gaps where we have no curated baseline.
	// We do NOT override curated scores with live data because the evaluation harnesses
	// differ (curated = provider-reported SWE-bench Verified; live = bash-only leaderboard).
	if leaderboard != nil {
		liveByKey := make(map[string]float64)
		for entryName, score := range leaderboard {
			key := matchSWELeaderboardName(entryName)
			if key == "" {
				continue
			}
			if existing, ok := liveByKey[key]; !ok || score > existing {
				liveByKey[key] = score
			}
		}
		for key, score := range liveByKey {
			if idx, ok := keyToIdx[key]; ok && patterns[idx].score == 0 {
				// Update every pattern that shares this modelKey.
				for i := range patterns {
					if patterns[i].modelKey == key {
						patterns[i].score = score
					}
				}
			}
		}
	}

	// Apply scores to all model specs — first matching pattern wins.
	for i := range all {
		for _, p := range patterns {
			if strings.Contains(all[i].ID, p.contains) {
				if p.score > all[i].SWEScore {
					all[i].SWEScore = p.score
				}
				break
			}
		}
	}
	return all
}

// normaliseSWEName strips punctuation and collapses whitespace so that
// "claude-opus-4.5", "claude opus 4.5", and "Claude Opus 45" all normalise
// to "claude opus 45".
var sweNameDigitPunctRe = regexp.MustCompile(`(\d)[.\-](\d)`)
var sweNamePunctRe = regexp.MustCompile(`[.\-_]`)
var sweNameSpaceRe = regexp.MustCompile(`\s+`)

func normaliseSWEName(s string) string {
	s = strings.ToLower(s)
	// Remove dots/hyphens between digits: "4.5" → "45", "4-5" → "45"
	s = sweNameDigitPunctRe.ReplaceAllString(s, "${1}${2}")
	// Replace remaining punctuation with spaces
	s = sweNamePunctRe.ReplaceAllString(s, " ")
	s = sweNameSpaceRe.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// matchSWELeaderboardName maps a lowercased leaderboard entry name to the canonical
// model key used in sweModelPatterns. Returns "" if no match is found.
func matchSWELeaderboardName(lower string) string {
	normalised := normaliseSWEName(lower)
	for _, p := range sweLeaderboardPatterns {
		if strings.Contains(normalised, p.contains) {
			return p.modelKey
		}
	}
	return ""
}

// dateSuffixRe matches date suffixes like -20241022 at end of model IDs.
var dateSuffixRe = regexp.MustCompile(`-\d{8}$`)

// extractFamily returns a normalised "base family" string for lineage grouping.
//
// The goal is to group models that are close iterations of each other (same
// generation, same class) while keeping distinct generations apart. We preserve
// the first version-like token as the "generation" to avoid grouping Claude 3
// with Claude 4, or GPT-5.1 with GPT-5.4.
//
// Examples:
//
//	claude-opus-4-6              → claude-opus-4
//	claude-opus-4-5-20251101     → claude-opus-4
//	claude-sonnet-4-5            → claude-sonnet-4
//	claude-3-5-sonnet-20241022   → claude-3-sonnet
//	claude-3-7-sonnet-20250219   → claude-3-sonnet
//	claude-3-opus-20240229       → claude-3-opus
//	gemini-3.1-pro-preview       → gemini-3-pro
//	gemini-2.5-flash             → gemini-2-flash
//	gpt-5.4-pro                  → gpt-5-pro
//	gpt-5.2-codex                → gpt-5-codex
//	gpt-5.2                      → gpt-5
//	deepseek/deepseek-v3.2       → deepseek
//	google/gemini-2.5-pro        → gemini-2-pro
//	minimax-m2.5                 → minimax-m2
//	grok-4-1-fast                → grok-4-fast
//	kimi-k2-thinking             → kimi-k2-thinking
//	k2p5                         → k2p5
func extractFamily(modelID string) string {
	id := modelID

	// Strip OpenRouter-style "provider/" prefix.
	if idx := strings.LastIndex(id, "/"); idx >= 0 {
		id = id[idx+1:]
	}

	// Strip Bedrock-style dotted prefixes: "us.anthropic.claude-…" → "claude-…"
	// We find the FIRST dot followed by a hyphenated model name (contains "-")
	// to avoid picking version dots like "v3.2".
	// Assumption: dotted prefixes always end with a lowercase letter segment
	// (e.g. ".claude", ".deepseek"). This is correct for all known provider
	// naming conventions as of 2026-03.
	for i := 0; i < len(id); i++ {
		if id[i] == '.' {
			after := id[i+1:]
			if len(after) > 0 && after[0] >= 'a' && after[0] <= 'z' && strings.Contains(after, "-") {
				id = after
				i = -1 // restart scan on the remaining string
			}
		}
	}

	// Lowercase for uniform matching.
	id = strings.ToLower(id)

	// Strip Bedrock version suffixes like "-v1:0", ":0" at end.
	if idx := strings.LastIndex(id, ":"); idx >= 0 {
		id = id[:idx]
	}

	// Strip date suffixes: -20241022, -20250929, etc.
	id = dateSuffixRe.ReplaceAllString(id, "")

	// Strip common tags.
	for _, tag := range []string{"-latest", "-preview", "-exp", "-free", "-customtools"} {
		id = strings.ReplaceAll(id, tag, "")
	}

	// Strip trailing dashes left over from tag removal.
	id = strings.TrimRight(id, "-")

	// Normalise embedded version dots in tokens.
	// - Pure version tokens like "v3.2", "5.4" are kept as-is (isVersionToken handles them).
	// - Tokens with alpha prefix like "gemini" stay as-is.
	// - Mixed tokens like "m2.5" get their trailing ".N" stripped → "m2".
	parts := strings.Split(id, "-")
	var expanded []string
	for _, p := range parts {
		if dot := strings.Index(p, "."); dot > 0 && !isVersionToken(p) {
			prefix := p[:dot]
			suffix := p[dot+1:]
			if isAlpha(prefix) && isVersionToken(suffix) {
				// "gemini.5" → split to "gemini" + "5" (alpha prefix + version)
				expanded = append(expanded, prefix, suffix)
				continue
			}
			// "m2.5" → strip sub-version → "m2"
			if isVersionToken(suffix) {
				expanded = append(expanded, prefix)
				continue
			}
		}
		expanded = append(expanded, p)
	}

	// Classify tokens: keep word tokens and the FIRST version token (as
	// the generation marker). Drop subsequent version tokens.
	var family []string
	seenGeneration := false
	for _, p := range expanded {
		if isVersionToken(p) {
			if !seenGeneration {
				// Keep the major part of the first version as the generation.
				// "4.6" → "4", "3.1" → "3", "5.4" → "5", "v3.2" → "3"
				gen := p
				if gen[0] == 'v' {
					gen = gen[1:]
				}
				if dot := strings.Index(gen, "."); dot >= 0 {
					gen = gen[:dot]
				}
				family = append(family, gen)
				seenGeneration = true
			}
			continue
		}
		family = append(family, p)
	}

	if len(family) == 0 {
		return id
	}
	return strings.Join(family, "-")
}

// isAlpha returns true if s is non-empty and contains only lowercase ASCII letters.
func isAlpha(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < 'a' || c > 'z' {
			return false
		}
	}
	return true
}

// isVersionToken returns true if a hyphen-delimited token looks like a version
// number or date: pure digits, dotted digits (3.1, 4.5), "v3", "v3.2", single
// digit, etc.
func isVersionToken(s string) bool {
	if s == "" {
		return false
	}
	// "v3", "v3.2"
	stripped := s
	if stripped[0] == 'v' && len(stripped) > 1 {
		stripped = stripped[1:]
	}
	// Check if all remaining chars are digits or dots.
	for _, c := range stripped {
		if c != '.' && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// inferSWEScores propagates SWE-bench scores to unscored models via lineage.
//
// For each model with SWEScore == 0, it looks up the model's "family" (via
// extractFamily) and assigns familyMaxScore + 0.1, flagging SWEInferred = true.
// This ensures new/unbenched models from strong families surface near the top.
func inferSWEScores(all []modelSpec) []modelSpec {
	// Pass 1: find max actual (non-inferred) score per family.
	familyMax := make(map[string]float64)
	for i := range all {
		if all[i].SWEScore == 0 || all[i].SWEInferred {
			continue
		}
		fam := extractFamily(all[i].ID)
		if all[i].SWEScore > familyMax[fam] {
			familyMax[fam] = all[i].SWEScore
		}
	}

	// Pass 2: assign inferred scores to unscored models.
	inferred := 0
	for i := range all {
		if all[i].SWEScore > 0 {
			continue
		}
		fam := extractFamily(all[i].ID)
		if maxScore, ok := familyMax[fam]; ok {
			all[i].SWEScore = math.Round((maxScore+0.1)*10) / 10
			all[i].SWEInferred = true
			inferred++
		}
	}
	log.Printf("Inferred SWE-bench scores for %d models via lineage inheritance", inferred)
	return all
}

// --- Fetchers ---

const (
	aiGatewayModelsURL = "https://ai-gateway.vercel.sh/v1"
	aiGatewayBaseURL   = "https://ai-gateway.vercel.sh"
)

func fetchOpenRouterModels() ([]modelSpec, error) {
	log.Println("Fetching models from OpenRouter API...")
	var resp openRouterResponse
	if err := fetchJSON("https://openrouter.ai/api/v1/models", &resp); err != nil {
		return nil, err
	}

	var models []modelSpec
	for _, m := range resp.Data {
		if !hasString(m.SupportedParameters, "tools") {
			continue
		}

		input := []string{"text"}
		if strings.Contains(m.Architecture.Modality, "image") {
			input = append(input, "image")
		}

		// Convert pricing from $/token to $/million tokens
		models = append(models, modelSpec{
			ID:             m.ID,
			Name:           m.Name,
			API:            "openai-completions",
			BaseURL:        "https://openrouter.ai/api/v1",
			Provider:       "openrouter",
			Reasoning:      hasString(m.SupportedParameters, "reasoning"),
			Input:          input,
			CostInput:      parseFloat(m.Pricing.Prompt) * 1_000_000,
			CostOutput:     parseFloat(m.Pricing.Completion) * 1_000_000,
			CostCacheRead:  parseFloat(m.Pricing.InputCacheRead) * 1_000_000,
			CostCacheWrite: parseFloat(m.Pricing.InputCacheWrite) * 1_000_000,
			ContextWindow:  intOr(m.ContextLength, 4096),
			MaxTokens:      intOr(m.TopProvider.MaxCompletionTokens, 4096),
		})
	}
	log.Printf("Fetched %d tool-capable models from OpenRouter", len(models))
	return models, nil
}

func fetchAIGatewayModels() ([]modelSpec, error) {
	log.Println("Fetching models from Vercel AI Gateway API...")
	var resp aiGatewayResponse
	if err := fetchJSON(aiGatewayModelsURL+"/models", &resp); err != nil {
		return nil, err
	}

	var models []modelSpec
	for _, m := range resp.Data {
		if !hasString(m.Tags, "tool-use") {
			continue
		}

		input := []string{"text"}
		if hasString(m.Tags, "vision") {
			input = append(input, "image")
		}

		models = append(models, modelSpec{
			ID:             m.ID,
			Name:           stringOr(m.Name, m.ID),
			API:            "anthropic-messages",
			BaseURL:        aiGatewayBaseURL,
			Provider:       "vercel-ai-gateway",
			Reasoning:      hasString(m.Tags, "reasoning"),
			Input:          input,
			CostInput:      toFloat(m.Pricing.Input) * 1_000_000,
			CostOutput:     toFloat(m.Pricing.Output) * 1_000_000,
			CostCacheRead:  toFloat(m.Pricing.InputCacheRead) * 1_000_000,
			CostCacheWrite: toFloat(m.Pricing.InputCacheWrite) * 1_000_000,
			ContextWindow:  intOr(m.ContextWindow, 4096),
			MaxTokens:      intOr(m.MaxTokens, 4096),
		})
	}
	log.Printf("Fetched %d tool-capable models from Vercel AI Gateway", len(models))
	return models, nil
}

// --- Poe (poe.com, OpenAI-compatible) ---

type poeModelResponse struct {
	Data []poeModel `json:"data"`
}

type poeModel struct {
	ID           string `json:"id"`
	Description  string `json:"description"`
	OwnedBy      string `json:"owned_by"`
	Architecture struct {
		InputModalities []string `json:"input_modalities"`
	} `json:"architecture"`
	SupportedFeatures  []string `json:"supported_features"`
	SupportedEndpoints []string `json:"supported_endpoints"`
	Pricing            struct {
		Prompt          string `json:"prompt"`
		Completion      string `json:"completion"`
		InputCacheRead  string `json:"input_cache_read"`
		InputCacheWrite string `json:"input_cache_write"`
	} `json:"pricing"`
	ContextWindow struct {
		ContextLength   int `json:"context_length"`
		MaxOutputTokens int `json:"max_output_tokens"`
	} `json:"context_window"`
	ContextLength int `json:"context_length"`
	Reasoning     struct {
		Required                bool `json:"required"`
		SupportsReasoningEffort bool `json:"supports_reasoning_effort"`
	} `json:"reasoning"`
	Metadata struct {
		DisplayName string `json:"display_name"`
	} `json:"metadata"`
	Parameters []struct {
		Name   string `json:"name"`
		Schema struct {
			Maximum float64  `json:"maximum"`
			Enum    []string `json:"enum"`
		} `json:"schema"`
	} `json:"parameters"`
}

// poeContextDescRe captures context-window hints from free-text model
// descriptions. Poe leaves the structured context_window field null for many
// third-party bots but usually mentions the size in the description. Examples
// handled: "Context Window: 256k", "Context Length: 131k tokens",
// "context window of 128,000 tokens", "1 million token context window",
// "256k context".
//
// The regex has two alternatives:
//   - "context ... NUMBER[unit]"  (keyword first)
//   - "NUMBER[unit] ... context"  (number first)
var poeContextDescRe = regexp.MustCompile(
	`(?i)` +
		// keyword-first: "Context Window: 256k", "Context Length: 131k"
		`(?:context[ _-]*(?:window|length|size)[^\d]{0,40}([\d,]+(?:\.\d+)?)\s*(k|m|million|thousand|tokens?)?` +
		// number-first with optional "tokens" and then "context[s]":
		//   "256k context", "1 million token context window", "1 million tokens) ... context"
		`|([\d,]+(?:\.\d+)?)\s*(k|m|million|thousand)?[- ]?(?:tokens?[^.]{0,40})?contexts?` +
		// fallback: "NUMBER[unit] tokens" anywhere inside a (context|supports|handle|window) phrase
		`|(?:context|supports|handle|window|long)[^\d.]{0,40}([\d,]+(?:\.\d+)?)\s*(k|m|million|thousand)\s*tokens?` +
		`)`)

// parsePoeContextFromDesc returns the largest context-window size (in tokens)
// mentioned in a free-text description, or 0 if none is found.
func parsePoeContextFromDesc(desc string) int {
	if desc == "" {
		return 0
	}
	best := 0
	for _, match := range poeContextDescRe.FindAllStringSubmatch(desc, -1) {
		// Either group 1/2 (keyword-first) or 3/4 (number-first) will match.
		numStr := match[1]
		unit := match[2]
		if numStr == "" {
			numStr = match[3]
			unit = match[4]
		}
		if numStr == "" {
			numStr = match[5]
			unit = match[6]
		}
		numStr = strings.ReplaceAll(numStr, ",", "")
		unit = strings.ToLower(unit)
		n := parseFloat(numStr)
		if n <= 0 {
			continue
		}
		switch unit {
		case "k", "thousand":
			n *= 1_000
		case "m", "million":
			n *= 1_000_000
		}
		tokens := int(n)
		// Sanity: ignore absurdly small (<1024) or huge (>4M) matches —
		// those almost certainly matched something unrelated or a typo
		// (e.g. magistral's description says "40,000k" meaning 40k).
		if tokens < 1024 || tokens > 4_000_000 {
			continue
		}
		if tokens > best {
			best = tokens
		}
	}
	return best
}

// poeContextOverrides hardcodes context-window sizes for Poe bots whose
// metadata is unreliable or missing. Prefer fixing the scraper over adding
// entries here — this map is only for cases the API genuinely gets wrong.
var poeContextOverrides = map[string]int{
	// Poe description says "Context Window: 40,000k" which is a typo for
	// 40k tokens. Mistral's docs confirm 40,000 (40k) for Magistral Medium.
	"magistral-medium-2509-thinking": 40_000,
	// Poe reports kimi-k2.5 context_length=128000, but Moonshot's official
	// Kimi K2.5 / K2 Thinking spec is 256K. Poe's own max_output_tokens
	// parameter even allows 262144, confirming the lower number is wrong.
	// Apply to the base model and FW (Fireworks-hosted) sibling so both
	// surface the correct window.
	"kimi-k2.5":    262144,
	"kimi-k2.5-fw": 262144,
	// Poe doesn't report a context window for glm-5 / glm-5.1 bots; the
	// fallback uses max_output_tokens (131072) which understates the real
	// 200K window documented by Z.ai (matches OpenRouter / Vercel entries
	// at 202752/202800).
	"glm-5":      202752,
	"glm-5.1-fw": 202752,
}

// poeSiblingCtx returns the context size of a sibling Poe bot (same model
// under a different host) so `-fw` / `-tog` / `-t` variants can inherit from
// the canonical entry when their own metadata is missing. Matches in order:
//  1. exact base (e.g. `kimi-k2.5-fw` → `kimi-k2.5`)
//  2. any known model whose ID starts with the base (e.g. `qwen3.5-397b-fw`
//     → `qwen3.5-397b-a17b`)
//  3. any known model that shares the longest ID prefix of at least 5 chars
//     (e.g. `glm-5.1-fw` → `glm-5`)
func poeSiblingCtx(id string, known map[string]int) int {
	const suffixes = "-fw -tog -t -n"
	var base string
	for _, suf := range strings.Fields(suffixes) {
		if strings.HasSuffix(id, suf) {
			base = strings.TrimSuffix(id, suf)
			break
		}
	}
	if base == "" {
		return 0
	}
	// 1. exact match
	if ctx, ok := known[base]; ok && ctx > 0 {
		return ctx
	}
	// 2. prefix match: any known ID that starts with the base
	for k, v := range known {
		if k == id || v <= 0 {
			continue
		}
		if strings.HasPrefix(k, base+"-") {
			return v
		}
	}
	// 3. longest-shared-prefix match (at least 5 chars shared).
	best, bestLen := 0, 0
	for k, v := range known {
		if v <= 0 || k == id {
			continue
		}
		n := commonPrefixLen(k, base)
		if n >= 5 && n > bestLen {
			best, bestLen = v, n
		}
	}
	return best
}

// commonPrefixLen returns the length of the common prefix of a and b.
func commonPrefixLen(a, b string) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

// fetchPoeModels fetches the model catalog from Poe's public OpenAI-compatible
// endpoint. Poe's /v1/models requires no auth and returns every bot accessible
// via the chat/completions endpoint.
func fetchPoeModels() ([]modelSpec, error) {
	log.Println("Fetching models from Poe API...")
	var resp poeModelResponse
	if err := fetchJSON("https://api.poe.com/v1/models", &resp); err != nil {
		return nil, err
	}

	var models []modelSpec
	for _, m := range resp.Data {
		// Require tool support so the model works with fir's agent loop.
		if !hasString(m.SupportedFeatures, "tools") {
			continue
		}
		// Surface models that advertise /v1/chat/completions or
		// /v1/responses, plus those with an empty supported_endpoints
		// list (Poe's metadata is incomplete for many third-party
		// bots — e.g. the Kimi K2.5/K2.6, GLM, Qwen, Minimax, Seed,
		// DeepSeek families — but they are still reachable via
		// /v1/chat/completions). Models that explicitly restrict
		// themselves to non-chat, non-responses endpoints (/v1/videos,
		// /v1/images, etc.) are excluded.
		eps := m.SupportedEndpoints
		supportsChat := len(eps) == 0 || hasString(eps, "/v1/chat/completions")
		supportsResponses := hasString(eps, "/v1/responses")
		if !supportsChat && !supportsResponses {
			continue
		}
		// Prefer /v1/chat/completions when available (widely supported,
		// well-tested). Fall back to /v1/responses when the bot doesn't
		// expose /v1/chat/completions (e.g. gpt-5.3-codex-spark).
		api := "openai-completions"
		if !supportsChat && supportsResponses {
			api = "openai-responses"
		}

		input := []string{"text"}
		if hasString(m.Architecture.InputModalities, "image") {
			input = append(input, "image")
		}

		ctxLen := m.ContextWindow.ContextLength
		if ctxLen == 0 {
			ctxLen = m.ContextLength
		}
		maxOut := m.ContextWindow.MaxOutputTokens
		// Fall back to the max_output_tokens parameter's upper bound when
		// Poe doesn't fill in the structured context_window fields. This
		// is the only context-size signal available for many third-party
		// bots (e.g. kimi-k2.6 advertises 262144 here).
		paramMax := 0
		for _, p := range m.Parameters {
			if p.Name == "max_output_tokens" && p.Schema.Maximum > 0 {
				paramMax = int(p.Schema.Maximum)
				break
			}
		}
		if ctxLen == 0 && paramMax > 0 {
			ctxLen = paramMax
		}
		// Last-resort: scrape context size from the free-text description
		// (many third-party Poe bots only mention it there).
		if ctxLen == 0 {
			if descCtx := parsePoeContextFromDesc(m.Description); descCtx > 0 {
				ctxLen = descCtx
			}
		}
		// Hardcoded overrides take precedence over everything: used when
		// Poe's own metadata is wrong (see poeContextOverrides).
		if override, ok := poeContextOverrides[m.ID]; ok {
			ctxLen = override
		}
		if maxOut == 0 && paramMax > 0 {
			maxOut = paramMax
		}
		if maxOut == 0 {
			maxOut = 8192
		}
		// MaxTokens must never exceed the context window.
		if ctxLen > 0 && maxOut > ctxLen {
			maxOut = ctxLen
		}

		name := m.Metadata.DisplayName
		if name == "" {
			name = m.ID
		}

		// Route Claude-family bots through Anthropic's native
		// /v1/messages when Poe advertises it. This preserves native
		// thinking blocks and tool-use semantics that would otherwise be
		// flattened by Poe's OpenAI translation. /v1/messages is only
		// reliable for Claude on Poe — non-Claude models return a 200
		// error envelope when routed through it.
		//
		// BaseURL differs by API: the Anthropic handler appends
		// "/v1/messages" itself (so base must be bare host), while the
		// OpenAI chat-completions/responses handlers append
		// "/chat/completions" or "/responses" (so base must already
		// include "/v1").
		baseURL := "https://api.poe.com/v1"
		if strings.HasPrefix(m.ID, "claude-") && hasString(m.SupportedEndpoints, "/v1/messages") {
			api = "anthropic-messages"
			baseURL = "https://api.poe.com"
		}

		// Pick up any bot-advertised reasoning effort enum. Poe exposes
		// per-bot `parameters[].schema.enum` values under names like
		// "reasoning_effort", "effort", or "thinking_level". Only bots
		// with a recognisably OpenAI-style effort enum (values drawn from
		// {none,minimal,low,medium,high,xhigh,extra-high,max}) are
		// captured here — boolean-style thinking toggles (enable_thinking,
		// deep_thinking) and Anthropic "output_effort" knobs are handled
		// elsewhere.
		var effortValues []string
		for _, p := range m.Parameters {
			name := strings.ToLower(p.Name)
			if name != "reasoning_effort" && name != "effort" && name != "thinking_level" {
				continue
			}
			if len(p.Schema.Enum) == 0 {
				continue
			}
			effortValues = append([]string(nil), p.Schema.Enum...)
			break
		}
		// Hardcoded override for Poe's "assistant" router bot: its
		// /v1/models entry advertises supports_reasoning_effort=true but
		// ships an empty parameters[]. Empirically the backing model
		// (gpt-5.2-chat-latest) only accepts "medium" — any other value
		// 400s. Upstream Poe bug; safe to remove once they populate the
		// field.
		if m.ID == "assistant" && len(effortValues) == 0 && m.Reasoning.SupportsReasoningEffort {
			effortValues = []string{"medium"}
		}

		models = append(models, modelSpec{
			ID:                    m.ID,
			Name:                  name,
			API:                   api,
			BaseURL:               baseURL,
			Provider:              "poe",
			Reasoning:             m.Reasoning.Required || m.Reasoning.SupportsReasoningEffort,
			Input:                 input,
			CostInput:             parseFloat(m.Pricing.Prompt) * 1_000_000,
			CostOutput:            parseFloat(m.Pricing.Completion) * 1_000_000,
			CostCacheRead:         parseFloat(m.Pricing.InputCacheRead) * 1_000_000,
			CostCacheWrite:        parseFloat(m.Pricing.InputCacheWrite) * 1_000_000,
			ContextWindow:         intOr(ctxLen, 4096),
			MaxTokens:             intOr(maxOut, 4096),
			ReasoningEffortValues: effortValues,
		})
	}
	// Second pass: for bots whose context size is still missing, inherit
	// from a canonical sibling (e.g. `kimi-k2.5-fw` inherits from `kimi-k2.5`).
	// This recovers Fireworks/Together-hosted mirror bots that ship with
	// blank descriptions.
	known := make(map[string]int, len(models))
	for _, mm := range models {
		// 4096 is our "unknown" default; don't propagate it as a sibling signal.
		if mm.ContextWindow == 4096 {
			continue
		}
		known[mm.ID] = mm.ContextWindow
	}
	for i := range models {
		if models[i].ContextWindow > 0 && models[i].ContextWindow != 4096 {
			continue
		}
		if sib := poeSiblingCtx(models[i].ID, known); sib > 0 {
			models[i].ContextWindow = sib
			if models[i].MaxTokens == 0 || models[i].MaxTokens == 4096 || models[i].MaxTokens > sib {
				models[i].MaxTokens = sib
			}
		}
	}
	log.Printf("Fetched %d tool-capable models from Poe", len(models))
	return models, nil
}

var copilotClaude4Re = regexp.MustCompile(`^claude-(haiku|sonnet|opus)-4([.\-]|$)`)

func loadModelsDevData() ([]modelSpec, error) {
	log.Println("Fetching models from models.dev API...")
	var data modelsDevData
	if err := fetchJSON("https://models.dev/api.json", &data); err != nil {
		return nil, err
	}

	var models []modelSpec

	// --- Amazon Bedrock ---
	if p, ok := data["amazon-bedrock"]; ok {
		for id, m := range p.Models {
			if !m.ToolCall {
				continue
			}
			if strings.HasPrefix(id, "ai21.jamba") {
				continue // no streaming tool support
			}
			if strings.HasPrefix(id, "mistral.mistral-7b-instruct-v0") {
				continue // no system message support
			}
			input := []string{"text"}
			if hasString(m.Modalities.Input, "image") {
				input = append(input, "image")
			}
			models = append(models, modelSpec{
				ID:             id,
				Name:           stringOr(m.Name, id),
				API:            "bedrock-converse-stream",
				Provider:       "amazon-bedrock",
				BaseURL:        "https://bedrock-runtime.us-east-1.amazonaws.com",
				Reasoning:      m.Reasoning,
				Input:          input,
				CostInput:      m.Cost.Input,
				CostOutput:     m.Cost.Output,
				CostCacheRead:  m.Cost.CacheRead,
				CostCacheWrite: m.Cost.CacheWrite,
				ContextWindow:  intOr(m.Limit.Context, 4096),
				MaxTokens:      intOr(m.Limit.Output, 4096),
			})
		}
	}

	// --- Anthropic ---
	if p, ok := data["anthropic"]; ok {
		for id, m := range p.Models {
			if !m.ToolCall {
				continue
			}
			input := []string{"text"}
			if hasString(m.Modalities.Input, "image") {
				input = append(input, "image")
			}
			models = append(models, modelSpec{
				ID:             id,
				Name:           stringOr(m.Name, id),
				API:            "anthropic-messages",
				Provider:       "anthropic",
				BaseURL:        "https://api.anthropic.com",
				Reasoning:      m.Reasoning,
				Input:          input,
				CostInput:      m.Cost.Input,
				CostOutput:     m.Cost.Output,
				CostCacheRead:  m.Cost.CacheRead,
				CostCacheWrite: m.Cost.CacheWrite,
				ContextWindow:  intOr(m.Limit.Context, 4096),
				MaxTokens:      intOr(m.Limit.Output, 4096),
			})
		}
	}

	// --- Google ---
	if p, ok := data["google"]; ok {
		for id, m := range p.Models {
			if !m.ToolCall {
				continue
			}
			input := []string{"text"}
			if hasString(m.Modalities.Input, "image") {
				input = append(input, "image")
			}
			models = append(models, modelSpec{
				ID:             id,
				Name:           stringOr(m.Name, id),
				API:            "google-generative-ai",
				Provider:       "google",
				BaseURL:        "https://generativelanguage.googleapis.com/v1beta",
				Reasoning:      m.Reasoning,
				Input:          input,
				CostInput:      m.Cost.Input,
				CostOutput:     m.Cost.Output,
				CostCacheRead:  m.Cost.CacheRead,
				CostCacheWrite: m.Cost.CacheWrite,
				ContextWindow:  intOr(m.Limit.Context, 4096),
				MaxTokens:      intOr(m.Limit.Output, 4096),
			})
		}
	}

	// --- OpenAI ---
	if p, ok := data["openai"]; ok {
		for id, m := range p.Models {
			if !m.ToolCall {
				continue
			}
			input := []string{"text"}
			if hasString(m.Modalities.Input, "image") {
				input = append(input, "image")
			}
			models = append(models, modelSpec{
				ID:             id,
				Name:           stringOr(m.Name, id),
				API:            "openai-responses",
				Provider:       "openai",
				BaseURL:        "https://api.openai.com/v1",
				Reasoning:      m.Reasoning,
				Input:          input,
				CostInput:      m.Cost.Input,
				CostOutput:     m.Cost.Output,
				CostCacheRead:  m.Cost.CacheRead,
				CostCacheWrite: m.Cost.CacheWrite,
				ContextWindow:  intOr(m.Limit.Context, 4096),
				MaxTokens:      intOr(m.Limit.Output, 4096),
			})
		}
	}

	// --- Groq ---
	if p, ok := data["groq"]; ok {
		for id, m := range p.Models {
			if !m.ToolCall {
				continue
			}
			input := []string{"text"}
			if hasString(m.Modalities.Input, "image") {
				input = append(input, "image")
			}
			models = append(models, modelSpec{
				ID:             id,
				Name:           stringOr(m.Name, id),
				API:            "openai-completions",
				Provider:       "groq",
				BaseURL:        "https://api.groq.com/openai/v1",
				Reasoning:      m.Reasoning,
				Input:          input,
				CostInput:      m.Cost.Input,
				CostOutput:     m.Cost.Output,
				CostCacheRead:  m.Cost.CacheRead,
				CostCacheWrite: m.Cost.CacheWrite,
				ContextWindow:  intOr(m.Limit.Context, 4096),
				MaxTokens:      intOr(m.Limit.Output, 4096),
			})
		}
	}

	// --- Cerebras ---
	if p, ok := data["cerebras"]; ok {
		for id, m := range p.Models {
			if !m.ToolCall {
				continue
			}
			input := []string{"text"}
			if hasString(m.Modalities.Input, "image") {
				input = append(input, "image")
			}
			models = append(models, modelSpec{
				ID:             id,
				Name:           stringOr(m.Name, id),
				API:            "openai-completions",
				Provider:       "cerebras",
				BaseURL:        "https://api.cerebras.ai/v1",
				Reasoning:      m.Reasoning,
				Input:          input,
				CostInput:      m.Cost.Input,
				CostOutput:     m.Cost.Output,
				CostCacheRead:  m.Cost.CacheRead,
				CostCacheWrite: m.Cost.CacheWrite,
				ContextWindow:  intOr(m.Limit.Context, 4096),
				MaxTokens:      intOr(m.Limit.Output, 4096),
			})
		}
	}

	// --- xAI ---
	if p, ok := data["xai"]; ok {
		for id, m := range p.Models {
			if !m.ToolCall {
				continue
			}
			input := []string{"text"}
			if hasString(m.Modalities.Input, "image") {
				input = append(input, "image")
			}
			models = append(models, modelSpec{
				ID:             id,
				Name:           stringOr(m.Name, id),
				API:            "openai-completions",
				Provider:       "xai",
				BaseURL:        "https://api.x.ai/v1",
				Reasoning:      m.Reasoning,
				Input:          input,
				CostInput:      m.Cost.Input,
				CostOutput:     m.Cost.Output,
				CostCacheRead:  m.Cost.CacheRead,
				CostCacheWrite: m.Cost.CacheWrite,
				ContextWindow:  intOr(m.Limit.Context, 4096),
				MaxTokens:      intOr(m.Limit.Output, 4096),
			})
		}
	}

	// --- ZAI (coding plan) ---
	// Upstream now reads from the "zai-coding-plan" key on models.dev. Fall back
	// to legacy "zai" for older snapshots.
	zaiSrc, zaiOk := data["zai-coding-plan"]
	if !zaiOk {
		zaiSrc, zaiOk = data["zai"]
	}
	// Models that do NOT support top-level tool_stream streaming.
	zaiToolStreamUnsupported := map[string]bool{
		"glm-4.5":       true,
		"glm-4.5-air":   true,
		"glm-4.5-flash": true,
		"glm-4.5v":      true,
	}
	if zaiOk {
		for id, m := range zaiSrc.Models {
			if !m.ToolCall {
				continue
			}
			input := []string{"text"}
			if hasString(m.Modalities.Input, "image") {
				input = append(input, "image")
			}
			compat := &compatSpec{
				SupportsDeveloperRole: boolPtr(false),
				ThinkingFormat:        "zai",
			}
			if !zaiToolStreamUnsupported[id] {
				compat.ZaiToolStream = boolPtr(true)
			}
			models = append(models, modelSpec{
				ID:             id,
				Name:           stringOr(m.Name, id),
				API:            "openai-completions",
				Provider:       "zai",
				BaseURL:        "https://api.z.ai/api/coding/paas/v4",
				Reasoning:      m.Reasoning,
				Input:          input,
				CostInput:      m.Cost.Input,
				CostOutput:     m.Cost.Output,
				CostCacheRead:  m.Cost.CacheRead,
				CostCacheWrite: m.Cost.CacheWrite,
				ContextWindow:  intOr(m.Limit.Context, 4096),
				MaxTokens:      intOr(m.Limit.Output, 4096),
				Compat:         compat,
			})
		}
	}

	// --- Mistral ---
	if p, ok := data["mistral"]; ok {
		for id, m := range p.Models {
			if !m.ToolCall {
				continue
			}
			input := []string{"text"}
			if hasString(m.Modalities.Input, "image") {
				input = append(input, "image")
			}
			models = append(models, modelSpec{
				ID:             id,
				Name:           stringOr(m.Name, id),
				API:            "mistral-conversations",
				Provider:       "mistral",
				BaseURL:        "https://api.mistral.ai",
				Reasoning:      m.Reasoning,
				Input:          input,
				CostInput:      m.Cost.Input,
				CostOutput:     m.Cost.Output,
				CostCacheRead:  m.Cost.CacheRead,
				CostCacheWrite: m.Cost.CacheWrite,
				ContextWindow:  intOr(m.Limit.Context, 4096),
				MaxTokens:      intOr(m.Limit.Output, 4096),
			})
		}
	}

	// --- Hugging Face ---
	if p, ok := data["huggingface"]; ok {
		for id, m := range p.Models {
			if !m.ToolCall {
				continue
			}
			input := []string{"text"}
			if hasString(m.Modalities.Input, "image") {
				input = append(input, "image")
			}
			models = append(models, modelSpec{
				ID:             id,
				Name:           stringOr(m.Name, id),
				API:            "openai-completions",
				Provider:       "huggingface",
				BaseURL:        "https://router.huggingface.co/v1",
				Reasoning:      m.Reasoning,
				Input:          input,
				CostInput:      m.Cost.Input,
				CostOutput:     m.Cost.Output,
				CostCacheRead:  m.Cost.CacheRead,
				CostCacheWrite: m.Cost.CacheWrite,
				ContextWindow:  intOr(m.Limit.Context, 4096),
				MaxTokens:      intOr(m.Limit.Output, 4096),
				Compat: &compatSpec{
					SupportsDeveloperRole: boolPtr(false),
				},
			})
		}
	}

	// --- OpenCode Zen (Zen and Go) ---
	opencodeVariants := []struct {
		key      string
		provider string
		basePath string
	}{
		{"opencode", "opencode", "https://opencode.ai/zen"},
		{"opencode-go", "opencode-go", "https://opencode.ai/zen/go"},
	}
	for _, variant := range opencodeVariants {
		p, ok := data[variant.key]
		if !ok {
			continue
		}
		for id, m := range p.Models {
			if !m.ToolCall {
				continue
			}
			if m.Status == "deprecated" {
				continue
			}
			input := []string{"text"}
			if hasString(m.Modalities.Input, "image") {
				input = append(input, "image")
			}
			var api, baseURL string
			switch m.Provider.NPM {
			case "@ai-sdk/openai":
				api = "openai-responses"
				baseURL = variant.basePath + "/v1"
			case "@ai-sdk/anthropic":
				api = "anthropic-messages"
				baseURL = variant.basePath
			case "@ai-sdk/google":
				api = "google-generative-ai"
				baseURL = variant.basePath + "/v1"
			default:
				api = "openai-completions"
				baseURL = variant.basePath + "/v1"
			}
			models = append(models, modelSpec{
				ID:             id,
				Name:           stringOr(m.Name, id),
				API:            api,
				Provider:       variant.provider,
				BaseURL:        baseURL,
				Reasoning:      m.Reasoning,
				Input:          input,
				CostInput:      m.Cost.Input,
				CostOutput:     m.Cost.Output,
				CostCacheRead:  m.Cost.CacheRead,
				CostCacheWrite: m.Cost.CacheWrite,
				ContextWindow:  intOr(m.Limit.Context, 4096),
				MaxTokens:      intOr(m.Limit.Output, 4096),
			})
		}
	}

	// --- GitHub Copilot ---
	if p, ok := data["github-copilot"]; ok {
		for id, m := range p.Models {
			if !m.ToolCall {
				continue
			}
			if m.Status == "deprecated" {
				continue
			}
			input := []string{"text"}
			if hasString(m.Modalities.Input, "image") {
				input = append(input, "image")
			}

			isCopilotClaude4 := copilotClaude4Re.MatchString(id)
			needsResponsesAPI := strings.HasPrefix(id, "gpt-5") || strings.HasPrefix(id, "oswe")

			var api string
			var compat *compatSpec
			switch {
			case isCopilotClaude4:
				api = "anthropic-messages"
			case needsResponsesAPI:
				api = "openai-responses"
			default:
				api = "openai-completions"
				compat = &compatSpec{
					SupportsStore:           boolPtr(false),
					SupportsDeveloperRole:   boolPtr(false),
					SupportsReasoningEffort: boolPtr(false),
				}
			}

			models = append(models, modelSpec{
				ID:             id,
				Name:           stringOr(m.Name, id),
				API:            api,
				Provider:       "github-copilot",
				BaseURL:        "https://api.individual.githubcopilot.com",
				Reasoning:      m.Reasoning,
				Input:          input,
				CostInput:      m.Cost.Input,
				CostOutput:     m.Cost.Output,
				CostCacheRead:  m.Cost.CacheRead,
				CostCacheWrite: m.Cost.CacheWrite,
				ContextWindow:  intOr(m.Limit.Context, 128000),
				MaxTokens:      intOr(m.Limit.Output, 8192),
				Compat:         compat,
			})
		}
	}

	// --- MiniMax variants ---
	minimaxVariants := []struct {
		key      string
		provider string
		baseURL  string
	}{
		{"minimax", "minimax", "https://api.minimax.io/anthropic"},
		{"minimax-cn", "minimax-cn", "https://api.minimaxi.com/anthropic"},
	}
	for _, variant := range minimaxVariants {
		if p, ok := data[variant.key]; ok {
			for id, m := range p.Models {
				if !m.ToolCall {
					continue
				}
				input := []string{"text"}
				if hasString(m.Modalities.Input, "image") {
					input = append(input, "image")
				}
				models = append(models, modelSpec{
					ID:             id,
					Name:           stringOr(m.Name, id),
					API:            "anthropic-messages",
					Provider:       variant.provider,
					BaseURL:        variant.baseURL,
					Reasoning:      m.Reasoning,
					Input:          input,
					CostInput:      m.Cost.Input,
					CostOutput:     m.Cost.Output,
					CostCacheRead:  m.Cost.CacheRead,
					CostCacheWrite: m.Cost.CacheWrite,
					ContextWindow:  intOr(m.Limit.Context, 4096),
					MaxTokens:      intOr(m.Limit.Output, 4096),
				})
			}
		}
	}

	// --- Kimi For Coding ---
	if p, ok := data["kimi-for-coding"]; ok {
		// models.dev still exposes deprecated "k2p5" in some snapshots.
		// Normalize to the canonical "kimi-for-coding" id and drop duplicates.
		_, hasCanonical := p.Models["kimi-for-coding"]
		for id, m := range p.Models {
			if !m.ToolCall {
				continue
			}
			if id == "k2p5" && hasCanonical {
				continue
			}

			normalizedID := id
			normalizedName := stringOr(m.Name, id)
			if id == "k2p5" {
				normalizedID = "kimi-for-coding"
				normalizedName = "Kimi For Coding"
			}

			input := []string{"text"}
			if hasString(m.Modalities.Input, "image") {
				input = append(input, "image")
			}
			models = append(models, modelSpec{
				ID:             normalizedID,
				Name:           normalizedName,
				API:            "anthropic-messages",
				Provider:       "kimi-coding",
				BaseURL:        "https://api.kimi.com/coding",
				Reasoning:      m.Reasoning,
				Input:          input,
				CostInput:      m.Cost.Input,
				CostOutput:     m.Cost.Output,
				CostCacheRead:  m.Cost.CacheRead,
				CostCacheWrite: m.Cost.CacheWrite,
				ContextWindow:  intOr(m.Limit.Context, 4096),
				MaxTokens:      intOr(m.Limit.Output, 4096),
			})
		}
	}

	log.Printf("Loaded %d tool-capable models from models.dev", len(models))
	return models, nil
}

// intOr returns n if n > 0, otherwise def.
func intOr(n, def int) int {
	if n > 0 {
		return n
	}
	return def
}

// stringOr returns s if non-empty, otherwise def.
func stringOr(s, def string) string {
	if s != "" {
		return s
	}
	return def
}

// hasModel returns true if any model in all with the given provider and id exists.
func hasModel(all []modelSpec, provider, id string) bool {
	return findModel(all, provider, id) != nil
}

func findModel(all []modelSpec, provider, id string) *modelSpec {
	for i := range all {
		if all[i].Provider == provider && all[i].ID == id {
			return &all[i]
		}
	}
	return nil
}

// applyOverridesAndAdditions applies all the manual fixups from the TS script.
func applyOverridesAndAdditions(all []modelSpec) []modelSpec {
	// Filter out opencode/opencode-go gpt-5.3-codex-spark
	filtered := all[:0]
	for _, m := range all {
		if (m.Provider == "opencode" || m.Provider == "opencode-go") && m.ID == "gpt-5.3-codex-spark" {
			continue
		}
		filtered = append(filtered, m)
	}
	all = filtered

	// Filter MiniMax: only keep supported direct models
	minimaxDirectSupported := map[string]bool{"MiniMax-M2.7": true, "MiniMax-M2.7-highspeed": true}
	filtered = all[:0]
	for _, m := range all {
		if (m.Provider == "minimax" || m.Provider == "minimax-cn") && !minimaxDirectSupported[m.ID] {
			continue
		}
		filtered = append(filtered, m)
	}
	all = filtered

	// Set MiniMax M2.7 context/max tokens
	for i := range all {
		if (all[i].Provider == "minimax" || all[i].Provider == "minimax-cn") && minimaxDirectSupported[all[i].ID] {
			all[i].ContextWindow = 204800
			all[i].MaxTokens = 131072
		}
	}

	// Fix incorrect cache pricing for Claude Opus 4.5 from models.dev
	for i := range all {
		if all[i].Provider == "anthropic" && all[i].ID == "claude-opus-4-5" {
			all[i].CostCacheRead = 0.5
			all[i].CostCacheWrite = 6.25
		}
	}

	// Temporary overrides for Amazon Bedrock Opus 4.6 metadata
	for i := range all {
		m := &all[i]
		if m.Provider == "amazon-bedrock" && strings.Contains(m.ID, "anthropic.claude-opus-4-6-v1") {
			m.CostCacheRead = 0.5
			m.CostCacheWrite = 6.25
			m.ContextWindow = 1000000
		}
		if m.Provider == "amazon-bedrock" && strings.Contains(m.ID, "anthropic.claude-sonnet-4-6") {
			m.ContextWindow = 1000000
		}
		if (m.Provider == "anthropic" || m.Provider == "opencode" || m.Provider == "opencode-go" || m.Provider == "github-copilot") &&
			(m.ID == "claude-opus-4-6" || m.ID == "claude-sonnet-4-6" || m.ID == "claude-opus-4.6" || m.ID == "claude-sonnet-4.6") {
			m.ContextWindow = 1000000
		}
		// OpenCode variants list Claude Sonnet 4/4.5 with 1M context, actual limit is 200K
		if (m.Provider == "opencode" || m.Provider == "opencode-go") && (m.ID == "claude-sonnet-4-5" || m.ID == "claude-sonnet-4") {
			m.ContextWindow = 200000
		}
		if (m.Provider == "opencode" || m.Provider == "opencode-go") && m.ID == "gpt-5.4" {
			m.ContextWindow = 272000
			m.MaxTokens = 128000
		}
		if m.Provider == "openai" && m.ID == "gpt-5.4" {
			m.ContextWindow = 272000
			m.MaxTokens = 128000
		}
		// Keep selected OpenRouter model metadata stable until upstream settles.
		if m.Provider == "openrouter" && m.ID == "moonshotai/kimi-k2.5" {
			m.CostInput = 0.41
			m.CostOutput = 2.06
			m.CostCacheRead = 0.07
			m.MaxTokens = 4096
		}
		if m.Provider == "openrouter" && m.ID == "z-ai/glm-5" {
			m.CostInput = 0.6
			m.CostOutput = 1.9
			m.CostCacheRead = 0.119
		}
	}

	// Add missing EU Opus 4.6 profile
	if !hasModel(all, "amazon-bedrock", "eu.anthropic.claude-opus-4-6-v1") {
		all = append(all, modelSpec{
			ID:        "eu.anthropic.claude-opus-4-6-v1",
			Name:      "Claude Opus 4.6 (EU)",
			API:       "bedrock-converse-stream",
			Provider:  "amazon-bedrock",
			BaseURL:   "https://bedrock-runtime.us-east-1.amazonaws.com",
			Reasoning: true,
			Input:     []string{"text", "image"},
			CostInput: 5, CostOutput: 25, CostCacheRead: 0.5, CostCacheWrite: 6.25,
			ContextWindow: 1000000, MaxTokens: 128000,
		})
	}

	// Add missing Claude Opus 4.6
	if !hasModel(all, "anthropic", "claude-opus-4-6") {
		all = append(all, modelSpec{
			ID:        "claude-opus-4-6",
			Name:      "Claude Opus 4.6",
			API:       "anthropic-messages",
			Provider:  "anthropic",
			BaseURL:   "https://api.anthropic.com",
			Reasoning: true,
			Input:     []string{"text", "image"},
			CostInput: 5, CostOutput: 25, CostCacheRead: 0.5, CostCacheWrite: 6.25,
			ContextWindow: 1000000, MaxTokens: 128000,
		})
	}

	// Add missing Claude Opus 4.8
	if !hasModel(all, "anthropic", "claude-opus-4-8") {
		all = append(all, modelSpec{
			ID:        "claude-opus-4-8",
			Name:      "Claude Opus 4.8",
			API:       "anthropic-messages",
			Provider:  "anthropic",
			BaseURL:   "https://api.anthropic.com",
			Reasoning: true,
			Input:     []string{"text", "image"},
			CostInput: 5, CostOutput: 25, CostCacheRead: 0.5, CostCacheWrite: 6.25,
			ContextWindow: 1000000, MaxTokens: 128000,
		})
	}

	// Add missing Claude Opus 4.7
	if !hasModel(all, "anthropic", "claude-opus-4-7") {
		all = append(all, modelSpec{
			ID:        "claude-opus-4-7",
			Name:      "Claude Opus 4.7",
			API:       "anthropic-messages",
			Provider:  "anthropic",
			BaseURL:   "https://api.anthropic.com",
			Reasoning: true,
			Input:     []string{"text", "image"},
			CostInput: 5, CostOutput: 25, CostCacheRead: 0.5, CostCacheWrite: 6.25,
			ContextWindow: 1000000, MaxTokens: 128000,
		})
	}

	// Add missing Claude Sonnet 4.6
	if !hasModel(all, "anthropic", "claude-sonnet-4-6") {
		all = append(all, modelSpec{
			ID:        "claude-sonnet-4-6",
			Name:      "Claude Sonnet 4.6",
			API:       "anthropic-messages",
			Provider:  "anthropic",
			BaseURL:   "https://api.anthropic.com",
			Reasoning: true,
			Input:     []string{"text", "image"},
			CostInput: 3, CostOutput: 15, CostCacheRead: 0.3, CostCacheWrite: 3.75,
			ContextWindow: 1000000, MaxTokens: 64000,
		})
	}

	// Add missing GPT models
	if !hasModel(all, "openai", "gpt-5-chat-latest") {
		all = append(all, modelSpec{
			ID: "gpt-5-chat-latest", Name: "GPT-5 Chat Latest",
			API: "openai-responses", Provider: "openai", BaseURL: "https://api.openai.com/v1",
			Reasoning: false, Input: []string{"text", "image"},
			CostInput: 1.25, CostOutput: 10, CostCacheRead: 0.125, CostCacheWrite: 0,
			ContextWindow: 128000, MaxTokens: 16384,
		})
	}
	if !hasModel(all, "openai", "gpt-5.1-codex") {
		all = append(all, modelSpec{
			ID: "gpt-5.1-codex", Name: "GPT-5.1 Codex",
			API: "openai-responses", Provider: "openai", BaseURL: "https://api.openai.com/v1",
			Reasoning: true, Input: []string{"text", "image"},
			CostInput: 1.25, CostOutput: 5, CostCacheRead: 0.125, CostCacheWrite: 1.25,
			ContextWindow: 400000, MaxTokens: 128000,
		})
	}
	if !hasModel(all, "openai", "gpt-5.1-codex-max") {
		all = append(all, modelSpec{
			ID: "gpt-5.1-codex-max", Name: "GPT-5.1 Codex Max",
			API: "openai-responses", Provider: "openai", BaseURL: "https://api.openai.com/v1",
			Reasoning: true, Input: []string{"text", "image"},
			CostInput: 1.25, CostOutput: 10, CostCacheRead: 0.125, CostCacheWrite: 0,
			ContextWindow: 400000, MaxTokens: 128000,
		})
	}
	if !hasModel(all, "openai", "gpt-5.3-codex-spark") {
		all = append(all, modelSpec{
			ID: "gpt-5.3-codex-spark", Name: "GPT-5.3 Codex Spark",
			API: "openai-responses", Provider: "openai", BaseURL: "https://api.openai.com/v1",
			Reasoning: true, Input: []string{"text"},
			CostInput: 0, CostOutput: 0, CostCacheRead: 0, CostCacheWrite: 0,
			ContextWindow: 128000, MaxTokens: 16384,
		})
	}
	if !hasModel(all, "openai", "gpt-5.4") {
		all = append(all, modelSpec{
			ID: "gpt-5.4", Name: "GPT-5.4",
			API: "openai-responses", Provider: "openai", BaseURL: "https://api.openai.com/v1",
			Reasoning: true, Input: []string{"text", "image"},
			CostInput: 2.5, CostOutput: 15, CostCacheRead: 0.25, CostCacheWrite: 0,
			ContextWindow: 1048576, MaxTokens: 128000,
		})
	}
	// Fix cache read pricing for gpt-5.4-pro across all providers
	for i := range all {
		if all[i].ID == "gpt-5.4-pro" || strings.HasSuffix(all[i].ID, "/gpt-5.4-pro") {
			all[i].CostCacheRead = 3
		}
	}
	if !hasModel(all, "openai", "gpt-5.4-pro") {
		all = append(all, modelSpec{
			ID: "gpt-5.4-pro", Name: "GPT-5.4 Pro",
			API: "openai-responses", Provider: "openai", BaseURL: "https://api.openai.com/v1",
			Reasoning: true, Input: []string{"text", "image"},
			CostInput: 30, CostOutput: 180, CostCacheRead: 3, CostCacheWrite: 0,
			ContextWindow: 1048576, MaxTokens: 128000,
		})
	}

	// Add missing Gemini 3.1 Flash Lite Preview until models.dev includes it.
	if !hasModel(all, "google", "gemini-3.1-flash-lite-preview") {
		all = append(all, modelSpec{
			ID: "gemini-3.1-flash-lite-preview", Name: "Gemini 3.1 Flash Lite Preview",
			API: "google-generative-ai", Provider: "google",
			BaseURL:   "https://generativelanguage.googleapis.com/v1beta",
			Reasoning: true, Input: []string{"text", "image"},
			ContextWindow: 1048576, MaxTokens: 65536,
		})
	}

	// Add missing GitHub Copilot GPT-5.3 models until models.dev includes them.
	copilotBase := findModel(all, "github-copilot", "gpt-5.2-codex")
	if copilotBase != nil && !hasModel(all, "github-copilot", "gpt-5.3-codex") {
		clone := *copilotBase
		clone.ID = "gpt-5.3-codex"
		clone.Name = "GPT-5.3 Codex"
		all = append(all, clone)
	}

	// OpenAI Codex (ChatGPT OAuth) models
	const codexBaseURL = "https://chatgpt.com/backend-api"
	const codexContext = 272000
	const codexMaxTokens = 128000
	codexModels := []modelSpec{
		{ID: "gpt-5.1", Name: "GPT-5.1", API: "openai-codex-responses", Provider: "openai-codex",
			BaseURL: codexBaseURL, Reasoning: true, Input: []string{"text", "image"},
			CostInput: 1.25, CostOutput: 10, CostCacheRead: 0.125, CostCacheWrite: 0,
			ContextWindow: codexContext, MaxTokens: codexMaxTokens},
		{ID: "gpt-5.1-codex-max", Name: "GPT-5.1 Codex Max", API: "openai-codex-responses", Provider: "openai-codex",
			BaseURL: codexBaseURL, Reasoning: true, Input: []string{"text", "image"},
			CostInput: 1.25, CostOutput: 10, CostCacheRead: 0.125, CostCacheWrite: 0,
			ContextWindow: codexContext, MaxTokens: codexMaxTokens},
		{ID: "gpt-5.1-codex-mini", Name: "GPT-5.1 Codex Mini", API: "openai-codex-responses", Provider: "openai-codex",
			BaseURL: codexBaseURL, Reasoning: true, Input: []string{"text", "image"},
			CostInput: 0.25, CostOutput: 2, CostCacheRead: 0.025, CostCacheWrite: 0,
			ContextWindow: codexContext, MaxTokens: codexMaxTokens},
		{ID: "gpt-5.2", Name: "GPT-5.2", API: "openai-codex-responses", Provider: "openai-codex",
			BaseURL: codexBaseURL, Reasoning: true, Input: []string{"text", "image"},
			CostInput: 1.75, CostOutput: 14, CostCacheRead: 0.175, CostCacheWrite: 0,
			ContextWindow: codexContext, MaxTokens: codexMaxTokens},
		{ID: "gpt-5.2-codex", Name: "GPT-5.2 Codex", API: "openai-codex-responses", Provider: "openai-codex",
			BaseURL: codexBaseURL, Reasoning: true, Input: []string{"text", "image"},
			CostInput: 1.75, CostOutput: 14, CostCacheRead: 0.175, CostCacheWrite: 0,
			ContextWindow: codexContext, MaxTokens: codexMaxTokens},
		{ID: "gpt-5.3-codex", Name: "GPT-5.3 Codex", API: "openai-codex-responses", Provider: "openai-codex",
			BaseURL: codexBaseURL, Reasoning: true, Input: []string{"text", "image"},
			CostInput: 1.75, CostOutput: 14, CostCacheRead: 0.175, CostCacheWrite: 0,
			ContextWindow: codexContext, MaxTokens: codexMaxTokens},
		{ID: "gpt-5.3-codex-spark", Name: "GPT-5.3 Codex Spark", API: "openai-codex-responses", Provider: "openai-codex",
			BaseURL: codexBaseURL, Reasoning: true, Input: []string{"text"},
			CostInput: 0, CostOutput: 0, CostCacheRead: 0, CostCacheWrite: 0,
			ContextWindow: 128000, MaxTokens: codexMaxTokens},
		{ID: "gpt-5.4", Name: "GPT-5.4", API: "openai-codex-responses", Provider: "openai-codex",
			BaseURL: codexBaseURL, Reasoning: true, Input: []string{"text", "image"},
			CostInput: 2.5, CostOutput: 15, CostCacheRead: 0.25, CostCacheWrite: 0,
			ContextWindow: 1048576, MaxTokens: codexMaxTokens},
		{ID: "gpt-5.5", Name: "GPT-5.5", API: "openai-codex-responses", Provider: "openai-codex",
			BaseURL: codexBaseURL, Reasoning: true, Input: []string{"text", "image"},
			CostInput: 5, CostOutput: 30, CostCacheRead: 0.5, CostCacheWrite: 0,
			ContextWindow: codexContext, MaxTokens: codexMaxTokens},
		{ID: "gpt-5.4-mini", Name: "GPT-5.4 Mini", API: "openai-codex-responses", Provider: "openai-codex",
			BaseURL: codexBaseURL, Reasoning: true, Input: []string{"text", "image"},
			CostInput: 0.75, CostOutput: 4.5, CostCacheRead: 0.075, CostCacheWrite: 0,
			ContextWindow: codexContext, MaxTokens: codexMaxTokens},
	}
	all = append(all, codexModels...)

	// --- DeepSeek V4 models ---
	deepseekCompat := &compatSpec{
		RequiresReasoningContentOnAssistantMessages: boolPtr(true),
		ThinkingFormat: "deepseek",
		ReasoningEffortMap: map[string]string{
			"minimal": "high",
			"low":     "high",
			"medium":  "high",
			"high":    "high",
			"xhigh":   "max",
		},
	}
	if !hasModel(all, "deepseek", "deepseek-v4-flash") {
		all = append(all, modelSpec{
			ID: "deepseek-v4-flash", Name: "DeepSeek V4 Flash",
			API: "openai-completions", Provider: "deepseek", BaseURL: "https://api.deepseek.com",
			Reasoning: true, Input: []string{"text"},
			CostInput: 0.14, CostOutput: 0.28, CostCacheRead: 0.028, CostCacheWrite: 0,
			ContextWindow: 1000000, MaxTokens: 384000,
			Compat: deepseekCompat,
		})
	}
	if !hasModel(all, "deepseek", "deepseek-v4-pro") {
		all = append(all, modelSpec{
			ID: "deepseek-v4-pro", Name: "DeepSeek V4 Pro",
			API: "openai-completions", Provider: "deepseek", BaseURL: "https://api.deepseek.com",
			Reasoning: true, Input: []string{"text"},
			CostInput: 1.74, CostOutput: 3.48, CostCacheRead: 0.145, CostCacheWrite: 0,
			ContextWindow: 1000000, MaxTokens: 384000,
			Compat: deepseekCompat,
		})
	}

	// Add missing Grok model
	if !hasModel(all, "xai", "grok-code-fast-1") {
		all = append(all, modelSpec{
			ID: "grok-code-fast-1", Name: "Grok Code Fast 1",
			API: "openai-completions", Provider: "xai", BaseURL: "https://api.x.ai/v1",
			Reasoning: false, Input: []string{"text"},
			CostInput: 0.2, CostOutput: 1.5, CostCacheRead: 0.02, CostCacheWrite: 0,
			ContextWindow: 32768, MaxTokens: 8192,
		})
	}

	// Add "auto" alias for openrouter/auto
	if !hasModel(all, "openrouter", "auto") {
		all = append(all, modelSpec{
			ID: "auto", Name: "Auto",
			API: "openai-completions", Provider: "openrouter", BaseURL: "https://openrouter.ai/api/v1",
			Reasoning: true, Input: []string{"text", "image"},
			CostInput: 0, CostOutput: 0, CostCacheRead: 0, CostCacheWrite: 0,
			ContextWindow: 2000000, MaxTokens: 30000,
		})
	}

	// Gemini 3.1 Pro — announced 2026-02-19, still in preview on all Google endpoints.
	// https://blog.google/innovation-and-ai/models-and-research/gemini-models/gemini-3-1-pro/
	// The model ID is gemini-3.1-pro-preview.

	// Gemini 3.1 Pro Preview Custom Tools — variant of the google provider model that
	// enables custom tool definitions (not returned by models.dev).
	if !hasModel(all, "google", "gemini-3.1-pro-preview-customtools") {
		all = append(all, modelSpec{
			ID:        "gemini-3.1-pro-preview-customtools",
			Name:      "Gemini 3.1 Pro Preview Custom Tools",
			API:       "google-generative-ai",
			Provider:  "google",
			BaseURL:   "https://generativelanguage.googleapis.com/v1beta",
			Reasoning: true,
			Input:     []string{"text", "image"},
			CostInput: 2, CostOutput: 12, CostCacheRead: 0.2, CostCacheWrite: 0,
			ContextWindow: 1048576, MaxTokens: 65536,
		})
	}

	if !hasModel(all, "google", "gemini-3.1-flash-light-preview") {
		all = append(all, modelSpec{
			ID:        "gemini-3.1-flash-light-preview",
			Name:      "Gemini 3.1 Flash Light Preview",
			API:       "google-generative-ai",
			Provider:  "google",
			BaseURL:   "https://generativelanguage.googleapis.com/v1beta",
			Reasoning: true,
			Input:     []string{"text", "image"},
			CostInput: 0.1, CostOutput: 0.4, CostCacheRead: 0.01, CostCacheWrite: 0,
			ContextWindow: 1048576, MaxTokens: 65536,
		})
	}

	if !hasModel(all, "google-vertex", "gemini-3.1-pro-preview-customtools") {
		all = append(all, modelSpec{
			ID:        "gemini-3.1-pro-preview-customtools",
			Name:      "Gemini 3.1 Pro Preview Custom Tools (Vertex)",
			API:       "google-vertex",
			Provider:  "google-vertex",
			Reasoning: true,
			Input:     []string{"text", "image"},
			CostInput: 2, CostOutput: 12, CostCacheRead: 0.2, CostCacheWrite: 0,
			ContextWindow: 1048576, MaxTokens: 65536,
		})
	}

	// Google Vertex models
	const vertexBaseURL = "https://{location}-aiplatform.googleapis.com"
	vertexModels := []modelSpec{
		{ID: "gemini-3-pro-preview", Name: "Gemini 3 Pro Preview (Vertex)", API: "google-vertex",
			Provider: "google-vertex", BaseURL: vertexBaseURL, Reasoning: true,
			Input: []string{"text", "image"}, CostInput: 2, CostOutput: 12, CostCacheRead: 0.2, CostCacheWrite: 0,
			ContextWindow: 1000000, MaxTokens: 64000},
		{ID: "gemini-3.1-pro-preview", Name: "Gemini 3.1 Pro Preview (Vertex)", API: "google-vertex",
			Provider: "google-vertex", BaseURL: vertexBaseURL, Reasoning: true,
			Input: []string{"text", "image"}, CostInput: 2, CostOutput: 12, CostCacheRead: 0.2, CostCacheWrite: 0,
			ContextWindow: 1048576, MaxTokens: 65536},
		{ID: "gemini-3-flash-preview", Name: "Gemini 3 Flash Preview (Vertex)", API: "google-vertex",
			Provider: "google-vertex", BaseURL: vertexBaseURL, Reasoning: true,
			Input: []string{"text", "image"}, CostInput: 0.5, CostOutput: 3, CostCacheRead: 0.05, CostCacheWrite: 0,
			ContextWindow: 1048576, MaxTokens: 65536},
		{ID: "gemini-3.1-pro-preview", Name: "Gemini 3.1 Pro Preview (Vertex)", API: "google-vertex",
			Provider: "google-vertex", BaseURL: vertexBaseURL, Reasoning: true,
			Input: []string{"text", "image"}, CostInput: 2, CostOutput: 12, CostCacheRead: 0.2, CostCacheWrite: 0,
			ContextWindow: 1048576, MaxTokens: 65536},
		{ID: "gemini-3.1-flash-light-preview", Name: "Gemini 3.1 Flash Light Preview (Vertex)", API: "google-vertex",
			Provider: "google-vertex", BaseURL: vertexBaseURL, Reasoning: true,
			Input: []string{"text", "image"}, CostInput: 0.1, CostOutput: 0.4, CostCacheRead: 0.01, CostCacheWrite: 0,
			ContextWindow: 1048576, MaxTokens: 65536},
		{ID: "gemini-2.0-flash", Name: "Gemini 2.0 Flash (Vertex)", API: "google-vertex",
			Provider: "google-vertex", BaseURL: vertexBaseURL, Reasoning: false,
			Input: []string{"text", "image"}, CostInput: 0.15, CostOutput: 0.6, CostCacheRead: 0.0375, CostCacheWrite: 0,
			ContextWindow: 1048576, MaxTokens: 8192},
		{ID: "gemini-2.0-flash-lite", Name: "Gemini 2.0 Flash Lite (Vertex)", API: "google-vertex",
			Provider: "google-vertex", BaseURL: vertexBaseURL, Reasoning: true,
			Input: []string{"text", "image"}, CostInput: 0.075, CostOutput: 0.3, CostCacheRead: 0.01875, CostCacheWrite: 0,
			ContextWindow: 1048576, MaxTokens: 65536},
		{ID: "gemini-2.5-pro", Name: "Gemini 2.5 Pro (Vertex)", API: "google-vertex",
			Provider: "google-vertex", BaseURL: vertexBaseURL, Reasoning: true,
			Input: []string{"text", "image"}, CostInput: 1.25, CostOutput: 10, CostCacheRead: 0.125, CostCacheWrite: 0,
			ContextWindow: 1048576, MaxTokens: 65536},
		{ID: "gemini-2.5-flash", Name: "Gemini 2.5 Flash (Vertex)", API: "google-vertex",
			Provider: "google-vertex", BaseURL: vertexBaseURL, Reasoning: true,
			Input: []string{"text", "image"}, CostInput: 0.3, CostOutput: 2.5, CostCacheRead: 0.03, CostCacheWrite: 0,
			ContextWindow: 1048576, MaxTokens: 65536},
		{ID: "gemini-2.5-flash-lite-preview-09-2025", Name: "Gemini 2.5 Flash Lite Preview 09-25 (Vertex)", API: "google-vertex",
			Provider: "google-vertex", BaseURL: vertexBaseURL, Reasoning: true,
			Input: []string{"text", "image"}, CostInput: 0.1, CostOutput: 0.4, CostCacheRead: 0.01, CostCacheWrite: 0,
			ContextWindow: 1048576, MaxTokens: 65536},
		{ID: "gemini-2.5-flash-lite", Name: "Gemini 2.5 Flash Lite (Vertex)", API: "google-vertex",
			Provider: "google-vertex", BaseURL: vertexBaseURL, Reasoning: true,
			Input: []string{"text", "image"}, CostInput: 0.1, CostOutput: 0.4, CostCacheRead: 0.01, CostCacheWrite: 0,
			ContextWindow: 1048576, MaxTokens: 65536},
		{ID: "gemini-1.5-pro", Name: "Gemini 1.5 Pro (Vertex)", API: "google-vertex",
			Provider: "google-vertex", BaseURL: vertexBaseURL, Reasoning: false,
			Input: []string{"text", "image"}, CostInput: 1.25, CostOutput: 5, CostCacheRead: 0.3125, CostCacheWrite: 0,
			ContextWindow: 1000000, MaxTokens: 8192},
		{ID: "gemini-1.5-flash", Name: "Gemini 1.5 Flash (Vertex)", API: "google-vertex",
			Provider: "google-vertex", BaseURL: vertexBaseURL, Reasoning: false,
			Input: []string{"text", "image"}, CostInput: 0.075, CostOutput: 0.3, CostCacheRead: 0.01875, CostCacheWrite: 0,
			ContextWindow: 1000000, MaxTokens: 8192},
		{ID: "gemini-1.5-flash-8b", Name: "Gemini 1.5 Flash-8B (Vertex)", API: "google-vertex",
			Provider: "google-vertex", BaseURL: vertexBaseURL, Reasoning: false,
			Input: []string{"text", "image"}, CostInput: 0.0375, CostOutput: 0.15, CostCacheRead: 0.01, CostCacheWrite: 0,
			ContextWindow: 1000000, MaxTokens: 8192},
	}
	all = append(all, vertexModels...)

	// Annotate Anthropic Messages Claude models with known server-tool capabilities.
	for i := range all {
		m := &all[i]
		if m.API != "anthropic-messages" {
			continue
		}
		if !strings.Contains(m.ID, "claude") {
			continue
		}
		if len(m.ServerTools) == 0 {
			m.ServerTools = []string{
				"web_search_20250305",
				"web_search_20260209",
				"web_fetch_20250910",
				"web_fetch_20260209",
				"code_execution_20250825",
			}
		}
		if strings.Contains(m.ID, "opus-4-8") || strings.Contains(m.ID, "opus-4.8") ||
			strings.Contains(m.ID, "opus-4-6") || strings.Contains(m.ID, "opus-4.6") ||
			strings.Contains(m.ID, "sonnet-4-6") || strings.Contains(m.ID, "sonnet-4.6") {
			m.Compaction = true
		}
	}

	// --- New providers added in upstream v0.71.x–v0.72.x: cloudflare, xiaomi, moonshotai ---
	// Models pulled from upstream pi-mono packages/ai/src/models.generated.ts at v0.72.1.
	// fir doesn't yet have native streaming providers for cloudflare-ai-gateway specifically
	// wired through anthropic.go, but openai-completions-style models work via the existing
	// OpenAI completions provider. The cf-aig-authorization header / gateway baseUrl wiring
	// lives in pkg/ai/providers/cloudflare.go.
	newProviderModels := []modelSpec{
		{ID: "@cf/google/gemma-4-26b-a4b-it", Name: "Gemma 4 26B A4B IT", API: "openai-completions", Provider: "cloudflare-workers-ai", BaseURL: "https://api.cloudflare.com/client/v4/accounts/{CLOUDFLARE_ACCOUNT_ID}/ai/v1", Reasoning: true, Input: []string{"text", "image"}, CostInput: 0.1, CostOutput: 0.3, CostCacheRead: 0.0, CostCacheWrite: 0.0, ContextWindow: 256000, MaxTokens: 16384},
		{ID: "@cf/meta/llama-4-scout-17b-16e-instruct", Name: "Llama 4 Scout 17B 16E Instruct", API: "openai-completions", Provider: "cloudflare-workers-ai", BaseURL: "https://api.cloudflare.com/client/v4/accounts/{CLOUDFLARE_ACCOUNT_ID}/ai/v1", Reasoning: false, Input: []string{"text", "image"}, CostInput: 0.27, CostOutput: 0.85, CostCacheRead: 0.0, CostCacheWrite: 0.0, ContextWindow: 128000, MaxTokens: 16384},
		{ID: "@cf/moonshotai/kimi-k2.5", Name: "Kimi K2.5", API: "openai-completions", Provider: "cloudflare-workers-ai", BaseURL: "https://api.cloudflare.com/client/v4/accounts/{CLOUDFLARE_ACCOUNT_ID}/ai/v1", Reasoning: true, Input: []string{"text", "image"}, CostInput: 0.6, CostOutput: 3.0, CostCacheRead: 0.1, CostCacheWrite: 0.0, ContextWindow: 256000, MaxTokens: 256000},
		{ID: "@cf/moonshotai/kimi-k2.6", Name: "Kimi K2.6", API: "openai-completions", Provider: "cloudflare-workers-ai", BaseURL: "https://api.cloudflare.com/client/v4/accounts/{CLOUDFLARE_ACCOUNT_ID}/ai/v1", Reasoning: true, Input: []string{"text", "image"}, CostInput: 0.95, CostOutput: 4.0, CostCacheRead: 0.16, CostCacheWrite: 0.0, ContextWindow: 256000, MaxTokens: 256000},
		{ID: "@cf/nvidia/nemotron-3-120b-a12b", Name: "Nemotron 3 Super 120B", API: "openai-completions", Provider: "cloudflare-workers-ai", BaseURL: "https://api.cloudflare.com/client/v4/accounts/{CLOUDFLARE_ACCOUNT_ID}/ai/v1", Reasoning: true, Input: []string{"text"}, CostInput: 0.5, CostOutput: 1.5, CostCacheRead: 0.0, CostCacheWrite: 0.0, ContextWindow: 256000, MaxTokens: 256000},
		{ID: "@cf/openai/gpt-oss-120b", Name: "GPT OSS 120B", API: "openai-completions", Provider: "cloudflare-workers-ai", BaseURL: "https://api.cloudflare.com/client/v4/accounts/{CLOUDFLARE_ACCOUNT_ID}/ai/v1", Reasoning: true, Input: []string{"text"}, CostInput: 0.35, CostOutput: 0.75, CostCacheRead: 0.0, CostCacheWrite: 0.0, ContextWindow: 128000, MaxTokens: 16384},
		{ID: "@cf/openai/gpt-oss-20b", Name: "GPT OSS 20B", API: "openai-completions", Provider: "cloudflare-workers-ai", BaseURL: "https://api.cloudflare.com/client/v4/accounts/{CLOUDFLARE_ACCOUNT_ID}/ai/v1", Reasoning: true, Input: []string{"text"}, CostInput: 0.2, CostOutput: 0.3, CostCacheRead: 0.0, CostCacheWrite: 0.0, ContextWindow: 128000, MaxTokens: 16384},
		{ID: "@cf/zai-org/glm-4.7-flash", Name: "GLM-4.7-Flash", API: "openai-completions", Provider: "cloudflare-workers-ai", BaseURL: "https://api.cloudflare.com/client/v4/accounts/{CLOUDFLARE_ACCOUNT_ID}/ai/v1", Reasoning: true, Input: []string{"text"}, CostInput: 0.06, CostOutput: 0.4, CostCacheRead: 0.0, CostCacheWrite: 0.0, ContextWindow: 131072, MaxTokens: 131072},
		{ID: "claude-3-5-haiku", Name: "Claude Haiku 3.5 (latest)", API: "anthropic-messages", Provider: "cloudflare-ai-gateway", BaseURL: "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/anthropic", Reasoning: false, Input: []string{"text", "image"}, CostInput: 0.8, CostOutput: 4.0, CostCacheRead: 0.08, CostCacheWrite: 1.0, ContextWindow: 200000, MaxTokens: 8192},
		{ID: "claude-3-haiku", Name: "Claude Haiku 3", API: "anthropic-messages", Provider: "cloudflare-ai-gateway", BaseURL: "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/anthropic", Reasoning: false, Input: []string{"text", "image"}, CostInput: 0.25, CostOutput: 1.25, CostCacheRead: 0.03, CostCacheWrite: 0.3, ContextWindow: 200000, MaxTokens: 4096},
		{ID: "claude-3-opus", Name: "Claude Opus 3", API: "anthropic-messages", Provider: "cloudflare-ai-gateway", BaseURL: "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/anthropic", Reasoning: false, Input: []string{"text", "image"}, CostInput: 15.0, CostOutput: 75.0, CostCacheRead: 1.5, CostCacheWrite: 18.75, ContextWindow: 200000, MaxTokens: 4096},
		{ID: "claude-3-sonnet", Name: "Claude Sonnet 3", API: "anthropic-messages", Provider: "cloudflare-ai-gateway", BaseURL: "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/anthropic", Reasoning: false, Input: []string{"text", "image"}, CostInput: 3.0, CostOutput: 15.0, CostCacheRead: 0.3, CostCacheWrite: 0.3, ContextWindow: 200000, MaxTokens: 4096},
		{ID: "claude-3.5-haiku", Name: "Claude Haiku 3.5 (latest)", API: "anthropic-messages", Provider: "cloudflare-ai-gateway", BaseURL: "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/anthropic", Reasoning: false, Input: []string{"text", "image"}, CostInput: 0.8, CostOutput: 4.0, CostCacheRead: 0.08, CostCacheWrite: 1.0, ContextWindow: 200000, MaxTokens: 8192},
		{ID: "claude-3.5-sonnet", Name: "Claude Sonnet 3.5 v2", API: "anthropic-messages", Provider: "cloudflare-ai-gateway", BaseURL: "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/anthropic", Reasoning: false, Input: []string{"text", "image"}, CostInput: 3.0, CostOutput: 15.0, CostCacheRead: 0.3, CostCacheWrite: 3.75, ContextWindow: 200000, MaxTokens: 8192},
		{ID: "claude-haiku-4-5", Name: "Claude Haiku 4.5 (latest)", API: "anthropic-messages", Provider: "cloudflare-ai-gateway", BaseURL: "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/anthropic", Reasoning: true, Input: []string{"text", "image"}, CostInput: 1.0, CostOutput: 5.0, CostCacheRead: 0.1, CostCacheWrite: 1.25, ContextWindow: 200000, MaxTokens: 64000},
		{ID: "claude-opus-4", Name: "Claude Opus 4 (latest)", API: "anthropic-messages", Provider: "cloudflare-ai-gateway", BaseURL: "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/anthropic", Reasoning: true, Input: []string{"text", "image"}, CostInput: 15.0, CostOutput: 75.0, CostCacheRead: 1.5, CostCacheWrite: 18.75, ContextWindow: 200000, MaxTokens: 32000},
		{ID: "claude-opus-4-1", Name: "Claude Opus 4.1 (latest)", API: "anthropic-messages", Provider: "cloudflare-ai-gateway", BaseURL: "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/anthropic", Reasoning: true, Input: []string{"text", "image"}, CostInput: 15.0, CostOutput: 75.0, CostCacheRead: 1.5, CostCacheWrite: 18.75, ContextWindow: 200000, MaxTokens: 32000},
		{ID: "claude-opus-4-5", Name: "Claude Opus 4.5 (latest)", API: "anthropic-messages", Provider: "cloudflare-ai-gateway", BaseURL: "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/anthropic", Reasoning: true, Input: []string{"text", "image"}, CostInput: 5.0, CostOutput: 25.0, CostCacheRead: 0.5, CostCacheWrite: 6.25, ContextWindow: 200000, MaxTokens: 64000},
		{ID: "claude-opus-4-6", Name: "Claude Opus 4.6 (latest)", API: "anthropic-messages", Provider: "cloudflare-ai-gateway", BaseURL: "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/anthropic", Reasoning: true, Input: []string{"text", "image"}, CostInput: 5.0, CostOutput: 25.0, CostCacheRead: 0.5, CostCacheWrite: 6.25, ContextWindow: 1000000, MaxTokens: 128000},
		{ID: "claude-opus-4-7", Name: "Claude Opus 4.7", API: "anthropic-messages", Provider: "cloudflare-ai-gateway", BaseURL: "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/anthropic", Reasoning: true, Input: []string{"text", "image"}, CostInput: 5.0, CostOutput: 25.0, CostCacheRead: 0.5, CostCacheWrite: 6.25, ContextWindow: 1000000, MaxTokens: 128000},
		{ID: "claude-opus-4-8", Name: "Claude Opus 4.8", API: "anthropic-messages", Provider: "cloudflare-ai-gateway", BaseURL: "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/anthropic", Reasoning: true, Input: []string{"text", "image"}, CostInput: 5.0, CostOutput: 25.0, CostCacheRead: 0.5, CostCacheWrite: 6.25, ContextWindow: 1000000, MaxTokens: 128000},
		{ID: "claude-sonnet-4", Name: "Claude Sonnet 4 (latest)", API: "anthropic-messages", Provider: "cloudflare-ai-gateway", BaseURL: "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/anthropic", Reasoning: true, Input: []string{"text", "image"}, CostInput: 3.0, CostOutput: 15.0, CostCacheRead: 0.3, CostCacheWrite: 3.75, ContextWindow: 200000, MaxTokens: 64000},
		{ID: "claude-sonnet-4-5", Name: "Claude Sonnet 4.5 (latest)", API: "anthropic-messages", Provider: "cloudflare-ai-gateway", BaseURL: "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/anthropic", Reasoning: true, Input: []string{"text", "image"}, CostInput: 3.0, CostOutput: 15.0, CostCacheRead: 0.3, CostCacheWrite: 3.75, ContextWindow: 200000, MaxTokens: 64000},
		{ID: "claude-sonnet-4-6", Name: "Claude Sonnet 4.6", API: "anthropic-messages", Provider: "cloudflare-ai-gateway", BaseURL: "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/anthropic", Reasoning: true, Input: []string{"text", "image"}, CostInput: 3.0, CostOutput: 15.0, CostCacheRead: 0.3, CostCacheWrite: 3.75, ContextWindow: 1000000, MaxTokens: 64000},
		{ID: "gpt-4", Name: "GPT-4", API: "openai-responses", Provider: "cloudflare-ai-gateway", BaseURL: "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/openai", Reasoning: false, Input: []string{"text"}, CostInput: 30.0, CostOutput: 60.0, CostCacheRead: 0.0, CostCacheWrite: 0.0, ContextWindow: 8192, MaxTokens: 8192},
		{ID: "gpt-4-turbo", Name: "GPT-4 Turbo", API: "openai-responses", Provider: "cloudflare-ai-gateway", BaseURL: "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/openai", Reasoning: false, Input: []string{"text", "image"}, CostInput: 10.0, CostOutput: 30.0, CostCacheRead: 0.0, CostCacheWrite: 0.0, ContextWindow: 128000, MaxTokens: 4096},
		{ID: "gpt-4o", Name: "GPT-4o", API: "openai-responses", Provider: "cloudflare-ai-gateway", BaseURL: "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/openai", Reasoning: false, Input: []string{"text", "image"}, CostInput: 2.5, CostOutput: 10.0, CostCacheRead: 1.25, CostCacheWrite: 0.0, ContextWindow: 128000, MaxTokens: 16384},
		{ID: "gpt-4o-mini", Name: "GPT-4o mini", API: "openai-responses", Provider: "cloudflare-ai-gateway", BaseURL: "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/openai", Reasoning: false, Input: []string{"text", "image"}, CostInput: 0.15, CostOutput: 0.6, CostCacheRead: 0.08, CostCacheWrite: 0.0, ContextWindow: 128000, MaxTokens: 16384},
		{ID: "gpt-5.1", Name: "GPT-5.1", API: "openai-responses", Provider: "cloudflare-ai-gateway", BaseURL: "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/openai", Reasoning: true, Input: []string{"text", "image"}, CostInput: 1.25, CostOutput: 10.0, CostCacheRead: 0.13, CostCacheWrite: 0.0, ContextWindow: 400000, MaxTokens: 128000},
		{ID: "gpt-5.1-codex", Name: "GPT-5.1 Codex", API: "openai-responses", Provider: "cloudflare-ai-gateway", BaseURL: "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/openai", Reasoning: true, Input: []string{"text", "image"}, CostInput: 1.25, CostOutput: 10.0, CostCacheRead: 0.125, CostCacheWrite: 0.0, ContextWindow: 400000, MaxTokens: 128000},
		{ID: "gpt-5.2", Name: "GPT-5.2", API: "openai-responses", Provider: "cloudflare-ai-gateway", BaseURL: "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/openai", Reasoning: true, Input: []string{"text", "image"}, CostInput: 1.75, CostOutput: 14.0, CostCacheRead: 0.175, CostCacheWrite: 0.0, ContextWindow: 400000, MaxTokens: 128000},
		{ID: "gpt-5.2-codex", Name: "GPT-5.2 Codex", API: "openai-responses", Provider: "cloudflare-ai-gateway", BaseURL: "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/openai", Reasoning: true, Input: []string{"text", "image"}, CostInput: 1.75, CostOutput: 14.0, CostCacheRead: 0.175, CostCacheWrite: 0.0, ContextWindow: 400000, MaxTokens: 128000},
		{ID: "gpt-5.3-codex", Name: "GPT-5.3 Codex", API: "openai-responses", Provider: "cloudflare-ai-gateway", BaseURL: "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/openai", Reasoning: true, Input: []string{"text", "image"}, CostInput: 1.75, CostOutput: 14.0, CostCacheRead: 0.175, CostCacheWrite: 0.0, ContextWindow: 400000, MaxTokens: 128000},
		{ID: "gpt-5.4", Name: "GPT-5.4", API: "openai-responses", Provider: "cloudflare-ai-gateway", BaseURL: "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/openai", Reasoning: true, Input: []string{"text", "image"}, CostInput: 2.5, CostOutput: 15.0, CostCacheRead: 0.25, CostCacheWrite: 0.0, ContextWindow: 1050000, MaxTokens: 128000},
		{ID: "gpt-5.5", Name: "GPT-5.5", API: "openai-responses", Provider: "cloudflare-ai-gateway", BaseURL: "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/openai", Reasoning: true, Input: []string{"text", "image"}, CostInput: 5.0, CostOutput: 30.0, CostCacheRead: 0.5, CostCacheWrite: 0.0, ContextWindow: 1050000, MaxTokens: 128000},
		{ID: "o1", Name: "o1", API: "openai-responses", Provider: "cloudflare-ai-gateway", BaseURL: "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/openai", Reasoning: true, Input: []string{"text", "image"}, CostInput: 15.0, CostOutput: 60.0, CostCacheRead: 7.5, CostCacheWrite: 0.0, ContextWindow: 200000, MaxTokens: 100000},
		{ID: "o3", Name: "o3", API: "openai-responses", Provider: "cloudflare-ai-gateway", BaseURL: "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/openai", Reasoning: true, Input: []string{"text", "image"}, CostInput: 2.0, CostOutput: 8.0, CostCacheRead: 0.5, CostCacheWrite: 0.0, ContextWindow: 200000, MaxTokens: 100000},
		{ID: "o3-mini", Name: "o3-mini", API: "openai-responses", Provider: "cloudflare-ai-gateway", BaseURL: "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/openai", Reasoning: true, Input: []string{"text"}, CostInput: 1.1, CostOutput: 4.4, CostCacheRead: 0.55, CostCacheWrite: 0.0, ContextWindow: 200000, MaxTokens: 100000},
		{ID: "o3-pro", Name: "o3-pro", API: "openai-responses", Provider: "cloudflare-ai-gateway", BaseURL: "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/openai", Reasoning: true, Input: []string{"text", "image"}, CostInput: 20.0, CostOutput: 80.0, CostCacheRead: 0.0, CostCacheWrite: 0.0, ContextWindow: 200000, MaxTokens: 100000},
		{ID: "o4-mini", Name: "o4-mini", API: "openai-responses", Provider: "cloudflare-ai-gateway", BaseURL: "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/openai", Reasoning: true, Input: []string{"text", "image"}, CostInput: 1.1, CostOutput: 4.4, CostCacheRead: 0.28, CostCacheWrite: 0.0, ContextWindow: 200000, MaxTokens: 100000},
		{ID: "workers-ai/@cf/moonshotai/kimi-k2.5", Name: "Kimi K2.5", API: "openai-completions", Provider: "cloudflare-ai-gateway", BaseURL: "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/compat", Reasoning: true, Input: []string{"text", "image"}, CostInput: 0.6, CostOutput: 3.0, CostCacheRead: 0.1, CostCacheWrite: 0.0, ContextWindow: 256000, MaxTokens: 256000},
		{ID: "workers-ai/@cf/moonshotai/kimi-k2.6", Name: "Kimi K2.6", API: "openai-completions", Provider: "cloudflare-ai-gateway", BaseURL: "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/compat", Reasoning: true, Input: []string{"text", "image"}, CostInput: 0.95, CostOutput: 4.0, CostCacheRead: 0.16, CostCacheWrite: 0.0, ContextWindow: 256000, MaxTokens: 256000},
		{ID: "workers-ai/@cf/nvidia/nemotron-3-120b-a12b", Name: "Nemotron 3 Super 120B", API: "openai-completions", Provider: "cloudflare-ai-gateway", BaseURL: "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/compat", Reasoning: true, Input: []string{"text"}, CostInput: 0.5, CostOutput: 1.5, CostCacheRead: 0.0, CostCacheWrite: 0.0, ContextWindow: 256000, MaxTokens: 256000},
		{ID: "workers-ai/@cf/zai-org/glm-4.7-flash", Name: "GLM-4.7-Flash", API: "openai-completions", Provider: "cloudflare-ai-gateway", BaseURL: "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/compat", Reasoning: true, Input: []string{"text"}, CostInput: 0.06, CostOutput: 0.4, CostCacheRead: 0.0, CostCacheWrite: 0.0, ContextWindow: 131072, MaxTokens: 131072},
		{ID: "mimo-v2-flash", Name: "MiMo-V2-Flash", API: "anthropic-messages", Provider: "xiaomi", BaseURL: "https://token-plan-ams.xiaomimimo.com/anthropic", Reasoning: true, Input: []string{"text"}, CostInput: 0.1, CostOutput: 0.3, CostCacheRead: 0.01, CostCacheWrite: 0.0, ContextWindow: 256000, MaxTokens: 64000},
		{ID: "mimo-v2-omni", Name: "MiMo-V2-Omni", API: "anthropic-messages", Provider: "xiaomi", BaseURL: "https://token-plan-ams.xiaomimimo.com/anthropic", Reasoning: true, Input: []string{"text", "image"}, CostInput: 0.4, CostOutput: 2.0, CostCacheRead: 0.08, CostCacheWrite: 0.0, ContextWindow: 256000, MaxTokens: 128000},
		{ID: "mimo-v2-pro", Name: "MiMo-V2-Pro", API: "anthropic-messages", Provider: "xiaomi", BaseURL: "https://token-plan-ams.xiaomimimo.com/anthropic", Reasoning: true, Input: []string{"text"}, CostInput: 1.0, CostOutput: 3.0, CostCacheRead: 0.2, CostCacheWrite: 0.0, ContextWindow: 1000000, MaxTokens: 128000},
		{ID: "mimo-v2.5", Name: "MiMo-V2.5", API: "anthropic-messages", Provider: "xiaomi", BaseURL: "https://token-plan-ams.xiaomimimo.com/anthropic", Reasoning: true, Input: []string{"text"}, CostInput: 0.4, CostOutput: 2.0, CostCacheRead: 0.08, CostCacheWrite: 0.0, ContextWindow: 1048576, MaxTokens: 131072},
		{ID: "mimo-v2.5-pro", Name: "MiMo-V2.5-Pro", API: "anthropic-messages", Provider: "xiaomi", BaseURL: "https://token-plan-ams.xiaomimimo.com/anthropic", Reasoning: true, Input: []string{"text", "image"}, CostInput: 1.0, CostOutput: 3.0, CostCacheRead: 0.2, CostCacheWrite: 0.0, ContextWindow: 1048576, MaxTokens: 131072},
		{ID: "kimi-k2-0711-preview", Name: "Kimi K2 0711", API: "openai-completions", Provider: "moonshotai", BaseURL: "https://api.moonshot.ai/v1", Reasoning: false, Input: []string{"text"}, CostInput: 0.6, CostOutput: 2.5, CostCacheRead: 0.15, CostCacheWrite: 0.0, ContextWindow: 131072, MaxTokens: 16384},
		{ID: "kimi-k2-0905-preview", Name: "Kimi K2 0905", API: "openai-completions", Provider: "moonshotai", BaseURL: "https://api.moonshot.ai/v1", Reasoning: false, Input: []string{"text"}, CostInput: 0.6, CostOutput: 2.5, CostCacheRead: 0.15, CostCacheWrite: 0.0, ContextWindow: 262144, MaxTokens: 262144},
		{ID: "kimi-k2-thinking", Name: "Kimi K2 Thinking", API: "openai-completions", Provider: "moonshotai", BaseURL: "https://api.moonshot.ai/v1", Reasoning: true, Input: []string{"text"}, CostInput: 0.6, CostOutput: 2.5, CostCacheRead: 0.15, CostCacheWrite: 0.0, ContextWindow: 262144, MaxTokens: 262144},
		{ID: "kimi-k2-thinking-turbo", Name: "Kimi K2 Thinking Turbo", API: "openai-completions", Provider: "moonshotai", BaseURL: "https://api.moonshot.ai/v1", Reasoning: true, Input: []string{"text"}, CostInput: 1.15, CostOutput: 8.0, CostCacheRead: 0.15, CostCacheWrite: 0.0, ContextWindow: 262144, MaxTokens: 262144},
		{ID: "kimi-k2-turbo-preview", Name: "Kimi K2 Turbo", API: "openai-completions", Provider: "moonshotai", BaseURL: "https://api.moonshot.ai/v1", Reasoning: false, Input: []string{"text"}, CostInput: 2.4, CostOutput: 10.0, CostCacheRead: 0.6, CostCacheWrite: 0.0, ContextWindow: 262144, MaxTokens: 262144},
		{ID: "kimi-k2.5", Name: "Kimi K2.5", API: "openai-completions", Provider: "moonshotai", BaseURL: "https://api.moonshot.ai/v1", Reasoning: true, Input: []string{"text", "image"}, CostInput: 0.6, CostOutput: 3.0, CostCacheRead: 0.1, CostCacheWrite: 0.0, ContextWindow: 262144, MaxTokens: 262144},
		{ID: "kimi-k2.6", Name: "Kimi K2.6", API: "openai-completions", Provider: "moonshotai", BaseURL: "https://api.moonshot.ai/v1", Reasoning: true, Input: []string{"text", "image"}, CostInput: 0.95, CostOutput: 4.0, CostCacheRead: 0.16, CostCacheWrite: 0.0, ContextWindow: 262144, MaxTokens: 262144},
		{ID: "kimi-k2-0711-preview", Name: "Kimi K2 0711", API: "openai-completions", Provider: "moonshotai-cn", BaseURL: "https://api.moonshot.cn/v1", Reasoning: false, Input: []string{"text"}, CostInput: 0.6, CostOutput: 2.5, CostCacheRead: 0.15, CostCacheWrite: 0.0, ContextWindow: 131072, MaxTokens: 16384},
		{ID: "kimi-k2-0905-preview", Name: "Kimi K2 0905", API: "openai-completions", Provider: "moonshotai-cn", BaseURL: "https://api.moonshot.cn/v1", Reasoning: false, Input: []string{"text"}, CostInput: 0.6, CostOutput: 2.5, CostCacheRead: 0.15, CostCacheWrite: 0.0, ContextWindow: 262144, MaxTokens: 262144},
		{ID: "kimi-k2-thinking", Name: "Kimi K2 Thinking", API: "openai-completions", Provider: "moonshotai-cn", BaseURL: "https://api.moonshot.cn/v1", Reasoning: true, Input: []string{"text"}, CostInput: 0.6, CostOutput: 2.5, CostCacheRead: 0.15, CostCacheWrite: 0.0, ContextWindow: 262144, MaxTokens: 262144},
		{ID: "kimi-k2-thinking-turbo", Name: "Kimi K2 Thinking Turbo", API: "openai-completions", Provider: "moonshotai-cn", BaseURL: "https://api.moonshot.cn/v1", Reasoning: true, Input: []string{"text"}, CostInput: 1.15, CostOutput: 8.0, CostCacheRead: 0.15, CostCacheWrite: 0.0, ContextWindow: 262144, MaxTokens: 262144},
		{ID: "kimi-k2-turbo-preview", Name: "Kimi K2 Turbo", API: "openai-completions", Provider: "moonshotai-cn", BaseURL: "https://api.moonshot.cn/v1", Reasoning: false, Input: []string{"text"}, CostInput: 2.4, CostOutput: 10.0, CostCacheRead: 0.6, CostCacheWrite: 0.0, ContextWindow: 262144, MaxTokens: 262144},
		{ID: "kimi-k2.5", Name: "Kimi K2.5", API: "openai-completions", Provider: "moonshotai-cn", BaseURL: "https://api.moonshot.cn/v1", Reasoning: true, Input: []string{"text", "image"}, CostInput: 0.6, CostOutput: 3.0, CostCacheRead: 0.1, CostCacheWrite: 0.0, ContextWindow: 262144, MaxTokens: 262144},
		{ID: "kimi-k2.6", Name: "Kimi K2.6", API: "openai-completions", Provider: "moonshotai-cn", BaseURL: "https://api.moonshot.cn/v1", Reasoning: true, Input: []string{"text", "image"}, CostInput: 0.95, CostOutput: 4.0, CostCacheRead: 0.16, CostCacheWrite: 0.0, ContextWindow: 262144, MaxTokens: 262144},
	}
	for _, nm := range newProviderModels {
		if !hasModel(all, nm.Provider, nm.ID) {
			all = append(all, nm)
		}
	}

	// --- Mistral medium 3.5 (added upstream in v0.71.0) ---
	if !hasModel(all, "mistral", "mistral-medium-3.5") {
		all = append(all, modelSpec{
			ID: "mistral-medium-3.5", Name: "Mistral Medium 3.5",
			API: "mistral-conversations", Provider: "mistral",
			BaseURL: "https://api.mistral.ai", Reasoning: true,
			Input:     []string{"text", "image"},
			CostInput: 1.5, CostOutput: 7.5, CostCacheRead: 0, CostCacheWrite: 0,
			ContextWindow: 262144, MaxTokens: 262144,
		})
	}

	return all
}

// deduplicate groups models by provider/id and returns deduped+sorted results.
// models.dev has priority over OpenRouter/AI Gateway (earlier entries win).
func deduplicate(all []modelSpec) []modelSpec {
	// Add Azure OpenAI models derived from OpenAI openai-responses models
	var azureModels []modelSpec
	for _, m := range all {
		if m.Provider == "openai" && m.API == "openai-responses" {
			az := m
			az.API = "azure-openai-responses"
			az.Provider = "azure-openai-responses"
			az.BaseURL = ""
			azureModels = append(azureModels, az)
		}
	}
	all = append(all, azureModels...)

	// Deduplicate: first seen wins
	type key struct{ provider, id string }
	seen := make(map[key]bool)
	var unique []modelSpec
	for _, m := range all {
		k := key{m.Provider, m.ID}
		if !seen[k] {
			seen[k] = true
			unique = append(unique, m)
		}
	}

	// Sort by provider then model ID for deterministic output
	sort.Slice(unique, func(i, j int) bool {
		if unique[i].Provider != unique[j].Provider {
			return unique[i].Provider < unique[j].Provider
		}
		return unique[i].ID < unique[j].ID
	})
	return unique
}

// formatFloat formats a float64 for Go source output.
// It rounds to 8 decimal places to avoid floating-point noise from
// per-token price arithmetic (e.g. 0.2 × 1e6 → 0.19999999999999998).
func formatFloat(f float64) string {
	rounded := math.Round(f*1e8) / 1e8
	return fmt.Sprintf("%g", rounded)
}

// goString escapes a string for a Go string literal (double-quoted).
func goString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// renderCompat renders a compatSpec as a Go expression.
func renderCompat(c *compatSpec) string {
	if c == nil {
		return "nil"
	}
	var fields []string
	if c.SupportsStore != nil {
		fields = append(fields, fmt.Sprintf("SupportsStore: boolRef(%v)", *c.SupportsStore))
	}
	if c.SupportsDeveloperRole != nil {
		fields = append(fields, fmt.Sprintf("SupportsDeveloperRole: boolRef(%v)", *c.SupportsDeveloperRole))
	}
	if c.SupportsReasoningEffort != nil {
		fields = append(fields, fmt.Sprintf("SupportsReasoningEffort: boolRef(%v)", *c.SupportsReasoningEffort))
	}
	if c.ThinkingFormat != "" {
		fields = append(fields, fmt.Sprintf("ThinkingFormat: %s", goString(c.ThinkingFormat)))
	}
	if c.ZaiToolStream != nil {
		fields = append(fields, fmt.Sprintf("ZaiToolStream: boolRef(%v)", *c.ZaiToolStream))
	}
	if c.RequiresReasoningContentOnAssistantMessages != nil {
		fields = append(fields, fmt.Sprintf("RequiresReasoningContentOnAssistantMessages: boolRef(%v)", *c.RequiresReasoningContentOnAssistantMessages))
	}
	if len(c.ReasoningEffortMap) > 0 {
		pairs := make([]string, 0, len(c.ReasoningEffortMap))
		for k, v := range c.ReasoningEffortMap {
			pairs = append(pairs, fmt.Sprintf("%s: %s", goString(k), goString(v)))
		}
		sort.Strings(pairs)
		fields = append(fields, "ReasoningEffortMap: map[string]string{"+strings.Join(pairs, ", ")+"}")
	}
	if len(fields) == 0 {
		return "nil"
	}
	return "&OpenAICompletionsCompat{" + strings.Join(fields, ", ") + "}"
}

// renderHeaders renders a map[string]string as a Go map literal or nil.
func renderHeaders(h map[string]string) string {
	if len(h) == 0 {
		return "nil"
	}
	// Sort keys for deterministic output
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s: %s", goString(k), goString(h[k])))
	}
	return "map[string]string{" + strings.Join(parts, ", ") + "}"
}

// renderInput renders a []string as a Go string slice literal.
func renderInput(input []string) string {
	quoted := make([]string, len(input))
	for i, s := range input {
		quoted[i] = goString(s)
	}
	return "[]string{" + strings.Join(quoted, ", ") + "}"
}

func renderStringSlice(items []string) string {
	quoted := make([]string, len(items))
	for i, s := range items {
		quoted[i] = goString(s)
	}
	return "[]string{" + strings.Join(quoted, ", ") + "}"
}

// generateGoSource generates the complete Go source file from the model list.
func generateGoSource(models []modelSpec) string {
	var sb strings.Builder
	sb.WriteString("// Code generated by cmd/generate-models. DO NOT EDIT.\n")
	sb.WriteString(fmt.Sprintf("// Generated at: %s\n", time.Now().UTC().Format(time.RFC3339)))
	sb.WriteString("package ai\n\n")
	sb.WriteString("func init() {\n")

	for _, m := range models {
		sb.WriteString("\tRegisterModel(&Model{\n")
		sb.WriteString(fmt.Sprintf("\t\tID:            %s,\n", goString(m.ID)))
		sb.WriteString(fmt.Sprintf("\t\tName:          %s,\n", goString(m.Name)))
		sb.WriteString(fmt.Sprintf("\t\tApi:           %s,\n", goString(m.API)))
		sb.WriteString(fmt.Sprintf("\t\tProvider:      %s,\n", goString(m.Provider)))
		sb.WriteString(fmt.Sprintf("\t\tBaseURL:       %s,\n", goString(m.BaseURL)))
		sb.WriteString(fmt.Sprintf("\t\tReasoning:     %v,\n", m.Reasoning))
		sb.WriteString(fmt.Sprintf("\t\tInput:         %s,\n", renderInput(m.Input)))
		sb.WriteString(fmt.Sprintf("\t\tCost:          ModelCost{Input: %s, Output: %s, CacheRead: %s, CacheWrite: %s},\n",
			formatFloat(m.CostInput), formatFloat(m.CostOutput),
			formatFloat(m.CostCacheRead), formatFloat(m.CostCacheWrite)))
		sb.WriteString(fmt.Sprintf("\t\tContextWindow: %d,\n", m.ContextWindow))
		sb.WriteString(fmt.Sprintf("\t\tMaxTokens:     %d,\n", m.MaxTokens))
		sb.WriteString(fmt.Sprintf("\t\tHeaders:       %s,\n", renderHeaders(m.Headers)))
		compat := renderCompat(m.Compat)
		if compat != "nil" {
			sb.WriteString(fmt.Sprintf("\t\tCompat:        %s,\n", compat))
		}
		if len(m.ServerTools) > 0 {
			sb.WriteString(fmt.Sprintf("\t\tServerTools:   %s,\n", renderStringSlice(m.ServerTools)))
		}
		if m.Compaction {
			sb.WriteString("\t\tCompaction:    true,\n")
		}
		if m.SWEScore > 0 {
			sb.WriteString(fmt.Sprintf("\t\tSWEScore:      %s,\n", formatFloat(m.SWEScore)))
		}
		if m.SWEInferred {
			sb.WriteString("\t\tSWEInferred:   true,\n")
		}
		if len(m.ReasoningEffortValues) > 0 {
			sb.WriteString(fmt.Sprintf("\t\tReasoningEffortValues: %s,\n", renderStringSlice(m.ReasoningEffortValues)))
		}
		sb.WriteString("\t})\n")
	}

	sb.WriteString("}\n")
	return sb.String()
}

// repoRoot returns the root of the fir repository (parent of cmd/generate-models).
func repoRoot() string {
	_, filename, _, _ := runtime.Caller(0)
	// filename is .../cmd/generate-models/main.go
	return filepath.Join(filepath.Dir(filename), "..", "..")
}

func main() {
	defaultOut := filepath.Join(repoRoot(), "pkg", "ai", "models_generated.go")
	out := flag.String("out", defaultOut, "output file path")
	flag.Parse()

	// Fetch from all sources
	modelsDevModels, err := loadModelsDevData()
	if err != nil {
		log.Fatalf("models.dev: %v", err)
	}

	openRouterModels, err := fetchOpenRouterModels()
	if err != nil {
		log.Printf("Warning: OpenRouter fetch failed: %v", err)
	}

	aiGatewayModels, err := fetchAIGatewayModels()
	if err != nil {
		log.Printf("Warning: AI Gateway fetch failed: %v", err)
	}

	poeModels, err := fetchPoeModels()
	if err != nil {
		log.Printf("Warning: Poe fetch failed: %v", err)
	}

	// Fetch SWE-bench Verified leaderboard for model capability ordering.
	// Failure is non-fatal; curated baseline scores are used instead.
	sweScores := fetchSWEBenchScores()

	// Combine: models.dev first (takes priority during dedup)
	all := append(modelsDevModels, openRouterModels...)
	all = append(all, aiGatewayModels...)
	all = append(all, poeModels...)

	// Apply manual overrides and additions
	all = applyOverridesAndAdditions(all)

	// Deduplicate and sort
	all = deduplicate(all)

	// Annotate models with SWE-bench Verified scores for capability ordering.
	all = applySWEScores(all, sweScores)

	// Propagate scores to unbenched models via family lineage inheritance.
	all = inferSWEScores(all)

	// Generate Go source
	source := generateGoSource(all)

	// Write output
	if err := os.WriteFile(*out, []byte(source), 0o644); err != nil {
		log.Fatalf("write %s: %v", *out, err)
	}

	log.Printf("Generated %s", *out)

	// Print statistics
	reasoningCount := 0
	sweCount := 0
	sweInferredCount := 0
	for _, m := range all {
		if m.Reasoning {
			reasoningCount++
		}
		if m.SWEScore > 0 {
			sweCount++
			if m.SWEInferred {
				sweInferredCount++
			}
		}
	}
	log.Printf("Model Statistics:")
	log.Printf("  Total models: %d", len(all))
	log.Printf("  Reasoning-capable models: %d", reasoningCount)
	log.Printf("  Models with SWE-bench scores: %d (%d verified, %d inferred)", sweCount, sweCount-sweInferredCount, sweInferredCount)

	// Per-provider counts
	providerCounts := make(map[string]int)
	for _, m := range all {
		providerCounts[m.Provider]++
	}
	providerNames := make([]string, 0, len(providerCounts))
	for p := range providerCounts {
		providerNames = append(providerNames, p)
	}
	sort.Strings(providerNames)
	for _, p := range providerNames {
		log.Printf("  %s: %d models", p, providerCounts[p])
	}
}
