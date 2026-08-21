package apikind

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
)

// fakeHandler records what the registry hands back to callers.
type fakeHandler struct {
	name         string
	registered   []string
	unregistered []string
	err          error
}

func (f *fakeHandler) Register(id string, payload json.RawMessage, sourceID string) error {
	f.registered = append(f.registered, fmt.Sprintf("%s|%s|%s", id, string(payload), sourceID))
	return f.err
}

func (f *fakeHandler) Unregister(id string) {
	f.unregistered = append(f.unregistered, id)
}

// TestGetUnknownKindIsNil pins the lookup miss. Callers branch on a nil
// handler to decide whether an ApiSpec kind is supported at all, so a miss
// must be a plain nil interface — not a typed nil, which would pass an
// `h != nil` check and then panic on use.
func TestGetUnknownKindIsNil(t *testing.T) {
	if h := Get("no-such-kind"); h != nil {
		t.Fatalf("Get on an unregistered kind = %#v, want nil", h)
	}
	if h := Get(""); h != nil {
		t.Fatalf("Get(\"\") = %#v, want nil", h)
	}
}

// TestRegisterAndGet pins that the handler a provider package installs is the
// one a caller gets back, and that it is usable through the interface.
func TestRegisterAndGet(t *testing.T) {
	want := &fakeHandler{name: "decl-google"}
	Register("test-decl-google", want)

	got := Get("test-decl-google")
	if got != Handler(want) {
		t.Fatalf("Get returned %#v, want the registered handler", got)
	}

	if err := got.Register("gemini-x", json.RawMessage(`{"baseUrl":"https://x"}`), "ext:demo"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if len(want.registered) != 1 || want.registered[0] != `gemini-x|{"baseUrl":"https://x"}|ext:demo` {
		t.Fatalf("handler saw %v, want the id, payload and sourceID passed through verbatim", want.registered)
	}

	got.Unregister("gemini-x")
	if len(want.unregistered) != 1 || want.unregistered[0] != "gemini-x" {
		t.Fatalf("Unregister saw %v, want [gemini-x]", want.unregistered)
	}
}

// TestRegisterErrorPropagates pins that a handler's registration failure
// reaches the caller unchanged — the bridge reports it to the extension.
func TestRegisterErrorPropagates(t *testing.T) {
	boom := fmt.Errorf("bad spec")
	Register("test-failing", &fakeHandler{err: boom})

	err := Get("test-failing").Register("id", nil, "src")
	if err != boom {
		t.Fatalf("Register error = %v, want %v", err, boom)
	}
}

// TestRegisterReplaces pins last-writer-wins: re-registering a kind replaces
// the previous handler rather than being ignored or panicking, which is what
// lets a provider package's init() be re-run in tests.
func TestRegisterReplaces(t *testing.T) {
	first := &fakeHandler{name: "first"}
	second := &fakeHandler{name: "second"}

	Register("test-replaced", first)
	Register("test-replaced", second)

	got := Get("test-replaced")
	if got != Handler(second) {
		t.Fatalf("Get returned the %q handler, want %q", got.(*fakeHandler).name, second.name)
	}
	if err := got.Register("id", nil, "src"); err != nil {
		t.Fatal(err)
	}
	if len(first.registered) != 0 {
		t.Errorf("the replaced handler must not be called: %v", first.registered)
	}
}

// TestKindsAreIndependent pins that registering one kind does not disturb
// another — several provider packages register from init() concurrently at
// process start.
func TestKindsAreIndependent(t *testing.T) {
	a := &fakeHandler{name: "a"}
	b := &fakeHandler{name: "b"}
	Register("test-kind-a", a)
	Register("test-kind-b", b)

	if Get("test-kind-a") != Handler(a) || Get("test-kind-b") != Handler(b) {
		t.Fatal("registering a second kind clobbered the first")
	}
}

// TestConcurrentRegisterAndGet pins that the registry is safe for the real
// access pattern: provider init()s registering while the extension bridge
// looks kinds up. Run under -race this fails if the mutex is dropped.
func TestConcurrentRegisterAndGet(t *testing.T) {
	const n = 32
	var wg sync.WaitGroup
	start := make(chan struct{})

	for i := 0; i < n; i++ {
		kind := fmt.Sprintf("test-conc-%d", i)
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			Register(kind, &fakeHandler{name: kind})
		}()
		go func() {
			defer wg.Done()
			<-start
			_ = Get(kind)
		}()
	}
	close(start)
	wg.Wait()

	for i := 0; i < n; i++ {
		kind := fmt.Sprintf("test-conc-%d", i)
		h := Get(kind)
		if h == nil {
			t.Fatalf("%s went missing after concurrent registration", kind)
		}
		if got := h.(*fakeHandler).name; got != kind {
			t.Fatalf("%s resolved to handler %q", kind, got)
		}
	}
}
