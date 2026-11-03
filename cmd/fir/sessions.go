package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/kfet/fir/pkg/session/store"
)

const sessionsUsage = `Usage:
  fir sessions [list] [--all | --cwd DIR] [--store LABEL | --read-only] [--limit N] [--json]
  fir sessions dirs [--json]
  fir sessions search PATTERN [--kind bookmark|transcript] [--cwd DIR] [--store LABEL] [--since DUR] [--limit N] [--per-file N] [-i] [--json]

Sessions come from every store: fir's own ("local") and each read-only
sessionStores entry (settings.json) or FIR_SESSION_STORES=label=path[,...].

Output is tab-separated, one record per line, no header (--json: one JSON
object per line):
  list    store  modified  id  messages  path  cwd  name  first_message
  dirs    store  path  readonly  exists
  search  store  kind  id  path:line  snippet
search is a Go regexp over bookmarks first, then transcripts, newest first
(ctx-* logs excluded). Transcripts are large: --kind bookmark is fast, a
full transcript scan of a big archive takes tens of seconds.`

// runSessions implements the "fir sessions" subcommand.
func runSessions() error {
	return runSessionsArgs(os.Args[2:], os.Stdout)
}

func runSessionsArgs(args []string, w io.Writer) error {
	verb := "list"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		verb, args = args[0], args[1:]
	}
	switch verb {
	case "list":
		return runSessionsList(args, w)
	case "dirs":
		return runSessionsDirs(args, w)
	case "search":
		return runSessionsSearch(args, w)
	case "help":
		fmt.Fprintln(w, sessionsUsage)
		return nil
	}
	return fmt.Errorf("unknown sessions subcommand: %s\n%s", verb, sessionsUsage)
}

func newSessionsFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet("fir sessions "+name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func parseSessionsFlags(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return fmt.Errorf("%s", sessionsUsage)
		}
		return fmt.Errorf("%v\n%s", err, sessionsUsage)
	}
	return nil
}

// oneLine flattens s to a single tab-free line, truncated to max runes.
func oneLine(s string, max int) string {
	s = strings.Join(strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
	if r := []rune(s); max > 0 && len(r) > max {
		s = string(r[:max-1]) + "…"
	}
	return s
}

func storeLabels(store string) []string {
	if store == "" {
		return nil
	}
	return []string{store}
}

func checkStoreLabel(label string) error {
	if label == "" {
		return nil
	}
	for _, s := range store.Stores() {
		if s.Label == label {
			return nil
		}
	}
	return fmt.Errorf("unknown store %q (see fir sessions dirs)", label)
}

// absCwd makes a --cwd value absolute: stores key projects by absolute path.
func absCwd(cwd string) (string, error) {
	if cwd == "" {
		return "", nil
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return "", fmt.Errorf("--cwd %s: %w", cwd, err)
	}
	return abs, nil
}

func runSessionsList(args []string, w io.Writer) error {
	fs := newSessionsFlags("list")
	all := fs.Bool("all", false, "every project, not just the current directory")
	cwdFlag := fs.String("cwd", "", "project directory (default: current directory)")
	storeFlag := fs.String("store", "", "only this store label")
	readOnly := fs.Bool("read-only", false, "only the read-only sessionStores (archives)")
	limit := fs.Int("limit", 0, "at most N sessions (0 = no limit)")
	asJSON := fs.Bool("json", false, "JSON lines")
	if err := parseSessionsFlags(fs, args); err != nil {
		return err
	}
	if err := checkStoreLabel(*storeFlag); err != nil {
		return err
	}
	cwd, err := absCwd(*cwdFlag)
	if err != nil {
		return err
	}
	if cwd == "" && !*all {
		var err error
		if cwd, err = os.Getwd(); err != nil {
			return fmt.Errorf("get working directory: %w", err)
		}
	}
	if *all {
		cwd = ""
	}
	sessions := store.Catalog(store.CatalogOptions{Cwd: cwd, Labels: storeLabels(*storeFlag), ExtrasOnly: *readOnly})
	if *limit > 0 && len(sessions) > *limit {
		sessions = sessions[:*limit]
	}
	bw := bufio.NewWriter(w)
	defer bw.Flush()
	enc := json.NewEncoder(bw)
	for _, s := range sessions {
		if *asJSON {
			_ = enc.Encode(map[string]any{
				"store":         s.Store,
				"read_only":     s.ReadOnly,
				"id":            s.ID,
				"path":          s.Path,
				"cwd":           s.Cwd,
				"name":          s.Name,
				"created":       s.Created.UTC().Format(time.RFC3339),
				"modified":      s.Modified.UTC().Format(time.RFC3339),
				"messages":      s.MessageCount,
				"first_message": oneLine(s.FirstMessage, 200),
				"parent":        s.ParentSessionPath,
			})
			continue
		}
		fmt.Fprintf(bw, "%s\t%s\t%s\t%d\t%s\t%s\t%s\t%s\n",
			s.Store, s.Modified.UTC().Format(time.RFC3339), s.ID, s.MessageCount,
			s.Path, s.Cwd, oneLine(s.Name, 80), oneLine(s.FirstMessage, 100))
	}
	if len(sessions) == 0 && !*asJSON {
		where := cwd
		if where == "" {
			where = "any project"
		}
		fmt.Fprintf(os.Stderr, "No sessions found for %s\n", where)
	}
	return nil
}

func runSessionsDirs(args []string, w io.Writer) error {
	fs := newSessionsFlags("dirs")
	asJSON := fs.Bool("json", false, "JSON lines")
	if err := parseSessionsFlags(fs, args); err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	for _, s := range store.Stores() {
		fi, err := os.Stat(s.Path)
		exists := err == nil && fi.IsDir()
		if *asJSON {
			_ = enc.Encode(map[string]any{"store": s.Label, "path": s.Path, "read_only": s.ReadOnly, "exists": exists})
			continue
		}
		fmt.Fprintf(w, "%s\t%s\t%t\t%t\n", s.Label, s.Path, s.ReadOnly, exists)
	}
	return nil
}

// searchFile is one file to grep, in output order.
type searchFile struct {
	store string
	kind  string // "bookmark" or "transcript"
	id    string
	path  string
	when  time.Time
}

type searchHit struct {
	Store   string `json:"store"`
	Kind    string `json:"kind"`
	ID      string `json:"id"`
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Snippet string `json:"snippet"`
}

func runSessionsSearch(args []string, w io.Writer) error {
	fs := newSessionsFlags("search")
	cwdFlag := fs.String("cwd", "", "only this project directory")
	storeFlag := fs.String("store", "", "only this store label")
	kind := fs.String("kind", "", "only bookmark or transcript files")
	since := fs.Duration("since", 0, "only files newer than this (e.g. 72h)")
	limit := fs.Int("limit", 50, "stop after N hits (0 = no limit)")
	perFile := fs.Int("per-file", 3, "at most N hits per file (0 = no limit)")
	ignoreCase := fs.Bool("i", false, "case-insensitive")
	asJSON := fs.Bool("json", false, "JSON lines")
	// Flags may follow the pattern: parse, take the pattern, parse the rest.
	if err := parseSessionsFlags(fs, args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) == 0 {
		return fmt.Errorf("search needs a PATTERN\n%s", sessionsUsage)
	}
	pattern := rest[0]
	if err := parseSessionsFlags(fs, rest[1:]); err != nil {
		return err
	}
	if len(fs.Args()) > 0 {
		return fmt.Errorf("unexpected arguments: %v\n%s", fs.Args(), sessionsUsage)
	}
	if err := checkStoreLabel(*storeFlag); err != nil {
		return err
	}
	if *kind != "" && *kind != "bookmark" && *kind != "transcript" {
		return fmt.Errorf("--kind must be bookmark or transcript\n%s", sessionsUsage)
	}
	if *ignoreCase {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return fmt.Errorf("bad pattern: %w", err)
	}
	var cutoff time.Time
	if *since > 0 {
		cutoff = time.Now().Add(-*since)
	}
	cwd, err := absCwd(*cwdFlag)
	if err != nil {
		return err
	}
	files := collectSearchFiles(cwd, *storeFlag, *kind, cutoff)

	bw := bufio.NewWriter(w)
	defer bw.Flush()
	enc := json.NewEncoder(bw)
	n := 0
	searchFiles(files, re, *perFile, func(h searchHit) bool {
		if *asJSON {
			_ = enc.Encode(h)
		} else {
			fmt.Fprintf(bw, "%s\t%s\t%s\t%s:%d\t%s\n", h.Store, h.Kind, h.ID, h.Path, h.Line, h.Snippet)
		}
		n++
		return *limit <= 0 || n < *limit
	})
	return nil
}

// collectSearchFiles lists bookmarks then transcripts across the stores,
// each group newest first.
func collectSearchFiles(cwd, label, kind string, cutoff time.Time) []searchFile {
	var bookmarks, transcripts []searchFile
	for _, st := range store.Stores() {
		if label != "" && st.Label != label {
			continue
		}
		var dirs []string
		if cwd != "" {
			dirs = []string{store.SessionDirForRoot(st.Path, cwd)}
		} else if des, err := os.ReadDir(st.Path); err == nil {
			for _, de := range des {
				if de.IsDir() {
					dirs = append(dirs, filepath.Join(st.Path, de.Name()))
				}
			}
		}
		for _, d := range dirs {
			des, err := os.ReadDir(d)
			if err != nil {
				continue
			}
			for _, de := range des {
				name := de.Name()
				if de.IsDir() || !strings.HasSuffix(name, ".jsonl") || strings.Count(name, ".jsonl") != 1 {
					continue
				}
				f := searchFile{store: st.Label, path: filepath.Join(d, name)}
				switch {
				case strings.HasPrefix(name, "ctx-"):
					continue
				case strings.HasPrefix(name, "bookmarks-"):
					f.kind = "bookmark"
					f.id = strings.TrimSuffix(strings.TrimPrefix(name, "bookmarks-"), ".jsonl")
					if info, err := de.Info(); err == nil {
						f.when = info.ModTime()
					}
				default:
					f.kind = "transcript"
					f.id = store.SessionIDFromFileName(name)
					if t, ok := store.TimestampFromFileName(name); ok {
						f.when = t
					} else if info, err := de.Info(); err == nil {
						f.when = info.ModTime()
					}
				}
				if (kind != "" && f.kind != kind) || (!cutoff.IsZero() && f.when.Before(cutoff)) {
					continue
				}
				if f.kind == "bookmark" {
					bookmarks = append(bookmarks, f)
				} else {
					transcripts = append(transcripts, f)
				}
			}
		}
	}
	newest := func(fs []searchFile) {
		sort.SliceStable(fs, func(i, j int) bool { return fs[i].when.After(fs[j].when) })
	}
	newest(bookmarks)
	newest(transcripts)
	return append(bookmarks, transcripts...)
}

// searchFiles greps files in parallel but reports hits in file order; emit
// returning false stops the search.
func searchFiles(files []searchFile, re *regexp.Regexp, perFile int, emit func(searchHit) bool) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	results := make([]chan []searchHit, len(files))
	for i := range results {
		results[i] = make(chan []searchHit, 1)
	}
	work := make(chan int)
	var wg sync.WaitGroup
	for range runtime.GOMAXPROCS(0) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				results[i] <- grepFile(ctx, files[i], re, perFile)
			}
		}()
	}
	go func() {
		defer close(work)
		for i := range files {
			select {
			case work <- i:
			case <-ctx.Done():
				return
			}
		}
	}()
	defer wg.Wait()
	for i := range files {
		for _, h := range <-results[i] {
			if !emit(h) {
				// Result channels are buffered, so no worker blocks;
				// cancel stops the feeder and in-flight greps.
				cancel()
				return
			}
		}
	}
}

func grepFile(ctx context.Context, f searchFile, re *regexp.Regexp, perFile int) []searchHit {
	if ctx.Err() != nil {
		return nil
	}
	fh, err := os.Open(f.path)
	if err != nil {
		return nil
	}
	defer fh.Close()
	r := bufio.NewReaderSize(fh, 1<<16)
	var hits []searchHit
	for line := 1; ; line++ {
		b, err := r.ReadBytes('\n')
		if len(b) > 0 {
			if loc := re.FindIndex(b); loc != nil {
				hits = append(hits, searchHit{
					Store: f.store, Kind: f.kind, ID: f.id, Path: f.path, Line: line,
					Snippet: snippet(b, loc[0], loc[1]),
				})
				if perFile > 0 && len(hits) >= perFile {
					return hits
				}
			}
		}
		if err != nil || (line%1000 == 0 && ctx.Err() != nil) {
			return hits
		}
	}
}

// snippet returns ~160 runes of line around [start,end), on one line.
func snippet(line []byte, start, end int) string {
	const ctxBytes = 80
	lo, hi := start-ctxBytes, end+ctxBytes
	if lo < 0 {
		lo = 0
	}
	if hi > len(line) {
		hi = len(line)
	}
	s := strings.ToValidUTF8(string(bytes.TrimSpace(line[lo:hi])), "")
	s = oneLine(s, 200)
	if lo > 0 {
		s = "…" + s
	}
	if hi < len(line) {
		s += "…"
	}
	return s
}
