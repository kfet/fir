package extension

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/pinoauth"
)

// fakeDeviceServer serves an OpenAI-style device-code grant plus a token
// endpoint.
type fakeDeviceServer struct {
	srv          *httptest.Server
	usercodeCode int           // usercode HTTP status
	approve      chan struct{} // closed → poll returns the grant
	usercodeHits atomic.Int32
	userAgent    atomic.Value
	mu           sync.Mutex
	tokenBody    string
}

func newFakeDeviceServer(t *testing.T, usercodeStatus int) *fakeDeviceServer {
	t.Helper()
	old := minDeviceInterval
	minDeviceInterval = 10 * time.Millisecond
	t.Cleanup(func() { minDeviceInterval = old })

	f := &fakeDeviceServer{usercodeCode: usercodeStatus, approve: make(chan struct{})}
	mux := http.NewServeMux()
	mux.HandleFunc("/usercode", func(w http.ResponseWriter, r *http.Request) {
		f.usercodeHits.Add(1)
		f.userAgent.Store(r.Header.Get("User-Agent"))
		if f.usercodeCode != http.StatusOK {
			w.WriteHeader(f.usercodeCode)
			return
		}
		_, _ = w.Write([]byte(`{"device_auth_id":"DAID","user_code":"ABCD-1234","interval":"0"}`))
	})
	mux.HandleFunc("/poll", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-f.approve:
			_, _ = w.Write([]byte(`{"authorization_code":"DEVCODE","code_verifier":"DEVVER","code_challenge":"x"}`))
		default:
			w.WriteHeader(http.StatusForbidden)
		}
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.tokenBody = string(b)
		f.mu.Unlock()
		_, _ = w.Write([]byte(`{"access_token":"AT","refresh_token":"RT","expires_in":3600}`))
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeDeviceServer) body() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tokenBody
}

// deviceLogin registers a provider with a device_code spec bound to
// callbackAddr and runs Login with cb.
func deviceLogin(t *testing.T, f *fakeDeviceServer, callbackAddr string, cb pinoauth.LoginCallbacks) error {
	t.Helper()
	return deviceLoginCtx(context.Background(), t, f, callbackAddr, cb)
}

func deviceLoginCtx(ctx context.Context, t *testing.T, f *fakeDeviceServer, callbackAddr string, cb pinoauth.LoginCallbacks) error {
	t.Helper()
	ai.ResetOAuthProviders()
	t.Cleanup(ai.ResetOAuthProviders)
	caps := &InitResult{Name: "test-ext", AuthProviders: []AuthProviderSpec{{
		ID: "test-dev", Name: "Dev", UsesCallbackServer: true,
		Flow: &OAuthFlowSpec{
			ClientID: "cid", AuthorizeURL: f.srv.URL + "/authorize", TokenURL: f.srv.URL + "/token",
			CallbackAddr: callbackAddr, CallbackPath: "/auth/callback",
			OpenURLInstructions: "Complete the sign-in in your browser.",
			DeviceCode: &DeviceCodeSpec{
				UserCodeURL: f.srv.URL + "/usercode", PollURL: f.srv.URL + "/poll",
				VerificationURL: "https://example.test/device", RedirectURI: "https://example.test/deviceauth/callback",
				Hint:    "If disabled, enable it.",
				Headers: map[string]string{"User-Agent": "codex-like"},
			},
		},
	}}}
	bridge := NewBridge(nil, caps)
	bridge.RegisterAuthProviders()
	t.Cleanup(bridge.UnregisterAuthProviders)
	_, err := ai.GetOAuthProvider("test-dev").Login(ctx, cb)
	return err
}

func authQuery(t *testing.T, raw string) url.Values {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query()
}

func TestDeviceCode_DeviceWins(t *testing.T) {
	f := newFakeDeviceServer(t, http.StatusOK)
	var instructions string
	var dismissed atomic.Bool
	release := make(chan struct{})
	defer close(release)
	err := deviceLogin(t, f, "127.0.0.1:0", pinoauth.LoginCallbacks{
		OnAuth: func(info pinoauth.AuthInfo) {
			instructions = info.Instructions
			close(f.approve)
		},
		OnManualCodeInput: func() (string, error) {
			<-release
			return "", fmt.Errorf("dismissed")
		},
		OnDismissManualInput: func() { dismissed.Store(true) },
	})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	browser := strings.Index(instructions, "Complete the sign-in")
	device := strings.Index(instructions, "https://example.test/device and enter code: ABCD-1234")
	if browser != 0 || device < 0 || !strings.Contains(instructions, "If disabled, enable it.") {
		t.Errorf("instructions = %q: want browser text first, then device link+code+hint", instructions)
	}
	body := f.body()
	for _, frag := range []string{"code=DEVCODE", "code_verifier=DEVVER", "redirect_uri=" + url.QueryEscape("https://example.test/deviceauth/callback")} {
		if !strings.Contains(body, frag) {
			t.Errorf("token body missing %q in %q", frag, body)
		}
	}
	if !dismissed.Load() {
		t.Error("device win must dismiss the paste prompt")
	}
	if ua, _ := f.userAgent.Load().(string); ua != "codex-like" {
		t.Errorf("usercode User-Agent = %q", ua)
	}
	if n := f.usercodeHits.Load(); n != 1 {
		t.Errorf("usercode requested %d times, want 1", n)
	}
}

func TestDeviceCode_CallbackWins(t *testing.T) {
	f := newFakeDeviceServer(t, http.StatusOK) // never approved
	var redirect string
	err := deviceLogin(t, f, "127.0.0.1:0", pinoauth.LoginCallbacks{
		OnAuth: func(info pinoauth.AuthInfo) {
			q := authQuery(t, info.URL)
			redirect = q.Get("redirect_uri")
			go func() {
				resp, err := http.Get(redirect + "?code=CB&state=" + url.QueryEscape(q.Get("state")))
				if err == nil {
					resp.Body.Close()
				}
			}()
		},
	})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	body := f.body()
	if !strings.Contains(body, "code=CB") || !strings.Contains(body, "redirect_uri="+url.QueryEscape(redirect)) {
		t.Errorf("token body %q: want loopback code + redirect", body)
	}
	if strings.Contains(body, "DEVVER") {
		t.Error("callback win must use fir's PKCE verifier")
	}
}

func TestDeviceCode_PasteWins(t *testing.T) {
	f := newFakeDeviceServer(t, http.StatusOK)
	var shown string
	err := deviceLogin(t, f, "127.0.0.1:0", pinoauth.LoginCallbacks{
		OnAuth: func(info pinoauth.AuthInfo) { shown = info.URL },
		OnManualCodeInput: func() (string, error) {
			q := authQuery(t, shown)
			return q.Get("redirect_uri") + "?code=PASTE&state=" + url.QueryEscape(q.Get("state")), nil
		},
	})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if body := f.body(); !strings.Contains(body, "code=PASTE") {
		t.Errorf("token body %q: want pasted code", body)
	}
}

func TestDeviceCode_UserCodeFailureOmitsSection(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, 530, http.StatusInternalServerError} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			f := newFakeDeviceServer(t, status)
			var instructions string
			var shown string
			err := deviceLogin(t, f, "127.0.0.1:0", pinoauth.LoginCallbacks{
				OnAuth: func(info pinoauth.AuthInfo) { instructions, shown = info.Instructions, info.URL },
				OnManualCodeInput: func() (string, error) {
					q := authQuery(t, shown)
					return q.Get("redirect_uri") + "?code=P&state=" + url.QueryEscape(q.Get("state")), nil
				},
			})
			if err != nil {
				t.Fatalf("Login: %v", err)
			}
			if instructions != "Complete the sign-in in your browser." {
				t.Errorf("instructions = %q: device section must be omitted", instructions)
			}
		})
	}
}

// A taken fixed callback port must not abort the login: the device code
// and a pasted failed-localhost URL still work.
func TestDeviceCode_BindFailureContinues(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	addr := ln.Addr().String()
	_, port, _ := net.SplitHostPort(addr)

	t.Run("paste", func(t *testing.T) {
		f := newFakeDeviceServer(t, http.StatusOK)
		var shown string
		err := deviceLogin(t, f, addr, pinoauth.LoginCallbacks{
			OnAuth: func(info pinoauth.AuthInfo) { shown = info.URL },
			OnManualCodeInput: func() (string, error) {
				q := authQuery(t, shown)
				return q.Get("redirect_uri") + "?code=PB&state=" + url.QueryEscape(q.Get("state")), nil
			},
		})
		if err != nil {
			t.Fatalf("Login: %v", err)
		}
		want := "redirect_uri=" + url.QueryEscape("http://localhost:"+port+"/auth/callback")
		if body := f.body(); !strings.Contains(body, "code=PB") || !strings.Contains(body, want) {
			t.Errorf("token body %q: want pasted code with %s", body, want)
		}
	})
	t.Run("device without paste handler", func(t *testing.T) {
		f := newFakeDeviceServer(t, http.StatusOK)
		err := deviceLogin(t, f, addr, pinoauth.LoginCallbacks{
			OnAuth: func(pinoauth.AuthInfo) { close(f.approve) },
		})
		if err != nil {
			t.Fatalf("Login: %v", err)
		}
		if body := f.body(); !strings.Contains(body, "code=DEVCODE") {
			t.Errorf("token body %q: want device code", body)
		}
	})
}

// Cancelling the login ends every source (listener, paste, device poll)
// and Login returns instead of hanging.
func TestDeviceCode_CancelStopsAll(t *testing.T) {
	f := newFakeDeviceServer(t, http.StatusOK)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := deviceLoginCtx(ctx, t, f, "127.0.0.1:0", pinoauth.LoginCallbacks{
		OnAuth: func(pinoauth.AuthInfo) { cancel() },
		OnManualCodeInput: func() (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		},
	})
	if err == nil {
		t.Fatal("cancelled login must fail")
	}
}

func TestPollDevice_ContextCancel(t *testing.T) {
	f := newFakeDeviceServer(t, http.StatusOK)
	dc := &DeviceCodeSpec{PollURL: f.srv.URL + "/poll"}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := pollDevice(ctx, dc, &deviceSession{authID: "a", userCode: "b", interval: minDeviceInterval})
		done <- err
	}()
	cancel()
	if err := <-done; err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Errorf("pollDevice after cancel: err = %v", err)
	}
}

// 429 and 5xx while polling are transient; another 4xx ends the source.
func TestPollDevice_Statuses(t *testing.T) {
	old := minDeviceInterval
	minDeviceInterval = time.Millisecond
	defer func() { minDeviceInterval = old }()
	run := func(seq ...int) error {
		var n atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			i := int(n.Add(1)) - 1
			if i < len(seq) {
				w.WriteHeader(seq[i])
				return
			}
			_, _ = w.Write([]byte(`{"authorization_code":"C","code_verifier":"V"}`))
		}))
		defer srv.Close()
		_, err := pollDevice(context.Background(), &DeviceCodeSpec{PollURL: srv.URL}, &deviceSession{interval: time.Millisecond})
		return err
	}
	if err := run(403, 404, 429, 500, 530); err != nil {
		t.Errorf("transient statuses: %v", err)
	}
	if err := run(400); err == nil || !strings.Contains(err.Error(), "HTTP 400") {
		t.Errorf("400: err = %v", err)
	}
}

func TestParseInterval(t *testing.T) {
	old := minDeviceInterval
	minDeviceInterval = time.Second
	defer func() { minDeviceInterval = old }()
	for in, want := range map[any]time.Duration{
		"5": 5 * time.Second, float64(3): 3 * time.Second, nil: 5 * time.Second,
		"junk": 5 * time.Second, "0": time.Second,
	} {
		if got := parseInterval(in); got != want {
			t.Errorf("parseInterval(%v) = %v, want %v", in, got, want)
		}
	}
}
