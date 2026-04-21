package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kfet/fir/pkg/session/store"
)

// runSessions implements the "fir sessions" subcommand.
//
// Usage:
//
//	fir sessions [list]   — list sessions associated with the current working directory
func runSessions() error {
	args := os.Args[2:] // skip "fir sessions"

	if len(args) == 0 || args[0] == "list" {
		return runSessionsList()
	}

	return fmt.Errorf("unknown sessions subcommand: %s\nUsage: fir sessions [list]", args[0])
}

func runSessionsList() error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}
	agentDir := resolveAgentDir()
	sessionDir := store.SessionDirForCwd(agentDir, cwd)

	sessions, err := store.ListSessions(cwd, sessionDir)
	if err != nil {
		return fmt.Errorf("list sessions: %w", err)
	}

	if len(sessions) == 0 {
		fmt.Fprintf(os.Stderr, "No sessions found for %s\n", cwd)
		return nil
	}

	// Header + rows. Compute widths.
	idW := 2
	nameW := 4
	for _, s := range sessions {
		id := filepath.Base(s.Path)
		id = strings.TrimSuffix(id, ".jsonl")
		if len(id) > idW {
			idW = len(id)
		}
		if len(s.Name) > nameW {
			nameW = len(s.Name)
		}
	}
	if nameW > 40 {
		nameW = 40
	}

	fmt.Printf("%-*s  %-19s  %4s  %-*s  %s\n", idW, "ID", "MODIFIED", "MSGS", nameW, "NAME", "FIRST MESSAGE")
	for _, s := range sessions {
		id := strings.TrimSuffix(filepath.Base(s.Path), ".jsonl")
		name := s.Name
		if len(name) > nameW {
			name = name[:nameW-1] + "…"
		}
		first := strings.ReplaceAll(s.FirstMessage, "\n", " ")
		if len(first) > 60 {
			first = first[:57] + "..."
		}
		fmt.Printf("%-*s  %-19s  %4d  %-*s  %s\n",
			idW, id,
			s.Modified.Local().Format(time.DateTime),
			s.MessageCount,
			nameW, name,
			first,
		)
	}
	return nil
}
