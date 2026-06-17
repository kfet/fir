package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// auditLogName is the file (alongside auth.json) that records every mutation
// to the credential store. It exists so that a vanished account — "my login
// was there minutes ago and now it's gone" — is answerable after the fact
// instead of a whodunit. The store has no other history.
const auditLogName = "auth-audit.log"

// AuditAction enumerates the kinds of mutation recorded in the audit log.
type AuditAction string

const (
	AuditActionSet     AuditAction = "set"     // a slot was created or overwritten
	AuditActionRemove  AuditAction = "remove"  // a slot was deleted (logout)
	AuditActionRefresh AuditAction = "refresh" // an OAuth token was refreshed in place
)

// AuditEntry is one append-only JSONL record of a credential-store mutation.
type AuditEntry struct {
	TS      string      `json:"ts"`              // RFC3339 millisecond timestamp
	Action  AuditAction `json:"action"`          // set | remove | refresh
	Slot    string      `json:"slot"`            // the slot key affected
	Type    string      `json:"type,omitempty"`  // credential type, when known
	Label   string      `json:"label,omitempty"` // human label, when known
	PID     int         `json:"pid"`             // process that performed the mutation
	Exe     string      `json:"exe,omitempty"`   // best-effort process name
	Remain  int         `json:"remain"`          // slot count remaining after the mutation
	Callers []string    `json:"callers,omitempty"`
}

// auditWriter appends AuditEntry records to a JSONL file. All writes are
// best-effort: an unwritable audit log must never block or fail a credential
// operation. A nil *auditWriter is a no-op (used by the in-memory backend).
type auditWriter struct {
	mu   sync.Mutex
	path string
}

// newAuditWriter returns an audit writer that appends next to authPath, or nil
// if authPath is empty (in-memory storage keeps no audit trail).
func newAuditWriter(authPath string) *auditWriter {
	if authPath == "" {
		return nil
	}
	return &auditWriter{path: filepath.Join(filepath.Dir(authPath), auditLogName)}
}

// record appends one entry. Best-effort: any error is swallowed.
func (w *auditWriter) record(action AuditAction, slot string, cred *AuthCredential, remain int) {
	if w == nil {
		return
	}
	e := AuditEntry{
		TS:      time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00"),
		Action:  action,
		Slot:    slot,
		PID:     os.Getpid(),
		Exe:     exeName(),
		Remain:  remain,
		Callers: callerFrames(),
	}
	if cred != nil {
		e.Type = string(cred.Type)
		e.Label = cred.Label
	}

	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	b = append(b, '\n')

	w.mu.Lock()
	defer w.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(w.path), 0700); err != nil {
		return
	}
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(b)
}

// exeName returns the basename of the running executable, best-effort.
func exeName() string {
	p, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Base(p)
}

// callerFrames captures a compact stack snippet identifying the caller that
// triggered the mutation — the crucial bit for diagnosing an unexpected
// deletion. Internal pkg/auth frames and the runtime are skipped so the first
// frames point at the real initiator (command handler, login flow, etc.).
func callerFrames() []string {
	const maxFrames = 8
	pcs := make([]uintptr, 32)
	// Skip: runtime.Callers, callerFrames, record, the mutation method.
	n := runtime.Callers(4, pcs)
	if n == 0 {
		return nil
	}
	frames := runtime.CallersFrames(pcs[:n])
	out := make([]string, 0, maxFrames)
	for {
		fr, more := frames.Next()
		fn := fr.Function
		// Drop noise: the audit writer's own helpers and the Go runtime.
		if !strings.Contains(fn, "/pkg/auth.(*auditWriter)") &&
			!strings.HasPrefix(fn, "runtime.") {
			out = append(out, fmt.Sprintf("%s (%s:%d)", trimFunc(fn), filepath.Base(fr.File), fr.Line))
		}
		if !more || len(out) >= maxFrames {
			break
		}
	}
	return out
}

// trimFunc shortens a fully-qualified function name to its package/symbol tail.
func trimFunc(fn string) string {
	if i := strings.LastIndex(fn, "/"); i >= 0 {
		return fn[i+1:]
	}
	return fn
}
