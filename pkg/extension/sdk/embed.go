package sdk

import (
	_ "embed"

	"github.com/kfet/fir/pkg/zfs"
)

//go:generate go run ../../../cmd/zfsgen -root . -o sdk.tar.gz python/fir_ext.py python/forkserver.py python/examples node/fir_ext.js node/pi_compat.js node/run.sh

//go:embed sdk.tar.gz
var sdkBlob []byte

// EmbeddedSDKs contains the lightweight SDK stubs for external extensions.
// Fir extracts these to <cache>/fir/sdks/<version>/ (see pkg/cache.Dir) at runtime so that
// extension processes can import them without bundling protocol code.
//
// Test files are deliberately left out: they ship nothing to users and
// cost binary size.
var EmbeddedSDKs = zfs.New(sdkBlob)
