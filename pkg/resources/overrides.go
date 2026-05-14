package resources

import (
	"fmt"
	"sort"
	"strings"
)

// Overridable is the interface a resource type must satisfy to participate
// in the generic coexistence + override resolution used by skills and
// extensions.
//
//   - GetID:       canonical disambiguated ID, of the form `<sanitized-origin>__<name>`
//   - GetName:     bare name (the shared "key" two resources can clash on)
//   - GetOrigin:   origin label ("builtin", "user", "project", "pkg:<src>", "path:<base>", ...)
//   - GetOverride: frontmatter `override` value — "", "true", or a full ID/origin-form target
//   - GetPath:     file path used to decorate diagnostics; "" is acceptable
type Overridable interface {
	GetID() string
	GetName() string
	GetOrigin() string
	GetOverride() string
	GetPath() string
}

// ResolveOverrides applies override semantics over `items` and returns a
// parallel keepMask indicating which items survive, an overriddenBy map
// (surviving-index → sorted IDs the survivor displaced), and diagnostics.
//
// `kind` is substituted into diagnostic messages (e.g. "skill", "extension").
//
// Rules (identical to the skill-specific implementation that preceded it):
//
//   - `override: <full-id>` — replace exactly one item by ID. The target may
//     be written in raw origin form (e.g. "path:foo__bar") or in already-
//     sanitized form ("path_foo__bar"); both resolve.
//   - `override: true` — replace any other same-named item. On conflict
//     (multiple `override: true` for the same name) the highest-precedence
//     origin wins (see originPrecedence); ties go to the earliest index.
//   - Items not targeted survive and coexist with their same-named peers.
func ResolveOverrides[T Overridable](items []T, kind string) (keep []bool, overriddenBy map[int][]string, diags []ResourceDiagnostic) {
	keep = make([]bool, len(items))
	for i := range keep {
		keep[i] = true
	}
	overriddenBy = make(map[int][]string)

	// Index items by ID for explicit-target overrides.
	byID := make(map[string]int, len(items))
	for i, it := range items {
		byID[it.GetID()] = i
	}

	type killEntry struct {
		killerID  string
		killerIdx int
	}
	killed := make(map[string]killEntry)

	// First pass: explicit-ID overrides.
	for i, it := range items {
		ovr := it.GetOverride()
		if ovr == "" || ovr == "true" {
			continue
		}
		victimIdx, ok := byID[ovr]
		if !ok {
			if cut := strings.LastIndex(ovr, "__"); cut > 0 {
				sanitized := SanitizeOriginForID(ovr[:cut]) + "__" + ovr[cut+2:]
				if vi, ok2 := byID[sanitized]; ok2 {
					victimIdx, ok = vi, true
				}
			}
		}
		if !ok {
			diags = append(diags, ResourceDiagnostic{
				Type:    "warning",
				Path:    it.GetPath(),
				Message: fmt.Sprintf("%s %q declares override: %q but no such %s is loaded", kind, it.GetID(), ovr, kind),
			})
			continue
		}
		if victimIdx == i {
			diags = append(diags, ResourceDiagnostic{
				Type:    "warning",
				Path:    it.GetPath(),
				Message: fmt.Sprintf("%s %q cannot override itself", kind, it.GetID()),
			})
			continue
		}
		killed[items[victimIdx].GetID()] = killEntry{killerID: it.GetID(), killerIdx: i}
	}

	// Second pass: `override: true`. Group by name; among `override:true`
	// claimants, highest-precedence wins; winner shadows every other
	// same-named item.
	byName := make(map[string][]int, len(items))
	for i, it := range items {
		byName[it.GetName()] = append(byName[it.GetName()], i)
	}
	for name, idxs := range byName {
		if len(idxs) < 2 {
			continue
		}
		var claimants []int
		for _, i := range idxs {
			if _, dead := killed[items[i].GetID()]; dead {
				continue
			}
			if items[i].GetOverride() == "true" {
				claimants = append(claimants, i)
			}
		}
		if len(claimants) == 0 {
			continue
		}
		winner := claimants[0]
		for _, c := range claimants[1:] {
			if originPrecedence(items[c].GetOrigin()) > originPrecedence(items[winner].GetOrigin()) {
				winner = c
			}
		}
		if len(claimants) > 1 {
			var others []string
			for _, c := range claimants {
				if c != winner {
					others = append(others, items[c].GetID())
				}
			}
			diags = append(diags, ResourceDiagnostic{
				Type:    "override-conflict",
				Path:    items[winner].GetPath(),
				Message: fmt.Sprintf("multiple %ss claim override of %q; %q won (others: %s)", kind, name, items[winner].GetID(), strings.Join(others, ", ")),
			})
		}
		for _, i := range idxs {
			if i == winner {
				continue
			}
			if _, already := killed[items[i].GetID()]; already {
				continue
			}
			killed[items[i].GetID()] = killEntry{killerID: items[winner].GetID(), killerIdx: winner}
		}
	}

	for victimID, k := range killed {
		overriddenBy[k.killerIdx] = append(overriddenBy[k.killerIdx], victimID)
	}
	for idx := range overriddenBy {
		sort.Strings(overriddenBy[idx])
	}
	for i, it := range items {
		if k, dead := killed[it.GetID()]; dead {
			keep[i] = false
			diags = append(diags, ResourceDiagnostic{
				Type:    "shadowed",
				Path:    it.GetPath(),
				Message: fmt.Sprintf("%s %q shadowed by override from %q", kind, it.GetID(), k.killerID),
			})
		}
	}
	return keep, overriddenBy, diags
}
