package sdk

import (
	"testing"

	"github.com/kfet/fir/pkg/zfs"
)

func TestSDKBlobFresh(t *testing.T) {
	paths := []string{"python/fir_ext.py", "python/forkserver.py", "python/examples", "node/fir_ext.js", "node/pi_compat.js", "node/run.sh"}
	if err := zfs.Diff(sdkBlob, ".", paths); err != nil {
		t.Fatalf("sdk.tar.gz is stale (run `go generate ./pkg/extension/sdk`): %v", err)
	}
}
