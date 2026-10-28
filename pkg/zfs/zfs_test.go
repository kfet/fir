package zfs

import (
	"bytes"
	"compress/gzip"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	d := t.TempDir()
	os.MkdirAll(filepath.Join(d, "a/b"), 0o755)
	os.WriteFile(filepath.Join(d, "a/x.txt"), []byte("hi"), 0o644)
	os.WriteFile(filepath.Join(d, "a/b/run.sh"), []byte("#!/bin/sh"), 0o755)
	var b1, b2 bytes.Buffer
	if err := Pack(&b1, d, []string{"a"}); err != nil {
		t.Fatal(err)
	}
	Pack(&b2, d, []string{"a"})
	if !bytes.Equal(b1.Bytes(), b2.Bytes()) {
		t.Fatal("nondeterministic")
	}
	z := New(b1.Bytes())
	if data, err := z.ReadFile("a/x.txt"); err != nil || string(data) != "hi" {
		t.Fatal(data, err)
	}
	es, err := z.ReadDir("a")
	if err != nil || len(es) != 2 {
		t.Fatal(es, err)
	}
	f, _ := z.Open("a/b/run.sh")
	st, _ := f.Stat()
	if st.Mode()&0o111 == 0 {
		t.Fatal("exec bit lost")
	}
	if err := Diff(b1.Bytes(), d, []string{"a"}); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(d, "a/x.txt"), []byte("ho"), 0o644)
	if Diff(b1.Bytes(), d, []string{"a"}) == nil {
		t.Fatal("diff missed change")
	}
	if _, err := New([]byte("junk")).ReadFile("x"); err == nil {
		t.Fatal("want error")
	}
}

func TestCorrupt(t *testing.T) {
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	zw.Write(bytes.Repeat([]byte{7}, 1024)) // valid gzip, invalid tar
	zw.Close()
	z := New(b.Bytes())
	if _, err := z.Open("x"); err == nil {
		t.Fatal("open: want error")
	}
	if _, err := z.ReadDir("."); err == nil {
		t.Fatal("readdir: want error")
	}
	if _, err := z.ReadFile("x"); err == nil {
		t.Fatal("readfile: want error")
	}
}

func TestTruncatedBody(t *testing.T) {
	d := t.TempDir()
	rnd := make([]byte, 1<<16)
	rand.Read(rnd)
	os.WriteFile(filepath.Join(d, "f"), rnd, 0o644)
	var b bytes.Buffer
	Pack(&b, d, []string{"f"})
	// Cut the stream mid-body: tar read of the entry must fail.
	if _, err := New(b.Bytes()[:b.Len()/2]).ReadFile("f"); err == nil {
		t.Fatal("want error")
	}
}
