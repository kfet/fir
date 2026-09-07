package main

import (
	"errors"
	"testing"
)

func TestParseUpdateArgs(t *testing.T) {
	tests := []struct {
		args    []string
		want    bool
		wantErr bool
	}{
		{args: nil},
		{args: []string{}},
		{args: []string{"-check"}, want: true},
		{args: []string{"--check"}, want: true},
		{args: []string{"-check", "--check"}, want: true},
		// A typo must not fall through to a real self-update.
		{args: []string{"-checkx"}, wantErr: true},
		{args: []string{"check"}, wantErr: true},
		{args: []string{"-check", "-force"}, wantErr: true},
	}
	for _, tc := range tests {
		got, err := parseUpdateArgs(tc.args)
		if (err != nil) != tc.wantErr {
			t.Errorf("parseUpdateArgs(%v) error = %v, wantErr %v", tc.args, err, tc.wantErr)
			continue
		}
		if err == nil && got != tc.want {
			t.Errorf("parseUpdateArgs(%v) = %v, want %v", tc.args, got, tc.want)
		}
	}
}

// `fir update -check` reports through its exit status, so a fleet script can
// branch on it without parsing text: 3 means an update is available.
func TestErrExitCode(t *testing.T) {
	err := error(errExitCode(3))
	var code errExitCode
	if !errors.As(err, &code) || int(code) != 3 {
		t.Fatalf("errors.As did not recover the status from %v", err)
	}
	if err.Error() != "exit status 3" {
		t.Fatalf("Error() = %q", err.Error())
	}
}
