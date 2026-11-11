package extension

import (
	"fmt"
	"os"
	"testing"
)

// TestMain points XDG_CONFIG_HOME at an empty directory so discovery never
// picks up the developer's real global extensions (~/.config/fir/extensions),
// which would change the registered-tool counts the tests assert.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "fir-ext-test-xdg-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	_ = os.Setenv("XDG_CONFIG_HOME", dir)
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
