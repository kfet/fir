package extension

import "strings"

func extensionSupportsMode(modes []string, activeMode string) bool {
	if len(modes) == 0 {
		return true
	}
	active := normalizeExtensionMode(activeMode)
	if active == "" {
		return true
	}
	for _, m := range modes {
		norm := normalizeExtensionMode(m)
		switch norm {
		case "", "all", "*":
			return true
		case "print":
			if active == "text" || active == "json" {
				return true
			}
		default:
			if norm == active {
				return true
			}
		}
	}
	return false
}

func normalizeExtensionMode(mode string) string {
	m := strings.ToLower(strings.TrimSpace(mode))
	switch m {
	case "tui":
		return "interactive"
	case "json-rpc", "jsonrpc":
		return "rpc"
	case "", "interactive", "text", "json", "rpc", "acp", "print":
		return m
	default:
		return m
	}
}
