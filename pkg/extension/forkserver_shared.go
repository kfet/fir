package extension

import (
	"log/slog"
	"sync"
	"syscall"
)

// sharedForks holds process-lifetime ForkServer singletons keyed by SDK dir.
// The SDK dir is content-addressed (~/.cache/fir/sdks/<hash>) and Python is
// the only forked runtime, so the key is equivalent to (runtime, sdk-hash).
//
// The template heap is session-free by construction (it imports only fir_ext
// + stdlib; per-spawn env arrives in the spawn control message), so one warm
// template can serve every session in this fir process — important under
// `--mode acp`, where one process owns many sessions and a per-session
// template would multiply N byte-identical interpreters.
//
// Lifetime == the fir process: lazily started on first need, never closed on
// session end (sessions only stop their own forked children). The template
// exits on its own when fir dies (stdin EOF), and CloseSharedForkServers
// handles explicit teardown (tests, clean exits).
var (
	sharedForkMu sync.Mutex
	sharedForks  = map[string]*ForkServer{}
)

// SharedForkServer returns the process-wide warm fork template for sdkDir,
// starting it on first use. A cached entry that has been closed or whose
// process has died is replaced with a fresh one. env should be the SDK env
// (session-free); it is only used when (re)starting the template.
//
// Callers must NOT Close the returned ForkServer — it is shared across
// sessions. Stop per-session children via StopChild / Process.Stop instead.
func SharedForkServer(sdkDir string, env []string, logger *slog.Logger) (*ForkServer, error) {
	sharedForkMu.Lock()
	defer sharedForkMu.Unlock()

	if fs, ok := sharedForks[sdkDir]; ok {
		if fs.usable() {
			return fs, nil
		}
		// Reclaim the dead template's sockDir before replacing it (Close on
		// an already-dead process is safe and removes the temp dir).
		_ = fs.Close()
		delete(sharedForks, sdkDir)
	}
	// Starting under the lock intentionally serialises the first start so N
	// concurrent sessions cannot race N redundant templates into existence;
	// after that everyone hits the cache. Worst case (a hung interpreter that
	// never reports ready) holds the lock for awaitReady's 15s timeout.
	fs, err := StartForkServer(sdkDir, env, logger)
	if err != nil {
		return nil, err
	}
	sharedForks[sdkDir] = fs
	return fs, nil
}

// CloseSharedForkServers shuts down and forgets all shared fork templates.
// Used by tests and available for explicit process teardown; not required on
// exit (the template exits on stdin EOF when fir dies).
func CloseSharedForkServers() {
	sharedForkMu.Lock()
	forks := make([]*ForkServer, 0, len(sharedForks))
	for _, fs := range sharedForks {
		forks = append(forks, fs)
	}
	sharedForks = map[string]*ForkServer{}
	sharedForkMu.Unlock()

	for _, fs := range forks {
		_ = fs.Close()
	}
}

// usable reports whether the template can still serve spawns: not closed and
// its process still alive (signal-0 probe).
func (fs *ForkServer) usable() bool {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.closed || fs.dead || fs.cmd == nil || fs.cmd.Process == nil {
		return false
	}
	return syscall.Kill(fs.cmd.Process.Pid, 0) == nil
}
