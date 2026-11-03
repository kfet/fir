package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultStoreLabel labels fir's own session store (<agentDir>/sessions).
const DefaultStoreLabel = "local"

// Store is a session store: a root directory holding one
// --<encoded-cwd>-- subdirectory per project, each holding transcripts.
// The default store is fir's own and the only writable one; extra stores
// (e.g. synced archives of another host's sessions) are read-only.
type Store struct {
	Label    string
	Path     string
	ReadOnly bool
}

// ErrReadOnlyStore is returned by writes aimed inside a read-only store.
var ErrReadOnlyStore = errors.New("session store is read-only")

type storeRegistry struct {
	def    Store
	extras []Store
	// roots[i] are the cleaned path and its symlink-resolved form of
	// extras[i], computed once so path checks never resolve the root.
	roots [][]string
}

var registry atomic.Pointer[storeRegistry]

// ConfigureStores registers the default (writable) sessions root and the
// extra read-only stores for this process. Call it once at startup, before
// any session is opened. Unconfigured, there are no extra stores and every
// path is writable. Extras are always read-only, whatever their ReadOnly
// field says. Passing an empty defaultRoot clears the registry.
//
// An extra that contains, equals or lies inside the default root is
// skipped and returned: it would make fir's own store read-only (every
// session silently in memory) or be listed twice.
func ConfigureStores(defaultRoot string, extras []Store) (skipped []Store) {
	if defaultRoot == "" {
		registry.Store(nil)
		return nil
	}
	def := filepath.Clean(defaultRoot)
	defForms := []string{def, resolveExisting(def)}
	r := &storeRegistry{def: Store{Label: DefaultStoreLabel, Path: def}}
	for _, e := range extras {
		e.Path = filepath.Clean(e.Path)
		e.ReadOnly = true
		if overlaps(defForms, []string{e.Path, resolveExisting(e.Path)}) {
			skipped = append(skipped, e)
			continue
		}
		r.extras = append(r.extras, e)
		forms := []string{e.Path}
		if real, err := filepath.EvalSymlinks(e.Path); err == nil && real != e.Path {
			forms = append(forms, real)
		}
		r.roots = append(r.roots, forms)
	}
	registry.Store(r)
	return skipped
}

// overlaps reports whether any path in a contains or lies inside any in b.
func overlaps(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if within(x, y) || within(y, x) {
				return true
			}
		}
	}
	return false
}

// Stores returns every configured store, the default first. Empty when
// ConfigureStores was never called.
func Stores() []Store {
	r := registry.Load()
	if r == nil {
		return nil
	}
	return append([]Store{r.def}, r.extras...)
}

// ExtraStores returns the configured read-only stores.
func ExtraStores() []Store {
	r := registry.Load()
	if r == nil {
		return nil
	}
	return append([]Store(nil), r.extras...)
}

func within(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// ReadOnlyStoreFor reports the read-only store that contains path (the root
// itself included). Symlinks are resolved on the deepest existing ancestor,
// so a link inside the default store that points into an archive is caught.
func ReadOnlyStoreFor(path string) (Store, bool) {
	r := registry.Load()
	if r == nil || len(r.extras) == 0 || path == "" {
		return Store{}, false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return Store{}, false
	}
	forms := []string{abs}
	if real := resolveExisting(abs); real != abs {
		forms = append(forms, real)
	}
	for i, roots := range r.roots {
		for _, root := range roots {
			for _, f := range forms {
				if within(f, root) {
					return r.extras[i], true
				}
			}
		}
	}
	return Store{}, false
}

// resolveExisting resolves symlinks in the longest existing prefix of p and
// re-appends the rest.
func resolveExisting(p string) string {
	rest := ""
	for cur := p; ; {
		if real, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(real, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// IsWithinStore reports whether path lies inside any configured store
// (default or extra). The ACP resume whitelist uses it.
func IsWithinStore(path string) bool {
	r := registry.Load()
	if r == nil {
		return false
	}
	if within(filepath.Clean(path), r.def.Path) {
		return true
	}
	_, ok := ReadOnlyStoreFor(path)
	return ok
}

// checkWritable returns ErrReadOnlyStore when path is inside a read-only
// store. Every write in this package goes through it.
func checkWritable(path string) error {
	if s, ok := ReadOnlyStoreFor(path); ok {
		return fmt.Errorf("%w: %s (store %q)", ErrReadOnlyStore, path, s.Label)
	}
	return nil
}

// encodeCwd is the per-project directory name for cwd.
func encodeCwd(cwd string) string {
	safe := "--" + strings.TrimLeft(cwd, "/\\")
	return strings.NewReplacer("/", "-", "\\", "-", ":", "-").Replace(safe) + "--"
}

// DecodeCwdDir reverses encodeCwd as well as it can: "--home-kfet-src--"
// → "/home/kfet/src". Lossy — a "-" in the original path comes back as
// "/" — so use it only when no recorded cwd exists.
func DecodeCwdDir(name string) string {
	if !strings.HasPrefix(name, "--") || !strings.HasSuffix(name, "--") || len(name) < 4 {
		return ""
	}
	return "/" + strings.ReplaceAll(name[2:len(name)-2], "-", "/")
}

// SessionDirForRoot is the project directory for cwd under a sessions root.
func SessionDirForRoot(root, cwd string) string {
	return filepath.Join(root, encodeCwd(cwd))
}

// writableSessionDir returns the default store's project directory for cwd,
// or "" when no default store is configured.
func writableSessionDir(cwd string) string {
	r := registry.Load()
	if r == nil {
		return ""
	}
	return filepath.Join(r.def.Path, encodeCwd(cwd))
}

// IsTranscriptName reports whether a session-dir file name is a transcript:
// <name>.jsonl, not a bookmarks/ctx log or a <transcript>.jsonl.state.jsonl.
func IsTranscriptName(name string) bool {
	return strings.HasSuffix(name, ".jsonl") &&
		strings.Count(name, ".jsonl") == 1 &&
		!strings.HasPrefix(name, "bookmarks-") &&
		!strings.HasPrefix(name, "ctx-")
}

// Key is a session's unique catalog key, "<store>:<session id>": the same
// session id can appear in two stores.
func (s SessionListInfo) Key() string {
	label := s.Store
	if label == "" {
		label = DefaultStoreLabel
	}
	return label + ":" + s.ID
}

// CatalogOptions filters Catalog.
type CatalogOptions struct {
	Cwd        string   // only sessions of this project dir ("" = all)
	Labels     []string // only these stores (nil = all)
	ExtrasOnly bool     // skip the default store
}

// MidWriteAge is how recently a transcript in a read-only store may have
// been modified before the catalog skips it: a sync tool may be writing it.
var MidWriteAge = time.Minute

// Catalog lists sessions across every configured store, newest first. The
// default store is listed as ListSessions does; read-only stores are listed
// from file names and each transcript's .meta.json only — never a
// transcript body, never a write. Transcripts there without a .meta.json,
// or modified within MidWriteAge, are skipped.
func Catalog(opts CatalogOptions) []SessionListInfo {
	r := registry.Load()
	if r == nil {
		return nil
	}
	want := func(label string) bool {
		if opts.Labels == nil {
			return true
		}
		for _, l := range opts.Labels {
			if l == label {
				return true
			}
		}
		return false
	}
	var out []SessionListInfo
	if !opts.ExtrasOnly && want(r.def.Label) {
		var infos []SessionListInfo
		if opts.Cwd != "" {
			infos, _ = ListSessions("", filepath.Join(r.def.Path, encodeCwd(opts.Cwd)))
		} else {
			infos = listRoots([]string{r.def.Path})
		}
		for _, s := range infos {
			s.Store = r.def.Label
			out = append(out, s)
		}
	}
	for _, s := range r.extras {
		if want(s.Label) {
			out = append(out, listReadOnlyStore(s, opts.Cwd)...)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Modified.After(out[j].Modified) })
	return out
}

func listReadOnlyStore(s Store, cwd string) []SessionListInfo {
	var dirs []string
	if cwd != "" {
		dirs = []string{filepath.Join(s.Path, encodeCwd(cwd))}
	} else {
		des, err := os.ReadDir(s.Path)
		if err != nil {
			return nil
		}
		for _, de := range des {
			if de.IsDir() {
				dirs = append(dirs, filepath.Join(s.Path, de.Name()))
			}
		}
	}
	results := make([][]SessionListInfo, len(dirs))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	cutoff := time.Now().Add(-MidWriteAge)
	for i, d := range dirs {
		wg.Add(1)
		go func(i int, d string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = listReadOnlyDir(s, d, cutoff)
		}(i, d)
	}
	wg.Wait()
	var out []SessionListInfo
	for _, r := range results {
		out = append(out, r...)
	}
	return out
}

func listReadOnlyDir(s Store, dir string, cutoff time.Time) []SessionListInfo {
	des, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []SessionListInfo
	for _, de := range des {
		name := de.Name()
		if de.IsDir() || !IsTranscriptName(name) {
			continue
		}
		info, err := de.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(metaPath(path))
		if err != nil {
			continue
		}
		var m MetaSidecar
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		if m.ID == "" {
			m.ID = SessionIDFromFileName(name)
		}
		if m.Cwd == "" {
			m.Cwd = DecodeCwdDir(filepath.Base(dir))
		}
		m.ParentSessionPath = remapParent(s.Path, m.ParentSessionPath)
		out = append(out, SessionListInfo{
			Path:              path,
			ID:                m.ID,
			Cwd:               m.Cwd,
			Name:              m.Name,
			ParentSessionPath: m.ParentSessionPath,
			Created:           m.Created,
			Modified:          info.ModTime(),
			MessageCount:      m.MessageCount,
			FirstMessage:      m.FirstMessage,
			Store:             s.Label,
			ReadOnly:          true,
		})
	}
	return out
}

// remapParent maps a parent transcript path recorded on the store's origin
// host (…/sessions/<project>/<file>) to its copy in this store, when the
// copy exists, so /resume can still draw the fork tree.
func remapParent(root, parent string) string {
	if parent == "" {
		return ""
	}
	cand := filepath.Join(root, filepath.Base(filepath.Dir(parent)), filepath.Base(parent))
	if within(cand, root) && fileExists(cand) {
		return cand
	}
	return parent
}

// SessionIDFromFileName extracts the session id from "<timestamp>_<id>.jsonl".
func SessionIDFromFileName(name string) string {
	base := strings.TrimSuffix(name, ".jsonl")
	if i := strings.LastIndex(base, "_"); i >= 0 {
		return base[i+1:]
	}
	return base
}

// TimestampFromFileName parses the creation time encoded in a
// "<2006-01-02T15-04-05-999999999Z>_<id>.jsonl" (or bookmarks-/ctx-)
// file name. ok is false for names without one.
func TimestampFromFileName(name string) (time.Time, bool) {
	base := strings.TrimSuffix(filepath.Base(name), ".jsonl")
	ts, _, found := strings.Cut(base, "_")
	if !found || len(ts) < len("2006-01-02T15-04-05Z") {
		return time.Time{}, false
	}
	// 2026-10-08T18-15-25-584271117Z → 2026-10-08T18:15:25.584271117Z
	date, clock, ok := strings.Cut(ts, "T")
	if !ok {
		return time.Time{}, false
	}
	parts := strings.SplitN(strings.TrimSuffix(clock, "Z"), "-", 4)
	if len(parts) < 3 {
		return time.Time{}, false
	}
	s := date + "T" + strings.Join(parts[:3], ":")
	if len(parts) == 4 {
		s += "." + parts[3]
	}
	t, err := time.Parse("2006-01-02T15:04:05.999999999", s)
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}
