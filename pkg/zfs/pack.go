package zfs

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// Pack writes a deterministic .tar.gz of the given paths (files or
// directories, relative to root) to w: entries sorted, zero mtimes, no owner
// info, modes reduced to 0644/0755.
func Pack(w io.Writer, root string, paths []string) error {
	var files []string
	for _, p := range paths {
		err := filepath.WalkDir(filepath.Join(root, p), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			// Mirror go:embed directory semantics: skip . and _ entries.
			if n := d.Name(); path != filepath.Join(root, p) && (n[0] == '.' || n[0] == '_') {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			files = append(files, filepath.ToSlash(rel))
			return nil
		})
		if err != nil {
			return err
		}
	}
	sort.Strings(files)
	zw, err := gzip.NewWriterLevel(w, gzip.BestCompression)
	if err != nil {
		return err
	}
	tw := tar.NewWriter(zw)
	for _, name := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		st, err := os.Stat(full)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(full)
		if err != nil {
			return err
		}
		mode := int64(0o644)
		if st.Mode()&0o111 != 0 {
			mode = 0o755
		}
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(data)), Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}); err != nil {
			return err
		}
		if _, err := tw.Write(data); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return zw.Close()
}

// Diff reports whether blob's contents (names, modes, bytes) differ from what
// Pack would produce now from root/paths. It compares decoded content, not
// compressed bytes, so it is immune to compressor version changes.
func Diff(blob []byte, root string, paths []string) error {
	var buf bytes.Buffer
	if err := Pack(&buf, root, paths); err != nil {
		return err
	}
	want, err := decode(buf.Bytes())
	if err != nil {
		return err
	}
	got, err := decode(blob)
	if err != nil {
		return err
	}
	if len(got) != len(want) {
		return fmt.Errorf("blob has %d files, source has %d", len(got), len(want))
	}
	for n, w := range want {
		g, ok := got[n]
		if !ok {
			return fmt.Errorf("%s missing from blob", n)
		}
		if g.Mode != w.Mode || !bytes.Equal(g.Data, w.Data) {
			return fmt.Errorf("%s differs from blob", n)
		}
	}
	return nil
}
