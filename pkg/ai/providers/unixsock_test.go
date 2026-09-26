package providers

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/kfet/fir/pkg/ai"
)

func TestSplitUnixURL(t *testing.T) {
	cases := []struct {
		in, sock, rest string
		ok, err        bool
	}{
		{"https://x/y", "", "", false, false},
		{"unix:///run/boxres/bifrost.sock/anthropic/v1/messages", "/run/boxres/bifrost.sock", "/anthropic/v1/messages", true, false},
		{"unix:///tmp/a.sock", "/tmp/a.sock", "/", true, false},
		{"unix:///tmp/a.sock/v1?x=1", "/tmp/a.sock", "/v1?x=1", true, false},
		{"unix:///tmp/a.sock/b.sock/v1", "/tmp/a.sock", "/b.sock/v1", true, false},
		{"unix:///tmp/nosock/v1", "", "", true, true},
	}
	for _, c := range cases {
		sock, rest, ok, err := SplitUnixURL(c.in)
		if sock != c.sock || rest != c.rest || ok != c.ok || (err != nil) != c.err {
			t.Errorf("%s: got (%q,%q,%v,%v)", c.in, sock, rest, ok, err)
		}
	}
}

type unixReq struct {
	path, host string
	header     http.Header
}

// serveUnix starts an HTTP server on a fresh unix socket that replies with the
// given SSE fixture and records each request.
func serveUnix(t *testing.T, fixture string) (string, <-chan unixReq) {
	t.Helper()
	dir, err := os.MkdirTemp("", "fus")
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "p.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	data := loadFixture(t, fixture)
	reqs := make(chan unixReq, 4)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs <- unixReq{r.URL.RequestURI(), r.Host, r.Header.Clone()}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write(data)
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close(); os.RemoveAll(dir) })
	return sock, reqs
}

func TestUnixSocket_AnthropicStreamNoKey(t *testing.T) {
	sock, reqs := serveUnix(t, "anthropic_simple_response.sse")
	model := testModel("unix://"+sock+"/anthropic", ai.ApiAnthropicMessages, "bifrost-unix")
	s := StreamSimpleAnthropic(context.Background(), model, ai.Context{Messages: []ai.Message{ai.NewUserMsg("hi", 1)}}, nil)
	collectEvents(t, s)
	res := s.Result()
	if res == nil || res.StopReason == ai.StopReasonError {
		t.Fatalf("unexpected result: %+v", res)
	}
	r := <-reqs
	if r.path != "/anthropic/v1/messages" || r.host != "localhost" {
		t.Errorf("path=%q host=%q", r.path, r.host)
	}
	if r.header.Get("x-api-key") != "" || r.header.Get("Authorization") != "" {
		t.Errorf("auth header sent: %v", r.header)
	}
}

func TestUnixSocket_OpenAIStreamNoKey(t *testing.T) {
	sock, reqs := serveUnix(t, "openai_simple_response.sse")
	model := testModel("unix://"+sock+"/openai/v1", ai.ApiOpenAICompletions, "bifrost-unix")
	s := StreamSimpleOpenAICompletions(context.Background(), model, ai.Context{Messages: []ai.Message{ai.NewUserMsg("hi", 1)}}, nil)
	collectEvents(t, s)
	res := s.Result()
	if res == nil || res.StopReason == ai.StopReasonError {
		t.Fatalf("unexpected result: %+v", res)
	}
	r := <-reqs
	if r.path != "/openai/v1/chat/completions" {
		t.Errorf("path=%q", r.path)
	}
	if r.header.Get("Authorization") != "" {
		t.Errorf("auth header sent: %v", r.header)
	}
}

func TestUnixSocket_BadURL(t *testing.T) {
	for _, u := range []string{"unix:///tmp/nosock/x", "unix://tmp/a.sock/x"} {
		unixBadURL(t, u)
	}
}

func unixBadURL(t *testing.T, u string) {
	model := testModel(u, ai.ApiOpenAICompletions, "bifrost-unix")
	s := StreamSimpleOpenAICompletions(context.Background(), model, ai.Context{Messages: []ai.Message{ai.NewUserMsg("hi", 1)}}, nil)
	collectEvents(t, s)
	if res := s.Result(); res == nil || res.StopReason != ai.StopReasonError {
		t.Fatalf("expected error, got %+v", res)
	}
}
