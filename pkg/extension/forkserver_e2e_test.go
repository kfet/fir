package extension

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kfet/fir/pkg/extension/sdk"
)

// execTestExtScript is the test extension as an executable script (shebang +
// chmod) for the plain exec spawn path, so fork vs exec compare apples to
// apples (same module, same SDK).
const execTestExtScript = "#!/usr/bin/env python3\n" + testExtScript

func writeExecTestExt(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "forktest_exec.py")
	if err := os.WriteFile(path, []byte(execTestExtScript), 0o755); err != nil {
		t.Fatalf("write exec test ext: %v", err)
	}
	return path
}

func median(ds []time.Duration) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	cp := append([]time.Duration(nil), ds...)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	return cp[len(cp)/2]
}

// readPrivateDirtyKB parses Private_Dirty (KB) from /proc/<pid>/smaps_rollup.
// Returns (0,false) when /proc is unavailable (e.g. macOS) or unreadable.
func readPrivateDirtyKB(pid int) (int, bool) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/smaps_rollup", pid))
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "Private_Dirty:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				if kb, err := strconv.Atoi(fields[1]); err == nil {
					return kb, true
				}
			}
		}
	}
	return 0, false
}

// TestForkVsExec_Startup is the cross-platform proof that forked sidecars start
// materially faster than independent python3 cold starts (no per-spawn SDK
// re-import). Asserts on every platform.
func TestForkVsExec_Startup(t *testing.T) {
	sdkDir, err := sdk.EnsureExtracted()
	if err != nil {
		t.Skipf("SDK unavailable: %v", err)
	}
	sdkEnv := sdk.SDKEnv(sdkDir)

	forkPath := writeTestExt(t)
	execPath := writeExecTestExt(t)

	const k = 7
	var forkDurs, execDurs []time.Duration

	for i := 0; i < k; i++ {
		fs, err := StartForkServer(sdkDir, sdkEnv, nil)
		if err != nil {
			t.Skipf("fork template unavailable: %v", err)
		}
		cfg := ExtProcConfig{Name: "forktest", Path: forkPath, Scope: "builtin"}
		p := NewProcess(cfg, sdkEnv, nil)
		start := time.Now()
		if err := p.StartForked(fs); err != nil {
			_ = fs.Close()
			t.Fatalf("StartForked: %v", err)
		}
		if _, err := Handshake(p, "/tmp", nil, 10*time.Second); err != nil {
			_ = fs.Close()
			t.Fatalf("fork handshake: %v", err)
		}
		forkDurs = append(forkDurs, time.Since(start))
		_ = p.Stop(context.Background())
		_ = fs.Close()
	}

	for i := 0; i < k; i++ {
		cfg := ExtProcConfig{Name: "forktest", Path: execPath, Scope: "project"}
		p := NewProcess(cfg, sdkEnv, nil)
		start := time.Now()
		if err := p.Start(); err != nil {
			t.Fatalf("exec Start: %v", err)
		}
		if _, err := Handshake(p, "/tmp", nil, 10*time.Second); err != nil {
			t.Fatalf("exec handshake: %v", err)
		}
		execDurs = append(execDurs, time.Since(start))
		_ = p.Stop(context.Background())
	}

	forkMed := median(forkDurs)
	execMed := median(execDurs)
	t.Logf("startup median: fork=%v exec=%v (speedup %.2fx)", forkMed, execMed,
		float64(execMed)/float64(forkMed))

	if forkMed >= execMed {
		t.Errorf("expected forked startup faster than exec: fork=%v exec=%v", forkMed, execMed)
	}
}

// TestForkVsExec_Memory proves forked children share the warm interpreter heap
// copy-on-write: their Private_Dirty is materially below an independent
// interpreter's. Linux-gated (needs /proc/<pid>/smaps_rollup); skips cleanly
// elsewhere (e.g. macOS).
func TestForkVsExec_Memory(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("memory assertion is Linux-gated (smaps_rollup); GOOS=%s", runtime.GOOS)
	}
	sdkDir, err := sdk.EnsureExtracted()
	if err != nil {
		t.Skipf("SDK unavailable: %v", err)
	}
	sdkEnv := sdk.SDKEnv(sdkDir)

	forkPath := writeTestExt(t)
	execPath := writeExecTestExt(t)

	fs, err := StartForkServer(sdkDir, sdkEnv, nil)
	if err != nil {
		t.Skipf("fork template unavailable: %v", err)
	}
	defer fs.Close()

	const n = 6
	var forkProcs, execProcs []*Process
	defer func() {
		for _, p := range forkProcs {
			_ = p.Stop(context.Background())
		}
		for _, p := range execProcs {
			_ = p.Stop(context.Background())
		}
	}()

	for i := 0; i < n; i++ {
		p := NewProcess(ExtProcConfig{Name: "forktest", Path: forkPath, Scope: "builtin"}, sdkEnv, nil)
		if err := p.StartForked(fs); err != nil {
			t.Fatalf("StartForked mem[%d]: %v", i, err)
		}
		if _, err := Handshake(p, "/tmp", nil, 10*time.Second); err != nil {
			t.Fatalf("fork handshake mem[%d]: %v", i, err)
		}
		forkProcs = append(forkProcs, p)
	}
	for i := 0; i < n; i++ {
		p := NewProcess(ExtProcConfig{Name: "forktest", Path: execPath, Scope: "project"}, sdkEnv, nil)
		if err := p.Start(); err != nil {
			t.Fatalf("exec Start mem[%d]: %v", i, err)
		}
		if _, err := Handshake(p, "/tmp", nil, 10*time.Second); err != nil {
			t.Fatalf("exec handshake mem[%d]: %v", i, err)
		}
		execProcs = append(execProcs, p)
	}

	// Give interpreters a moment to settle their heaps.
	time.Sleep(300 * time.Millisecond)

	var forkPD, execPD []int
	for _, p := range forkProcs {
		if kb, ok := readPrivateDirtyKB(p.Pid()); ok {
			forkPD = append(forkPD, kb)
		}
	}
	for _, p := range execProcs {
		if kb, ok := readPrivateDirtyKB(p.Pid()); ok {
			execPD = append(execPD, kb)
		}
	}
	if len(forkPD) == 0 || len(execPD) == 0 {
		t.Skip("smaps_rollup not readable for children")
	}

	sort.Ints(forkPD)
	sort.Ints(execPD)
	forkMedPD := forkPD[len(forkPD)/2]
	execMedPD := execPD[len(execPD)/2]
	t.Logf("Private_Dirty median: fork=%dKB exec=%dKB (reduction %.1f%%)",
		forkMedPD, execMedPD, 100*(1-float64(forkMedPD)/float64(execMedPD)))

	// Forked children share the warm heap copy-on-write, so their private
	// dirty pages must be materially below an independent interpreter's. Use a
	// conservative 0.75x bar to stay robust across CPython builds.
	if float64(forkMedPD) >= 0.75*float64(execMedPD) {
		t.Errorf("expected forked Private_Dirty materially lower: fork=%dKB exec=%dKB", forkMedPD, execMedPD)
	}
}
