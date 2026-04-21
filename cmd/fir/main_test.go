package main

import "testing"

func TestParseChdirFlag(t *testing.T) {
	cases := []struct {
		name        string
		args        []string
		wantDir     string
		wantIdx     int
		wantConsume int
		wantFound   bool
		wantErr     bool
	}{
		{"none", []string{"sessions"}, "", 0, 0, false, false},
		{"short separate", []string{"-C", "/tmp", "sessions"}, "/tmp", 0, 2, true, false},
		{"short equals", []string{"-C=/tmp", "sessions"}, "/tmp", 0, 1, true, false},
		{"long cwd separate", []string{"--cwd", "dir"}, "dir", 0, 2, true, false},
		{"long cwd equals", []string{"--cwd=dir"}, "dir", 0, 1, true, false},
		{"long directory equals", []string{"--directory=x"}, "x", 0, 1, true, false},
		{"later position", []string{"sessions", "-C", "/tmp"}, "/tmp", 1, 2, true, false},
		{"missing arg", []string{"-C"}, "", 0, 0, true, true},
		{"empty equals", []string{"-C="}, "", 0, 0, true, true},
		{"empty long", []string{"--cwd="}, "", 0, 0, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, idx, consume, found, err := parseChdirFlag(tc.args)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if dir != tc.wantDir || idx != tc.wantIdx || consume != tc.wantConsume || found != tc.wantFound {
				t.Fatalf("got dir=%q idx=%d consume=%d found=%v; want %q %d %d %v",
					dir, idx, consume, found, tc.wantDir, tc.wantIdx, tc.wantConsume, tc.wantFound)
			}
		})
	}
}
