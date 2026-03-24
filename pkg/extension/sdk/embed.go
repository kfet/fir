package sdk

import "embed"

// EmbeddedSDKs contains the lightweight SDK stubs for external extensions.
// Fir extracts these to ~/.cache/fir/sdks/<version>/ at runtime so that
// extension processes can import them without bundling protocol code.
//
//go:embed python node
var EmbeddedSDKs embed.FS
