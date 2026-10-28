// Package zfs serves an fs.FS from a gzip-compressed tar archive embedded in
// the binary. The archive is inflated once, lazily, and kept in memory.
package zfs

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"io/fs"
	"sync"
	"testing/fstest"
)

// FS is a lazily-decompressed read-only file system.
type FS struct {
	blob []byte
	once sync.Once
	m    fstest.MapFS
	err  error
}

// New returns an FS backed by a .tar.gz blob. Nothing is decompressed until
// first use. A corrupt blob makes every operation return the decode error.
func New(blob []byte) *FS { return &FS{blob: blob} }

func (z *FS) load() (fs.FS, error) {
	z.once.Do(func() { z.m, z.err = decode(z.blob) })
	return z.m, z.err
}

func decode(blob []byte) (fstest.MapFS, error) {
	zr, err := gzip.NewReader(bytes.NewReader(blob))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	tr := tar.NewReader(zr)
	m := fstest.MapFS{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return m, nil
		}
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			return nil, err
		}
		m[h.Name] = &fstest.MapFile{Data: data, Mode: fs.FileMode(h.Mode).Perm()}
	}
}

// Open implements fs.FS.
func (z *FS) Open(name string) (fs.File, error) {
	f, err := z.load()
	if err != nil {
		return nil, err
	}
	return f.Open(name)
}

// ReadFile implements fs.ReadFileFS.
func (z *FS) ReadFile(name string) ([]byte, error) {
	f, err := z.load()
	if err != nil {
		return nil, err
	}
	return fs.ReadFile(f, name)
}

// ReadDir implements fs.ReadDirFS.
func (z *FS) ReadDir(name string) ([]fs.DirEntry, error) {
	f, err := z.load()
	if err != nil {
		return nil, err
	}
	return fs.ReadDir(f, name)
}
