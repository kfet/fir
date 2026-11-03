package main

import (
	"fmt"
	"os"

	"github.com/kfet/fir/pkg/config"
	"github.com/kfet/fir/pkg/session/store"
)

// configureSessionStores registers fir's own session store and the extra
// read-only stores from FIR_SESSION_STORES / the global sessionStores
// setting. It runs before any subcommand or session opens, so every mode
// and every `fir sessions` verb sees the same set. Bad entries are skipped
// with a warning on stderr; they never stop fir.
func configureSessionStores() {
	cwd, _ := os.Getwd()
	agentDir := resolveAgentDir()
	extras, warnings := config.LoadSessionStores(cwd, agentDir)
	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "fir: warning: %s\n", w)
	}
	stores := make([]store.Store, 0, len(extras))
	for _, e := range extras {
		stores = append(stores, store.Store{Label: e.Label, Path: e.Path, ReadOnly: true})
	}
	root := store.SessionsDir(agentDir)
	for _, s := range store.ConfigureStores(root, stores) {
		fmt.Fprintf(os.Stderr, "fir: warning: sessionStores: %q (%s) overlaps fir's own session store %s; skipped\n", s.Label, s.Path, root)
	}
}
