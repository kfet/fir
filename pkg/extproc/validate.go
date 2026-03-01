package extproc

import (
	"fmt"
	"strings"
	"unicode"
)

// ValidateExtensionName checks that name is a reasonable extension identifier.
// If name is empty, fallback is returned instead. Returns an error if the name
// contains path separators or control characters.
func ValidateExtensionName(name, fallback string) (string, error) {
	if name == "" {
		return fallback, nil
	}
	if strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("extproc: extension name %q contains path separators", name)
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("extproc: extension name %q contains control characters", name)
		}
	}
	return name, nil
}
