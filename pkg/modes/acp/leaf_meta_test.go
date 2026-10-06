package acp

import (
	"context"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"
)

func TestPromptResponse_LeafIdMeta_ForkableAtLeaf(t *testing.T) {
	pa, cwd := newRehydrateAgent(t)
	writeForkParent(t, pa.agentDir, cwd)
	resp, err := pa.resumeSessionLocal(context.Background(), ResumeSessionRequest{SessionId: "parent-uuid", Cwd: cwd, At: "e2"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if e, ok := pa.removeSession(resp.SessionId); ok {
			pa.teardownSession(context.Background(), resp.SessionId, e)
		}
	})
	entry := pa.lookupSession(resp.SessionId)
	pr := entry.promptResponse()
	if pr.StopReason != acpsdk.StopReasonEndTurn {
		t.Errorf("stopReason = %q", pr.StopReason)
	}
	leaf, _ := pr.Meta["leafId"].(string)
	if leaf != "e2" {
		t.Fatalf("_meta.leafId = %v, want e2", pr.Meta)
	}
	// The reported leaf is accepted as session/fork _meta.at.
	childID, child, err := pa.forkSessionLocal(context.Background(), ForkSessionRequest{
		SessionId: resp.SessionId, Cwd: cwd, Meta: map[string]any{"at": leaf},
	})
	if err != nil {
		t.Fatalf("fork at leafId: %v", err)
	}
	t.Cleanup(func() {
		if e, ok := pa.removeSession(childID); ok {
			pa.teardownSession(context.Background(), childID, e)
		}
	})
	if got := child.session.SessionStore.GetLeafID(); got != "e2" {
		t.Errorf("child leaf = %q", got)
	}
}

func TestPromptResponse_NoStore_OmitsMeta(t *testing.T) {
	if pr := (&firSession{}).promptResponse(); pr.Meta != nil {
		t.Errorf("meta = %v", pr.Meta)
	}
}

func TestMergeMeta(t *testing.T) {
	got := mergeMeta(map[string]any{"image": true, "_meta": map[string]any{"x": 1}}, "leafId")
	m := got["_meta"].(map[string]any)
	if got["image"] != true || m["x"] != 1 || m["leafId"] != true {
		t.Errorf("got %v", got)
	}
	if mergeMeta(nil, "k")["_meta"].(map[string]any)["k"] != true {
		t.Error("nil input")
	}
}
