package pkg

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestParseSource(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
		// expected fields (zero value means "don't check")
		wantType   string
		wantHost   string
		wantPath   string
		wantURL    string
		wantRef    string
		wantPinned bool
		// For local: just check the suffix of the resolved path.
		wantLocalSuffix string
	}{
		// ---- local paths ----
		{
			name:            "dot-relative",
			input:           "./some/pkg",
			wantType:        "local",
			wantLocalSuffix: "some/pkg",
		},
		{
			name:            "absolute",
			input:           "/tmp/mypkg",
			wantType:        "local",
			wantLocalSuffix: "tmp/mypkg",
		},
		{
			name:            "tilde-home",
			input:           "~/foo/bar",
			wantType:        "local",
			wantLocalSuffix: "foo/bar",
		},
		{
			name:            "double-dot-relative",
			input:           "../sibling",
			wantType:        "local",
			wantLocalSuffix: "sibling",
		},

		// ---- SSH URLs ----
		{
			name:     "ssh-basic",
			input:    "git@github.com:user/repo",
			wantType: "git",
			wantHost: "github.com",
			wantPath: "user/repo",
			wantURL:  "https://github.com/user/repo",
		},
		{
			name:       "ssh-with-ref",
			input:      "git@github.com:user/repo@main",
			wantType:   "git",
			wantHost:   "github.com",
			wantPath:   "user/repo",
			wantURL:    "https://github.com/user/repo",
			wantRef:    "main",
			wantPinned: true,
		},
		{
			name:     "ssh-dot-git-stripped",
			input:    "git@github.com:user/repo.git",
			wantType: "git",
			wantHost: "github.com",
			wantPath: "user/repo",
			wantURL:  "https://github.com/user/repo",
		},

		// ---- HTTPS URLs ----
		{
			name:     "https-basic",
			input:    "https://github.com/user/repo",
			wantType: "git",
			wantHost: "github.com",
			wantPath: "user/repo",
			wantURL:  "https://github.com/user/repo",
		},
		{
			name:       "https-with-ref",
			input:      "https://github.com/user/repo@v1.2.3",
			wantType:   "git",
			wantHost:   "github.com",
			wantPath:   "user/repo",
			wantURL:    "https://github.com/user/repo",
			wantRef:    "v1.2.3",
			wantPinned: true,
		},
		{
			name:     "https-dot-git-stripped",
			input:    "https://github.com/user/repo.git",
			wantType: "git",
			wantHost: "github.com",
			wantPath: "user/repo",
			wantURL:  "https://github.com/user/repo",
		},
		{
			name:     "https-non-github-host",
			input:    "https://gitlab.example.com/org/proj",
			wantType: "git",
			wantHost: "gitlab.example.com",
			wantPath: "org/proj",
			wantURL:  "https://gitlab.example.com/org/proj",
		},

		// ---- bare host/path ----
		{
			name:     "bare-github",
			input:    "github.com/user/repo",
			wantType: "git",
			wantHost: "github.com",
			wantPath: "user/repo",
			wantURL:  "https://github.com/user/repo",
		},
		{
			name:       "bare-with-ref",
			input:      "github.com/user/repo@abc123",
			wantType:   "git",
			wantHost:   "github.com",
			wantPath:   "user/repo",
			wantURL:    "https://github.com/user/repo",
			wantRef:    "abc123",
			wantPinned: true,
		},
		{
			name:     "bare-deep-path",
			input:    "gitlab.com/org/sub/repo",
			wantType: "git",
			wantHost: "gitlab.com",
			wantPath: "org/sub/repo",
			wantURL:  "https://gitlab.com/org/sub/repo",
		},

		// ---- no-dot host → local fallback ----
		{
			name:            "no-dot-host-treated-local",
			input:           "justname/repo",
			wantType:        "local",
			wantLocalSuffix: "justname/repo",
		},

		// ---- error cases ----
		{
			name:    "empty",
			input:   "",
			wantErr: true,
		},
		{
			name:    "ssh-missing-colon",
			input:   "git@github.com/user/repo",
			wantErr: true,
		},
		{
			name:    "https-no-path",
			input:   "https://github.com",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseSource(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (src=%+v)", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tc.wantType != "" && got.Type != tc.wantType {
				t.Errorf("Type: got %q, want %q", got.Type, tc.wantType)
			}
			if tc.wantHost != "" && got.Host != tc.wantHost {
				t.Errorf("Host: got %q, want %q", got.Host, tc.wantHost)
			}
			if tc.wantPath != "" && got.Path != tc.wantPath {
				t.Errorf("Path: got %q, want %q", got.Path, tc.wantPath)
			}
			if tc.wantURL != "" && got.URL != tc.wantURL {
				t.Errorf("URL: got %q, want %q", got.URL, tc.wantURL)
			}
			if tc.wantRef != "" && got.Ref != tc.wantRef {
				t.Errorf("Ref: got %q, want %q", got.Ref, tc.wantRef)
			}
			if tc.wantPinned && !got.Pinned {
				t.Errorf("Pinned: got false, want true")
			}
			if !tc.wantPinned && got.Pinned {
				t.Errorf("Pinned: got true, want false")
			}
			if tc.wantLocalSuffix != "" {
				want := filepath.FromSlash(tc.wantLocalSuffix)
				if !strings.HasSuffix(got.Local, want) {
					t.Errorf("Local: got %q, want suffix %q", got.Local, want)
				}
			}
			if got.Raw != tc.input {
				t.Errorf("Raw: got %q, want %q", got.Raw, tc.input)
			}
		})
	}
}
