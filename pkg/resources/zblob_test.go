package resources

import (
	"testing"

	"github.com/kfet/fir/pkg/zfs"
)

func TestBuiltinBlobFresh(t *testing.T) {
	if err := zfs.Diff(builtinBlob, ".", []string{"builtin_skills", "builtin_extensions"}); err != nil {
		t.Fatalf("builtin.tar.gz is stale (run `go generate ./pkg/resources`): %v", err)
	}
}
