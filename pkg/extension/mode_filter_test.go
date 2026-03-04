package extension

import "testing"

func TestExtensionSupportsMode(t *testing.T) {
	tests := []struct {
		name   string
		modes  []string
		active string
		want   bool
	}{
		{name: "empty means all", modes: nil, active: "acp", want: true},
		{name: "exact match", modes: []string{"acp"}, active: "acp", want: true},
		{name: "non match", modes: []string{"interactive"}, active: "acp", want: false},
		{name: "tui alias", modes: []string{"tui"}, active: "interactive", want: true},
		{name: "jsonrpc alias", modes: []string{"json-rpc"}, active: "rpc", want: true},
		{name: "print matches json", modes: []string{"print"}, active: "json", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extensionSupportsMode(tt.modes, tt.active); got != tt.want {
				t.Fatalf("extensionSupportsMode(%v, %q) = %v, want %v", tt.modes, tt.active, got, tt.want)
			}
		})
	}
}
