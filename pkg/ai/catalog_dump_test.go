package ai

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// dumpRegistry renders every registered model deterministically, including
// nil-vs-empty distinctions and the pointed-to compat values.
func dumpRegistry() string {
	ensureCatalog()
	modelRegistryMu.RLock()
	defer modelRegistryMu.RUnlock()
	var lines []string
	for p, ms := range modelRegistry {
		for id, m := range ms {
			c := *m
			c.Compat = nil
			compat := "nil"
			if m.Compat != nil {
				cp, ok := m.Compat.(*OpenAICompletionsCompat)
				if !ok {
					compat = fmt.Sprintf("UNEXPECTED %T", m.Compat)
				} else {
					cc := *cp
					b, _ := json.Marshal(cc)
					compat = fmt.Sprintf("%T%s", m.Compat, b)
					// pointer fields: print values explicitly
					for _, f := range []*bool{cc.SupportsStore, cc.SupportsDeveloperRole, cc.SupportsReasoningEffort, cc.ZaiToolStream, cc.RequiresReasoningContentOnAssistantMessages} {
						if f == nil {
							compat += " nil"
						} else {
							compat += fmt.Sprintf(" %v", *f)
						}
					}
					compat += fmt.Sprintf(" mapnil=%v", cc.ReasoningEffortMap == nil)
				}
			}
			lines = append(lines, fmt.Sprintf("%s/%s %#v compat=%s", p, id, c, compat))
		}
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n") + "\n"
}
