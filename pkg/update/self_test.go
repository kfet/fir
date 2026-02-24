package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// ============================================================================
// verifySHA256
// ============================================================================

func TestVerifySHA256_Valid(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "sha256test")
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("hello, world")
	if _, err := f.Write(content); err != nil {
		t.Fatal(err)
	}
	f.Close()

	h := sha256.Sum256(content)
	expected := hex.EncodeToString(h[:])

	if err := verifySHA256(f.Name(), expected); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

func TestVerifySHA256_WrongHash(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "sha256test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	f.Close()

	err = verifySHA256(f.Name(), strings.Repeat("0", 64))
	if err == nil {
		t.Error("expected error for wrong hash, got nil")
	}
}

func TestVerifySHA256_MissingFile(t *testing.T) {
	err := verifySHA256(t.TempDir()+"/nonexistent", strings.Repeat("0", 64))
	if err == nil {
		t.Error("expected error for missing file, got nil")
	}
}

// ============================================================================
// downloadText
// ============================================================================

func TestDownloadText_OK(t *testing.T) {
	const body = "abc123  fir-linux-amd64\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(body)) //nolint:errcheck
	}))
	defer srv.Close()

	got, err := downloadText(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != body {
		t.Errorf("got %q, want %q", got, body)
	}
}

func TestDownloadText_ContextCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := downloadText(ctx, srv.URL)
	if err == nil {
		t.Error("expected error from cancelled context, got nil")
	}
}

// ============================================================================
// downloadFile
// ============================================================================

func TestDownloadFile_OK(t *testing.T) {
	const content = "binary content"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(content)) //nolint:errcheck
	}))
	defer srv.Close()

	dest := t.TempDir() + "/out.bin"
	if err := downloadFile(context.Background(), srv.URL, dest); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("reading dest: %v", err)
	}
	if string(data) != content {
		t.Errorf("got %q, want %q", string(data), content)
	}
}

func TestDownloadFile_NonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	}))
	defer srv.Close()

	dest := t.TempDir() + "/out.bin"
	err := downloadFile(context.Background(), srv.URL, dest)
	if err == nil {
		t.Error("expected error for 404 response, got nil")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("expected 404 in error message, got: %v", err)
	}
}

func TestDownloadFile_ContextCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	dest := t.TempDir() + "/out.bin"
	err := downloadFile(ctx, srv.URL, dest)
	if err == nil {
		t.Error("expected error from cancelled context, got nil")
	}
}

// ============================================================================
// SelfUpdate — error paths only (we don't overwrite the test binary)
// ============================================================================

func TestSelfUpdate_NoAssetURL(t *testing.T) {
	rel := &Release{
		Version:  "v1.0.0",
		AssetURL: "",
	}
	err := SelfUpdate(context.Background(), rel)
	if err == nil {
		t.Error("expected error when AssetURL is empty, got nil")
	}
	// With no asset URL, HTTPS is skipped and gh fallback is attempted.
	// The error should mention gh or private repos.
	errMsg := err.Error()
	if !strings.Contains(errMsg, "gh") && !strings.Contains(errMsg, "private") &&
		!strings.Contains(errMsg, "no pre-built binary") {
		t.Errorf("expected gh/private-repo guidance in error, got: %v", err)
	}
}

func TestSelfUpdate_DownloadFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	}))
	defer srv.Close()

	rel := &Release{
		Version:  "v1.0.0",
		AssetURL: srv.URL + "/fir-linux-amd64",
	}
	err := SelfUpdate(context.Background(), rel)
	if err == nil {
		t.Error("expected error when download returns 404, got nil")
	}
	// HTTPS download fails, then gh fallback is attempted.
	// The error should mention gh or private repos.
	errMsg := err.Error()
	if !strings.Contains(errMsg, "gh") && !strings.Contains(errMsg, "download") {
		t.Errorf("expected gh/download guidance in error, got: %v", err)
	}
}

func TestSelfUpdate_ChecksumMismatch(t *testing.T) {
	const binaryContent = "fake binary content"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "checksums.txt"):
			// Return a deliberately wrong checksum for the binary.
			wrongChecksum := strings.Repeat("a", 64) + "  fir-" + CurrentPlatform() + "\n"
			w.Write([]byte(wrongChecksum)) //nolint:errcheck
		default:
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(binaryContent)) //nolint:errcheck
		}
	}))
	defer srv.Close()

	rel := &Release{
		Version:      "v1.0.0",
		AssetURL:     srv.URL + "/fir-" + CurrentPlatform(),
		ChecksumsURL: srv.URL + "/checksums.txt",
	}
	err := SelfUpdate(context.Background(), rel)
	if err == nil {
		t.Error("expected checksum error, got nil")
	}
	if !strings.Contains(err.Error(), "checksum") {
		t.Errorf("expected 'checksum' in error, got: %v", err)
	}
}
