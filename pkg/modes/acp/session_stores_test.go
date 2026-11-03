package acp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kfet/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/session/store"
)

// archiveFixture configures a read-only store "zbox" holding one session
// (transcript + .meta.json, old enough not to look mid-sync) and returns
// the agent dir and the archived transcript path.
func archiveFixture(t *testing.T) (agentDir, archived string) {
	t.Helper()
	agentDir = t.TempDir()
	t.Setenv("FIR_AGENT_DIR", agentDir)
	zbox := t.TempDir()
	store.ConfigureStores(store.SessionsDir(agentDir), []store.Store{{Label: "zbox", Path: zbox}})
	t.Cleanup(func() { store.ConfigureStores("", nil) })

	dir := store.SessionDirForRoot(zbox, "/home/u/proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	id := "9f1c2d3e-0000-4000-8000-000000000001"
	archived = filepath.Join(dir, "2026-09-01T10-00-00-000000000Z_"+id+".jsonl")
	body := `{"type":"session","version":3,"id":"` + id + `","timestamp":"2026-09-01T10:00:00Z","cwd":"/home/u/proj"}` + "\n" +
		`{"type":"message","message":{"role":"user","content":"hi"}}` + "\n"
	if err := os.WriteFile(archived, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	meta, _ := json.Marshal(store.MetaSidecar{ID: id, Cwd: "/home/u/proj", Name: "archived-one", MessageCount: 1, ModTime: old})
	if err := os.WriteFile(archived+".meta.json", meta, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(archived, old, old); err != nil {
		t.Fatal(err)
	}
	return agentDir, archived
}

func TestIsValidSessionPath_SessionStores(t *testing.T) {
	agentDir, archived := archiveFixture(t)
	if !isValidSessionPath(archived, agentDir) {
		t.Error("a transcript inside a configured store must be accepted")
	}
	for _, p := range []string{
		"/tmp/evil/session.jsonl",
		filepath.Dir(filepath.Dir(archived)) + "-evil/x.jsonl", // shared prefix, outside
		filepath.Join(filepath.Dir(archived), "..", "..", "..", "etc", "passwd"),
	} {
		if isValidSessionPath(p, agentDir) {
			t.Errorf("path outside every store accepted: %s", p)
		}
	}
}

func TestResolveSessionByUUID_SessionStores(t *testing.T) {
	agentDir, archived := archiveFixture(t)
	id := strings.TrimSuffix(filepath.Base(archived)[strings.Index(filepath.Base(archived), "_")+1:], ".jsonl")
	if got := resolveSessionByUUID(id, agentDir, "/elsewhere"); got != archived {
		t.Errorf("resolveSessionByUUID = %q, want %q", got, archived)
	}
}

func TestListSessions_IncludesSessionStores(t *testing.T) {
	_, archived := archiveFixture(t)
	pa := &firAgent{sessions: make(map[string]*firSession)}
	resp, err := pa.listSessionsLocal(context.Background(), ListSessionsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Sessions) != 1 {
		t.Fatalf("sessions = %+v", resp.Sessions)
	}
	s := resp.Sessions[0]
	if s.SessionId != archived || s.Title == nil || *s.Title != "[zbox] archived-one" {
		t.Errorf("session = %+v (title %v)", s, s.Title)
	}
	if s.Meta["fir/store"] != "zbox" || s.Meta["fir/readOnly"] != true {
		t.Errorf("_meta = %v", s.Meta)
	}
	raw, _ := json.Marshal(s)
	if !strings.Contains(string(raw), `"_meta":{"fir/readOnly":true,"fir/store":"zbox"}`) {
		t.Errorf("wire shape = %s", raw)
	}

	// Cwd-scoped listing matches the archive's recorded project dir.
	resp, _ = pa.listSessionsLocal(context.Background(), ListSessionsRequest{Cwd: "/home/u/proj"})
	if len(resp.Sessions) != 1 {
		t.Errorf("cwd-scoped sessions = %+v", resp.Sessions)
	}
	resp, _ = pa.listSessionsLocal(context.Background(), ListSessionsRequest{Cwd: "/home/u/other"})
	if len(resp.Sessions) != 0 {
		t.Errorf("other cwd should list nothing: %+v", resp.Sessions)
	}
}

func TestResumeCandidates_NewestFirstAcrossStores(t *testing.T) {
	agentDir, archived := archiveFixture(t)
	ss := store.NewSessionStore("/home/u/proj", store.DefaultSessionDir(agentDir, "/home/u/proj"))
	ss.AppendAgentMessage(agent.NewAgentMessage(ai.NewUserMsg("older local", time.Now().UnixMilli())))
	local := ss.GetSessionFile()
	ss.Close()
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(local, old, old); err != nil {
		t.Fatal(err)
	}
	got := resumeCandidates(agentDir, "/home/u/proj")
	if len(got) != 2 || got[0].Path != archived || got[1].Path != local {
		t.Fatalf("candidates = %+v", got)
	}
}
