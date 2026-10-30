// Command zfsgen packs source paths into a deterministic .tar.gz for pkg/zfs.
//
//	zfsgen -root DIR -o OUT.tar.gz path...
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"

	"github.com/kfet/fir/pkg/zfs"
)

func main() {
	root := flag.String("root", ".", "directory paths are relative to")
	out := flag.String("o", "", "output archive")
	flag.Parse()
	if *out == "" {
		fmt.Fprintln(os.Stderr, "usage: zfsgen -root DIR -o OUT.tar.gz path...")
		os.Exit(2)
	}
	var buf bytes.Buffer
	if err := zfs.Pack(&buf, *root, flag.Args()); err != nil {
		fmt.Fprintln(os.Stderr, "zfsgen:", err)
		os.Exit(1)
	}
	// Compare decoded content, not bytes: gzip output differs across Go
	// versions, and rewriting an unchanged archive dirties the tree in CI.
	if old, err := os.ReadFile(*out); err == nil &&
		(bytes.Equal(old, buf.Bytes()) || zfs.Diff(old, *root, flag.Args()) == nil) {
		return
	}
	if err := os.WriteFile(*out, buf.Bytes(), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "zfsgen:", err)
		os.Exit(1)
	}
}
