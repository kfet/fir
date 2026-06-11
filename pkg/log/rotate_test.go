package log

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
)

// writeLines writes n JSON lines of roughly lineBytes each to w.
func bigLine(i, pad int) []byte {
	b, _ := json.Marshal(map[string]string{
		"seq": strconv.Itoa(i),
		"pad": string(make([]byte, pad)),
	})
	return append(b, '\n')
}

func TestRotate_SizeTrigger(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "debug.log")
	w, err := newRotatingWriter(path, RotateConfig{
		MaxSizeMB:        1,
		Keep:             3,
		Compress:         true,
		CheckEveryWrites: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })

	// Write > 1MB to force a rotation.
	for i := 0; i < 2000; i++ {
		if _, err := w.Write(bigLine(i, 800)); err != nil {
			t.Fatal(err)
		}
	}
	w.wg.Wait() // drain detached gzip goroutine

	gz := path + ".1.gz"
	if _, err := os.Stat(gz); err != nil {
		t.Fatalf("expected backup %s: %v", gz, err)
	}
	// A single further write (with the archive drained and the file still
	// over the cap) deterministically triggers an in-place truncation.
	if _, err := w.Write(bigLine(99999, 800)); err != nil {
		t.Fatal(err)
	}
	w.wg.Wait()
	fi, _ := os.Stat(path)
	if fi.Size() >= 1024*1024 {
		t.Fatalf("live file not truncated: %d", fi.Size())
	}
}

func TestRotate_BackupShiftAndKeep(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "debug.log")
	keep := 3
	w, err := newRotatingWriter(path, RotateConfig{
		MaxSizeMB:        1,
		Keep:             keep,
		Compress:         true,
		CheckEveryWrites: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })

	// Force several rotations, tagging each generation so we can tell them
	// apart in the gzip backups.
	for gen := 0; gen < keep+2; gen++ {
		for i := 0; i < 2000; i++ {
			if _, err := w.Write(bigLine(gen, 800)); err != nil {
				t.Fatal(err)
			}
		}
		w.wg.Wait()
	}

	// Only `keep` backups should remain.
	if _, err := os.Stat(fmt.Sprintf("%s.%d.gz", path, keep+1)); !os.IsNotExist(err) {
		t.Fatalf("backup beyond keep cap should not exist")
	}
	for i := 1; i <= keep; i++ {
		if _, err := os.Stat(fmt.Sprintf("%s.%d.gz", path, i)); err != nil {
			t.Fatalf("expected backup .%d.gz: %v", i, err)
		}
	}
}

func TestRotate_GzipReadable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "debug.log")
	w, err := newRotatingWriter(path, RotateConfig{
		MaxSizeMB:        1,
		Keep:             3,
		Compress:         true,
		CheckEveryWrites: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })

	for i := 0; i < 2000; i++ {
		if _, err := w.Write(bigLine(i, 800)); err != nil {
			t.Fatal(err)
		}
	}
	w.wg.Wait()

	f, err := os.Open(path + ".1.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer gr.Close()
	sc := bufio.NewScanner(gr)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lines := 0
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("torn line in gzip backup: %q", sc.Text())
		}
		lines++
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if lines == 0 {
		t.Fatal("no lines in gzip backup")
	}
}

// TestRotate_ConcurrentNoLoss writes from many goroutines while rotations
// fire, then verifies every emitted line survives intact across the live file
// and all gzip backups — no loss, no torn lines.
func TestRotate_ConcurrentNoLoss(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "debug.log")
	w, err := newRotatingWriter(path, RotateConfig{
		MaxSizeMB:        1,
		Keep:             50,
		Compress:         true,
		CheckEveryWrites: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })

	const (
		writers   = 8
		perWriter = 1500
	)
	var wg sync.WaitGroup
	for g := 0; g < writers; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				b, _ := json.Marshal(map[string]any{
					"w":   g,
					"i":   i,
					"pad": string(make([]byte, 200)),
				})
				b = append(b, '\n')
				if _, err := w.Write(b); err != nil {
					t.Errorf("write: %v", err)
					return
				}
			}
		}(g)
	}
	wg.Wait()
	w.wg.Wait() // ensure all detached gzip archives are done before reading

	// Collect every line from the live file plus all backups.
	seen := make(map[[2]int]bool)
	count := func(r *bufio.Scanner) {
		r.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for r.Scan() {
			line := r.Bytes()
			if len(line) == 0 {
				continue
			}
			var m struct {
				W int `json:"w"`
				I int `json:"i"`
			}
			if err := json.Unmarshal(line, &m); err != nil {
				t.Fatalf("torn line: %q", r.Text())
			}
			seen[[2]int{m.W, m.I}] = true
		}
		if err := r.Err(); err != nil {
			t.Fatal(err)
		}
	}

	// Live file.
	lf, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	count(bufio.NewScanner(lf))
	lf.Close()

	// Backups.
	matches, _ := filepath.Glob(path + ".*.gz")
	for _, m := range matches {
		f, err := os.Open(m)
		if err != nil {
			t.Fatal(err)
		}
		gr, err := gzip.NewReader(f)
		if err != nil {
			f.Close()
			t.Fatalf("gzip %s: %v", m, err)
		}
		count(bufio.NewScanner(gr))
		gr.Close()
		f.Close()
	}

	missing := 0
	for g := 0; g < writers; g++ {
		for i := 0; i < perWriter; i++ {
			if !seen[[2]int{g, i}] {
				missing++
			}
		}
	}
	if missing != 0 {
		t.Fatalf("%d lines lost across rotation (have %d of %d)",
			missing, len(seen), writers*perWriter)
	}
}
