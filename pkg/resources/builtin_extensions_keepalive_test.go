package resources

import "testing"

func TestParseCommentFrontmatter_KeepAlive(t *testing.T) {
	for _, key := range []string{"keep_alive", "keep-alive"} {
		fm := ParseCommentFrontmatter("# ---\n# name: x\n# " + key + ": true\n# ---\n")
		if !fm.KeepAlive {
			t.Errorf("%s: KeepAlive not set", key)
		}
	}
	if ParseCommentFrontmatter("# ---\n# name: x\n# ---\n").KeepAlive {
		t.Error("KeepAlive set without the key")
	}
}
