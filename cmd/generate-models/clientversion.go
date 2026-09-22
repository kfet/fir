package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/kfet/fir/pkg/models"
)

// The Claude Code version pin rides the same nightly model-watch PR as the
// models that need it. Anthropic gates new models on the version the client
// advertises, so a gated model without its pin is a dead catalog entry —
// splitting them across two PRs makes it possible to merge one without the
// other. They are therefore one write, one validation, one review.

// claudeCodeNPMURL is the package ROOT, deliberately not the `/latest` alias
// (which has been flaky). dist-tags.latest on the root document is the same
// value from a stable path.
const claudeCodeNPMURL = "https://registry.npmjs.org/@anthropic-ai/claude-code"

// claudeCodeSource is the name this source reports into sources_failed.
const claudeCodeSource = "npm:claude-code"

// clientVersionAnomalyMinorJump is the "look twice" threshold: a pin moving by
// more than this many minor versions in one night, or across a major, is
// surfaced as an anomaly. The PR is still proposed — a human merges every one
// of them anyway — it just says so out loud.
const clientVersionAnomalyMinorJump = 1

// fetchClaudeCodeVersion reads dist-tags.latest from the npm registry and
// holds it to exactly the grammar the catalog loader will enforce, so a
// version that could never be published never reaches a PR.
func fetchClaudeCodeVersion() (string, error) {
	return fetchClaudeCodeVersionFrom(claudeCodeNPMURL)
}

// fetchClaudeCodeVersionFrom is fetchClaudeCodeVersion against an explicit
// URL, so tests can drive it with an httptest server.
func fetchClaudeCodeVersionFrom(url string) (string, error) {
	var doc struct {
		DistTags map[string]string `json:"dist-tags"`
	}
	if err := fetchJSON(url, &doc); err != nil {
		return "", err
	}
	v := doc.DistTags["latest"]
	if v == "" {
		return "", fmt.Errorf("npm document has no dist-tags.latest")
	}
	if len(v) > models.ClientVersionMaxLen || !models.ClientVersionRE.MatchString(v) {
		return "", fmt.Errorf("npm latest %q is not a plain dotted version", v)
	}
	return v, nil
}

// clientVersionBump decides what (if anything) to write. Forward only: a
// latest that is not strictly newer than the committed pin is no change at
// all. Never write a lower number — the loader would ignore it anyway (the
// embedded value is a floor), and a downgrade in the document is pure noise.
//
// Returns the bump description ("2.1.280 → 2.1.291") and an anomaly note, both
// empty when there is nothing to do.
func clientVersionBump(current, latest string) (bump, anomaly string) {
	if latest == "" || (current != "" && models.CompareClientVersions(latest, current) <= 0) {
		return "", ""
	}
	bump = fmt.Sprintf("%s → %s", current, latest)
	if current == "" {
		return bump, ""
	}
	cur, next := dottedParts(current), dottedParts(latest)
	switch {
	case len(cur) > 0 && len(next) > 0 && next[0] != cur[0]:
		anomaly = fmt.Sprintf("claude-code pin crosses a major version (%s)", bump)
	case len(cur) > 1 && len(next) > 1 && next[1]-cur[1] > clientVersionAnomalyMinorJump:
		anomaly = fmt.Sprintf("claude-code pin jumps %d minor versions (%s)", next[1]-cur[1], bump)
	}
	return bump, anomaly
}

// dottedParts splits a validated dotted version into ints.
func dottedParts(v string) []int {
	fields := strings.Split(v, ".")
	out := make([]int, 0, len(fields))
	for _, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil {
			return out
		}
		out = append(out, n)
	}
	return out
}

// overlayClaudeCodeVersion reads the committed overlay's current pin. An
// unreadable or invalid overlay yields "" — updateOverlay re-reads and fails
// loudly on the same file, so this stays silent.
func overlayClaudeCodeVersion(path string) string {
	if path == "" {
		return ""
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) == 0 {
		return ""
	}
	o, err := models.ParseCatalogOverlay(raw)
	if err != nil {
		return ""
	}
	return o.ClientVersions.Get(models.ClientVersionKeyClaudeCode)
}
