// Package modes_test holds cross-mode tests.
package modes_test

import (
	"testing"

	"github.com/kfet/fir/pkg/modes/acp"
	"github.com/kfet/fir/pkg/modes/interactive"
	"github.com/kfet/fir/pkg/slashcmd"
)

// TestCommandParity fails when a builtin command or subcommand exists in one
// mode but not another without being marked mode-only in slashcmd.Specs.
// Every mode's registry must bind a handler for every command and subcommand
// declared for it; since commands not marked otherwise are declared for all
// modes, a command added to only one adapter fails here.
func TestCommandParity(t *testing.T) {
	regs := map[slashcmd.Mode]*slashcmd.Registry{
		slashcmd.TUI: interactive.CommandRegistry(),
		slashcmd.ACP: acp.CommandRegistry(),
	}
	for _, mode := range slashcmd.Modes {
		r, ok := regs[mode]
		if !ok {
			t.Fatalf("no registry for mode %s; add it to this test", mode)
		}
		if r.Mode() != mode {
			t.Fatalf("registry for %s reports mode %s", mode, r.Mode())
		}
		if err := r.Validate(); err != nil {
			t.Errorf("%s mode is missing commands:\n%v", mode, err)
		}
	}
	// And the reverse: a mode-only handler bound in a mode the spec does not
	// declare is rejected by Registry.Bind, so building the registries above
	// already proved it. Assert the shared surface is genuinely shared.
	for _, s := range slashcmd.Specs {
		for _, mode := range slashcmd.Modes {
			if s.Available(mode) != regs[mode].Has(s.Name) {
				t.Errorf("/%s: declared in %s = %v, bound = %v", s.Name, mode, s.Available(mode), regs[mode].Has(s.Name))
			}
			for _, sub := range s.Subs {
				path := s.Name + " " + sub.Name
				if s.Available(mode) != regs[mode].Has(path) {
					t.Errorf("/%s: declared in %s = %v, bound = %v", path, mode, s.Available(mode), regs[mode].Has(path))
				}
			}
		}
	}
}
