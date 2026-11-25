package session

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// RequestResourceReload schedules a reload of skills, themes, the system
// prompt and the model registry. It is deferred: the reload runs once when
// the current turn ends (repeated requests collapse), or immediately when
// the session is idle. Unlike Reload it does not re-read settings (so an
// agent cannot widen its own extension allow-list), restart extensions,
// reload MCP servers or force a catalog-overlay refetch. A short summary of
// what changed is injected as a [SYS_EXT] message for the next turn.
func (s *AgentSession) RequestResourceReload() {
	s.resourceReloadPending.Store(true)
	if !s.Agent.State().IsStreaming {
		s.runPendingResourceReload()
	}
}

// runPendingResourceReload performs a pending RequestResourceReload, if any.
func (s *AgentSession) runPendingResourceReload() {
	if !s.resourceReloadPending.Swap(false) {
		return
	}
	if msg := s.reloadResources(); msg != "" {
		s.PrependContext(msg)
	}
}

// reloadResources does the work and returns the change summary.
func (s *AgentSession) reloadResources() string {
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()
	before := s.skillNames()
	diagsBefore := s.skillDiagnostics()
	modelsBefore := s.modelKeys()

	var errs []string
	if err := s.resourceLoader.Reload(); err != nil {
		errs = append(errs, "resources: "+err.Error())
	} else {
		// Keep the old prompt when nothing changed so the provider's
		// prompt cache stays valid.
		s.rebuildSystemPrompt(false)
	}
	// Only new diagnostics: standing warnings would repeat on every reload.
	newDiags, _ := diffSets(diagsBefore, s.skillDiagnostics())
	errs = append(errs, newDiags...)
	if s.modelRegistry != nil {
		s.modelRegistry.Refresh()
		s.modelRegistry.RefreshLive(context.Background())
	}

	skAdd, skDel := diffSets(before, s.skillNames())
	mAdd, mDel := diffSets(modelsBefore, s.modelKeys())

	var b strings.Builder
	b.WriteString("Resources reloaded (skills, system prompt, model registry).")
	line := func(label string, items []string) {
		if len(items) > 0 {
			fmt.Fprintf(&b, "\n%s: %s", label, strings.Join(items, ", "))
		}
	}
	line("Skills added", skAdd)
	line("Skills removed", skDel)
	line("Models added", mAdd)
	line("Models removed", mDel)
	line("Errors", errs)
	if len(skAdd)+len(skDel)+len(mAdd)+len(mDel)+len(errs) == 0 {
		b.WriteString("\nNo skill or model changes.")
	}
	return b.String()
}

func (s *AgentSession) skillNames() map[string]struct{} {
	out := map[string]struct{}{}
	if s.resourceLoader == nil {
		return out
	}
	skills, _ := s.resourceLoader.GetSkills()
	for _, sk := range skills {
		out[sk.Name] = struct{}{}
	}
	return out
}

func (s *AgentSession) skillDiagnostics() map[string]struct{} {
	out := map[string]struct{}{}
	if s.resourceLoader == nil {
		return out
	}
	_, diags := s.resourceLoader.GetSkills()
	for _, d := range diags {
		if d.Type == "error" || d.Type == "warning" {
			out["skill "+d.Type+": "+d.Message] = struct{}{}
		}
	}
	return out
}

func (s *AgentSession) modelKeys() map[string]struct{} {
	out := map[string]struct{}{}
	if s.modelRegistry == nil {
		return out
	}
	for _, m := range s.modelRegistry.GetAll() {
		out[m.Provider+"/"+m.ID] = struct{}{}
	}
	return out
}

// diffSets returns the sorted keys only in after (added) and only in before (removed).
func diffSets(before, after map[string]struct{}) (added, removed []string) {
	for k := range after {
		if _, ok := before[k]; !ok {
			added = append(added, k)
		}
	}
	for k := range before {
		if _, ok := after[k]; !ok {
			removed = append(removed, k)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}
