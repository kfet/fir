package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/auth"
	"github.com/kfet/pinoauth"
)

// These tests exercise `fir auth refresh <provider|provider#account>` argument
// handling against a TEMP auth.json and a stub OAuth provider. The real
// ~/.config/fir/auth.json is never opened: a refresh grant rotates (and on
// Anthropic immediately revokes) the live credential, so it must never be part
// of a test run.

// stubRefreshProvider mints a fresh, per-call token triple so a test can prove
// which slot was actually refreshed, and counts the grants it was asked for.
type stubRefreshProvider struct {
	id    string
	calls atomic.Int64
}

func (p *stubRefreshProvider) ID() string               { return p.id }
func (p *stubRefreshProvider) Name() string             { return "Stub " + p.id }
func (p *stubRefreshProvider) UsesCallbackServer() bool { return false }
func (p *stubRefreshProvider) Login(context.Context, pinoauth.LoginCallbacks) (*ai.OAuthCredentials, error) {
	return nil, nil
}
func (p *stubRefreshProvider) ListModels(context.Context, *ai.OAuthCredentials) ([]string, error) {
	return nil, nil
}
func (p *stubRefreshProvider) GetAPIKey(c *ai.OAuthCredentials) string {
	if c == nil {
		return ""
	}
	return c.Access
}
func (p *stubRefreshProvider) ModifyModels([]*ai.Model, *ai.OAuthCredentials) []*ai.Model { return nil }
func (p *stubRefreshProvider) ModelDefaults(string, []*ai.Model) *ai.Model                { return nil }

func (p *stubRefreshProvider) RefreshToken(_ context.Context, c *ai.OAuthCredentials) (*ai.OAuthCredentials, error) {
	p.calls.Add(1)
	return &ai.OAuthCredentials{
		Access:  "new-access",
		Refresh: "new-refresh",
		Expires: time.Now().Add(8 * time.Hour).UnixMilli(),
	}, nil
}

// tempAuthStorage writes a temp auth.json with the given slots and returns
// storage over it. Nothing here can reach the user's real credential file.
func tempAuthStorage(t *testing.T, data auth.AuthStorageData) *auth.AuthStorage {
	t.Helper()
	path := filepath.Join(t.TempDir(), "auth.json")
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		t.Fatalf("marshal auth data: %v", err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatalf("write temp auth.json: %v", err)
	}
	return auth.NewAuthStorage(path)
}

func staleOAuth(label string) auth.AuthCredential {
	return auth.AuthCredential{
		Type:    auth.CredentialTypeOAuth,
		Access:  "old-access-" + label,
		Refresh: "old-refresh-" + label,
		Expires: time.Now().Add(-time.Minute).UnixMilli(),
		Label:   label,
	}
}

// TestRefreshTarget_SlotKeyRefreshesOnlyThatSlot: the reported bug. A slot key
// pasted back from `fir auth refresh` / `fir login list` output must target
// exactly that account and leave its siblings untouched.
func TestRefreshTarget_SlotKeyRefreshesOnlyThatSlot(t *testing.T) {
	prov := &stubRefreshProvider{id: "slotprov-one"}
	ai.RegisterOAuthProvider(prov)

	st := tempAuthStorage(t, auth.AuthStorageData{
		"slotprov-one":       staleOAuth("default@x.com"),
		"slotprov-one#work":  staleOAuth("work@x.com"),
		"slotprov-one#other": staleOAuth("other@x.com"),
	})

	var errBuf bytes.Buffer
	results, err := refreshTargetResults(context.Background(), st,
		"slotprov-one#work", auth.DefaultRefreshWindow, false, &errBuf)
	if err != nil {
		t.Fatalf("err = %v, want nil (stderr: %s)", err, errBuf.String())
	}
	if len(results) != 1 || results[0].SlotKey != "slotprov-one#work" {
		t.Fatalf("results = %+v, want exactly the targeted slot", results)
	}
	if results[0].Outcome != auth.OutcomeRefreshed {
		t.Fatalf("outcome = %s (%v), want refreshed", results[0].Outcome, results[0].Err)
	}
	if prov.calls.Load() != 1 {
		t.Errorf("refresh grants = %d, want exactly 1", prov.calls.Load())
	}
	if got := st.Get("slotprov-one#work").Refresh; got != "new-refresh" {
		t.Errorf("targeted slot not rotated: %q", got)
	}
	for _, sibling := range []string{"slotprov-one", "slotprov-one#other"} {
		if got := st.Get(sibling).Refresh; !strings.HasPrefix(got, "old-refresh-") {
			t.Errorf("sibling %s was touched: refresh = %q", sibling, got)
		}
	}
}

// TestRefreshTarget_UnknownSlotIsHelpful: a valid provider with an unknown
// account must say so and list the stored slot keys — not fall through to the
// "unknown OAuth provider" path, and not silently refresh everything.
func TestRefreshTarget_UnknownSlotIsHelpful(t *testing.T) {
	prov := &stubRefreshProvider{id: "slotprov-two"}
	ai.RegisterOAuthProvider(prov)

	st := tempAuthStorage(t, auth.AuthStorageData{
		"slotprov-two":      staleOAuth("default@x.com"),
		"slotprov-two#work": staleOAuth("work@x.com"),
	})

	var errBuf bytes.Buffer
	results, err := refreshTargetResults(context.Background(), st,
		"slotprov-two#nope", auth.DefaultRefreshWindow, false, &errBuf)
	if err == nil {
		t.Fatal("err = nil, want an unknown-slot error")
	}
	if results != nil {
		t.Errorf("results = %+v, want none", results)
	}
	if prov.calls.Load() != 0 {
		t.Errorf("refresh grants = %d, want 0 — nothing may be refreshed", prov.calls.Load())
	}
	if !strings.Contains(err.Error(), "unknown account slot: slotprov-two#nope") {
		t.Errorf("error should name the slot: %v", err)
	}
	if strings.Contains(err.Error(), "unknown OAuth provider") {
		t.Errorf("must not fall through to the unknown-provider path: %v", err)
	}
	out := errBuf.String()
	for _, want := range []string{"slotprov-two", "slotprov-two#work", "work@x.com"} {
		if !strings.Contains(out, want) {
			t.Errorf("stderr missing %q:\n%s", want, out)
		}
	}
}

// TestRefreshTarget_UnknownSlotNoAccountsStored: same path with nothing stored
// for the provider points at `fir login`.
func TestRefreshTarget_UnknownSlotNoAccountsStored(t *testing.T) {
	ai.RegisterOAuthProvider(&stubRefreshProvider{id: "slotprov-empty"})

	st := tempAuthStorage(t, auth.AuthStorageData{"unrelated": staleOAuth("x")})

	var errBuf bytes.Buffer
	_, err := refreshTargetResults(context.Background(), st,
		"slotprov-empty#work", auth.DefaultRefreshWindow, false, &errBuf)
	if err == nil {
		t.Fatal("err = nil, want an unknown-slot error")
	}
	if !strings.Contains(errBuf.String(), "fir login slotprov-empty") {
		t.Errorf("stderr should suggest login:\n%s", errBuf.String())
	}
}

// TestRefreshTarget_BareProviderRefreshesAllSlots: the pre-existing behaviour
// is unchanged when the argument carries no account half.
func TestRefreshTarget_BareProviderRefreshesAllSlots(t *testing.T) {
	prov := &stubRefreshProvider{id: "slotprov-three"}
	ai.RegisterOAuthProvider(prov)

	st := tempAuthStorage(t, auth.AuthStorageData{
		"slotprov-three":      staleOAuth("default@x.com"),
		"slotprov-three#work": staleOAuth("work@x.com"),
		"other-prov":          staleOAuth("elsewhere@x.com"),
	})

	var errBuf bytes.Buffer
	results, err := refreshTargetResults(context.Background(), st,
		"slotprov-three", auth.DefaultRefreshWindow, false, &errBuf)
	if err != nil {
		t.Fatalf("err = %v, want nil (stderr: %s)", err, errBuf.String())
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2: %+v", len(results), results)
	}
	if results[0].SlotKey != "slotprov-three" || results[1].SlotKey != "slotprov-three#work" {
		t.Errorf("slot order wrong: %s, %s", results[0].SlotKey, results[1].SlotKey)
	}
	if prov.calls.Load() != 2 {
		t.Errorf("refresh grants = %d, want 2", prov.calls.Load())
	}
	if got := st.Get("other-prov").Refresh; got != "old-refresh-elsewhere@x.com" {
		t.Errorf("unrelated provider was touched: %q", got)
	}
}

// TestRefreshTarget_DefaultSlotKeyForm: "provider#default" and "provider#" both
// name the bare default slot, since that is how SlotKey composes it.
func TestRefreshTarget_DefaultSlotKeyForm(t *testing.T) {
	prov := &stubRefreshProvider{id: "slotprov-four"}
	ai.RegisterOAuthProvider(prov)

	st := tempAuthStorage(t, auth.AuthStorageData{
		"slotprov-four":      staleOAuth("default@x.com"),
		"slotprov-four#work": staleOAuth("work@x.com"),
	})

	var errBuf bytes.Buffer
	results, err := refreshTargetResults(context.Background(), st,
		"slotprov-four#default", auth.DefaultRefreshWindow, false, &errBuf)
	if err != nil {
		t.Fatalf("err = %v, want nil (stderr: %s)", err, errBuf.String())
	}
	if len(results) != 1 || results[0].SlotKey != "slotprov-four" {
		t.Fatalf("results = %+v, want just the default slot", results)
	}
	if got := st.Get("slotprov-four#work").Refresh; got != "old-refresh-work@x.com" {
		t.Errorf("named slot was touched: %q", got)
	}
}

// TestRefreshTarget_InvalidProviderHalfNamesProviderHalf: with a `#` in the
// argument and a bogus provider half, the error must name the provider half —
// so it lines up with the provider list printed beneath it — not the whole
// slot key.
func TestRefreshTarget_InvalidProviderHalfNamesProviderHalf(t *testing.T) {
	ai.RegisterOAuthProvider(&stubRefreshProvider{id: "slotprov-five"})

	st := tempAuthStorage(t, auth.AuthStorageData{"slotprov-five": staleOAuth("d@x.com")})

	var errBuf bytes.Buffer
	_, err := refreshTargetResults(context.Background(), st,
		"nosuchprov#kalin@example.com-inventory", auth.DefaultRefreshWindow, false, &errBuf)
	if err == nil {
		t.Fatal("err = nil, want an unknown-provider error")
	}
	if err.Error() != "unknown OAuth provider: nosuchprov" {
		t.Errorf("error must name the provider half only: %v", err)
	}
	out := errBuf.String()
	if !strings.Contains(out, "Unknown OAuth provider: nosuchprov\n") {
		t.Errorf("stderr must name the provider half:\n%s", out)
	}
	if strings.Contains(out, "nosuchprov#kalin") {
		t.Errorf("stderr should not echo the account half as a provider:\n%s", out)
	}
}

// TestRefreshTarget_EmptyProviderHalf: "#work" has no provider half to name, so
// the error reports the raw argument rather than a blank provider id.
func TestRefreshTarget_EmptyProviderHalf(t *testing.T) {
	st := tempAuthStorage(t, auth.AuthStorageData{"slotprov-six": staleOAuth("d@x.com")})

	var errBuf bytes.Buffer
	_, err := refreshTargetResults(context.Background(), st,
		"#work", auth.DefaultRefreshWindow, false, &errBuf)
	if err == nil {
		t.Fatal("err = nil, want an unknown-provider error")
	}
	if err.Error() != "unknown OAuth provider: #work" {
		t.Errorf("error should quote the raw argument: %v", err)
	}
	if !strings.Contains(errBuf.String(), "Unknown OAuth provider: #work\n") {
		t.Errorf("stderr should quote the raw argument:\n%s", errBuf.String())
	}
}
