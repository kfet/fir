package main

import (
	"errors"
	"testing"
)

func TestUpdateCheckOnly(t *testing.T) {
	tests := []struct {
		args []string
		want bool
	}{
		{nil, false},
		{[]string{}, false},
		{[]string{"-check"}, true},
		{[]string{"--check"}, true},
		{[]string{"-v", "--check"}, true},
		{[]string{"check"}, false},
		{[]string{"-checkx"}, false},
	}
	for _, tc := range tests {
		if got := updateCheckOnly(tc.args); got != tc.want {
			t.Errorf("updateCheckOnly(%v) = %v, want %v", tc.args, got, tc.want)
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
