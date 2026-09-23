package main

import (
	"fmt"
	"io"
	"os"

	"github.com/kfet/fir/pkg/models"
)

func runDoctor() error {
	args := os.Args[2:] // skip "fir doctor"
	if len(args) == 1 && args[0] == "client-version-gates" {
		return runDoctorClientVersionGates(os.Stdout, resolveAgentDir())
	}
	return fmt.Errorf("usage: fir doctor client-version-gates")
}

// runDoctorClientVersionGates prints one line per UNRESOLVED
// client-version-gate record in this host's doctor log, judged against this
// host's effective pin (embedded floor + cached catalog overlay; no network).
// It prints nothing when there is nothing to report, so fleet converge can
// simply test for non-empty output.
func runDoctorClientVersionGates(w io.Writer, agentDir string) error {
	recs, err := models.ReadGateRecords(models.DoctorLogPath(agentDir))
	if err != nil {
		return err
	}
	effective := func(key string) string { return models.LocalClientVersion(agentDir, key) }
	for _, g := range models.UnresolvedGates(recs, effective) {
		if _, err := fmt.Fprintln(w, g.ReportLine()); err != nil {
			return err
		}
	}
	return nil
}
