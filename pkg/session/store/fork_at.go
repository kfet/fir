package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ForkAt creates a child session from the session file at sourcePath whose
// leaf is entryID — the non-interactive equivalent of /resume followed by
// /tree to that entry, then branching into a new file. The source file is
// only read, never written or locked, so a live parent is unaffected.
//
// The child file contains exactly the root→entryID path (plus labels), so
// the LLM context it rebuilds is byte-identical to the parent's prefix at
// that point and provider prompt caches hit.
//
// cwd is recorded as the child's working directory (empty keeps the
// parent's), so a child can be opened from another worktree.
//
// An entry that would leave an assistant tool call without its tool result
// at the tail of the branch is rejected: the child would start mid tool
// call and the history would no longer match what the parent sent.
func ForkAt(sourcePath, entryID, cwd, sessionDir string) (*SessionStore, error) {
	header, entries := loadEntriesFromFile(sourcePath)
	if header == nil {
		return nil, fmt.Errorf("cannot fork: session file is empty or invalid: %s", sourcePath)
	}
	if sessionDir == "" {
		return nil, fmt.Errorf("sessionDir is required for ForkAt")
	}
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		return nil, fmt.Errorf("cannot create session directory: %w", err)
	}

	src := &SessionStore{
		header:      header,
		sessionID:   header.ID,
		sessionFile: sourcePath,
		sessionDir:  sessionDir,
		cwd:         header.Cwd,
		persist:     true,
		entries:     entries,
	}
	if cwd != "" {
		src.cwd = cwd
	}
	src.buildIndex()
	if src.byID[entryID] == nil {
		return nil, fmt.Errorf("entry %q not found in %s", entryID, sourcePath)
	}
	ctx := BuildSessionContextFromEntries(src.entries, entryID, src.byID)
	if danglingToolUse(ctx) {
		return nil, fmt.Errorf("entry %q leaves a tool call without its result; pick the tool result entry or a later one", entryID)
	}

	newFile, err := src.CreateBranchedSession(entryID)
	if err != nil {
		return nil, err
	}
	copyStateUnbound(sourcePath, newFile)
	ss, _ := OpenSessionStore(newFile, sessionDir)
	return ss, nil
}

// danglingToolUse reports whether the context ends with an assistant turn
// whose tool calls were not all answered on this branch (the context
// builder papers over those with synthesized "interrupted" results).
func danglingToolUse(ctx SessionContext) bool {
	for i := len(ctx.Messages) - 1; i >= 0; i-- {
		m := ctx.Messages[i]
		tr := m.Message.AsToolResult()
		if m.Custom != nil || tr == nil {
			return false
		}
		if IsInterruptedToolResult(tr) {
			return true
		}
	}
	return false
}

// ResolveSessionRef finds a session file from a user-supplied reference: an
// existing path, a file name inside sessionDir, or a session id (or unique
// id fragment) matched against file names in sessionDir and then in every
// project directory under sessionsRoot. Returns "" if nothing matches.
func ResolveSessionRef(ref, sessionDir, sessionsRoot string) string {
	if ref == "" {
		return ""
	}
	if fi, err := os.Stat(ref); err == nil && !fi.IsDir() {
		abs, _ := filepath.Abs(ref)
		return abs
	}
	if p := filepath.Join(sessionDir, ref); fileExists(p) {
		return p
	}
	if p := matchSessionFile(sessionDir, ref); p != "" {
		return p
	}
	dirs, _ := os.ReadDir(sessionsRoot)
	for _, d := range dirs {
		if d.IsDir() {
			if p := matchSessionFile(filepath.Join(sessionsRoot, d.Name()), ref); p != "" {
				return p
			}
		}
	}
	return ""
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// matchSessionFile returns the single .jsonl in dir whose name contains
// frag, or "" when there is none or more than one.
func matchSessionFile(dir, frag string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	found := ""
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") || !strings.Contains(e.Name(), frag) {
			continue
		}
		if found != "" {
			return ""
		}
		found = filepath.Join(dir, e.Name())
	}
	return found
}

// ForkableLeafID returns the newest entry on the current branch at which
// ForkAt would succeed — the leaf itself unless it is mid tool call (as it
// is while a tool is running), in which case the last entry before that
// assistant turn. Returns "" for an empty session.
func (ss *SessionStore) ForkableLeafID() string {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	branch := ss.getBranchUnlocked(ss.leafID)
	for i := len(branch) - 1; i >= 0; i-- {
		id := branch[i].ID
		if !danglingToolUse(BuildSessionContextFromEntries(ss.entries, id, ss.byID)) {
			return id
		}
	}
	return ""
}

// copyStateUnbound copies the saved settings of the session at src to dst,
// minus the runtime handle: a handle names exactly one transcript (the
// parent's ACP sessionId), so a copy carrying it would hijack the parent's
// handle on its next save.
func copyStateUnbound(src, dst string) {
	var st map[string]json.RawMessage
	if !ReadState(src, &st) {
		return
	}
	var rt map[string]json.RawMessage
	if json.Unmarshal(st["runtime"], &rt) == nil && rt["handle"] != nil {
		delete(rt, "handle")
		st["runtime"], _ = json.Marshal(rt)
	}
	_ = WriteState(dst, st)
}
