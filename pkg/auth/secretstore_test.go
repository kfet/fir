package auth

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/kfet/fir/pkg/config"
	"github.com/zalando/go-keyring"
)

// useMockKeyring routes defaultSecretStore to go-keyring's in-memory mock.
func useMockKeyring(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		keyring.MockInitWithError(err)
	} else {
		keyring.MockInit()
	}
	t.Setenv(config.AuthStorageEnv, "")
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func newKeyringStorage(t *testing.T, mode string) (*AuthStorage, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "auth.json")
	return newAuthStorage(NewSecureFileBackend(p, KeyringStore{}, mode)), p
}

func TestSecureBackend_KeyringModeStripsSecrets(t *testing.T) {
	useMockKeyring(t, nil)
	s, p := newKeyringStorage(t, config.AuthStorageKeyring)
	if err := s.Set("openai", AuthCredential{Type: CredentialTypeAPIKey, Key: "sk-secret", Label: "work"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("anthropic", AuthCredential{Type: CredentialTypeOAuth, Access: "acc", Refresh: "ref", Expires: 42,
		Extra: map[string]any{"secretKey": "aws"}}); err != nil {
		t.Fatal(err)
	}
	raw := readFile(t, p)
	for _, secret := range []string{"sk-secret", "acc", "ref", "aws"} {
		if strings.Contains(raw, `"`+secret+`"`) {
			t.Fatalf("auth.json still holds %q:\n%s", secret, raw)
		}
	}
	if !strings.Contains(raw, storageMarkerKey) || !strings.Contains(raw, `"work"`) {
		t.Fatalf("auth.json lacks marker or metadata:\n%s", raw)
	}
	// A fresh process reads the secrets back.
	s2 := newAuthStorage(NewSecureFileBackend(p, KeyringStore{}, config.AuthStorageFile))
	if got := s2.Get("openai"); got == nil || got.Key != "sk-secret" {
		t.Fatalf("openai = %+v", got)
	}
	if got := s2.Get("anthropic"); got == nil || got.Refresh != "ref" || got.Expires != 42 || got.Extra["secretKey"] != "aws" {
		t.Fatalf("anthropic = %+v", got)
	}
	if s2.Has(storageMarkerKey) {
		t.Fatal("marker leaked into credentials")
	}
	// Remove deletes the keyring item.
	if err := s2.Remove("openai"); err != nil {
		t.Fatal(err)
	}
	b := NewSecureFileBackend(p, KeyringStore{}, config.AuthStorageFile)
	if _, err := (KeyringStore{}).Get(b.account("openai")); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("keyring item survived Remove: %v", err)
	}
}

func TestSecureBackend_AutoFallsBackToFile(t *testing.T) {
	useMockKeyring(t, errors.New("no dbus"))
	s, p := newKeyringStorage(t, config.AuthStorageAuto)
	if err := s.Set("openai", AuthCredential{Type: CredentialTypeAPIKey, Key: "sk-secret"}); err != nil {
		t.Fatal(err)
	}
	if raw := readFile(t, p); !strings.Contains(raw, "sk-secret") || strings.Contains(raw, storageMarkerKey) {
		t.Fatalf("expected plain auth.json:\n%s", raw)
	}
}

func TestSecureBackend_KeyringModeErrorsWithoutStore(t *testing.T) {
	useMockKeyring(t, errors.New("no dbus"))
	s, _ := newKeyringStorage(t, config.AuthStorageKeyring)
	if err := s.Set("openai", AuthCredential{Type: CredentialTypeAPIKey, Key: "k"}); err == nil {
		t.Fatal("want error when keyring mode has no store")
	}
}

func TestSecureBackend_NoSilentDowngradeAfterMigration(t *testing.T) {
	useMockKeyring(t, nil)
	s, p := newKeyringStorage(t, config.AuthStorageKeyring)
	if err := s.Set("openai", AuthCredential{Type: CredentialTypeAPIKey, Key: "k"}); err != nil {
		t.Fatal(err)
	}
	keyring.MockInitWithError(errors.New("locked"))
	s2 := newAuthStorage(NewSecureFileBackend(p, KeyringStore{}, config.AuthStorageFile))
	if s2.loadError == nil {
		t.Fatal("want load error when migrated store is unavailable")
	}
	if err := s2.Set("x", AuthCredential{Type: CredentialTypeAPIKey, Key: "y"}); err == nil {
		t.Fatal("want write error, not a plain-file downgrade")
	}
	if raw := readFile(t, p); strings.Contains(raw, `"y"`) {
		t.Fatalf("secret written to file:\n%s", raw)
	}
}

func TestMigrateAuthStorage_RoundTrip(t *testing.T) {
	useMockKeyring(t, nil)
	dir := t.TempDir()
	p := filepath.Join(dir, "auth.json")
	plain := AuthStorageData{"openai": {Type: CredentialTypeAPIKey, Key: "sk-1"}}
	b, _ := json.Marshal(plain)
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := MigrateAuthStorage(p, config.AuthStorageKeyring); err != nil {
		t.Fatal(err)
	}
	if raw := readFile(t, p); strings.Contains(raw, "sk-1") {
		t.Fatalf("not stripped:\n%s", raw)
	}
	st, err := AuthStorageStatus(p)
	if err != nil || st.Location != "keyring" || st.SlotCount != 1 || st.ProbeErr != nil {
		t.Fatalf("status = %+v, %v", st, err)
	}
	// Idempotent.
	if err := MigrateAuthStorage(p, config.AuthStorageKeyring); err != nil {
		t.Fatal(err)
	}
	if err := MigrateAuthStorage(p, config.AuthStorageFile); err != nil {
		t.Fatal(err)
	}
	if raw := readFile(t, p); !strings.Contains(raw, "sk-1") || strings.Contains(raw, storageMarkerKey) {
		t.Fatalf("not restored:\n%s", raw)
	}
	acct := NewSecureFileBackend(p, KeyringStore{}, "").account("openai")
	if _, err := (KeyringStore{}).Get(acct); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("keyring item not cleaned: %v", err)
	}
	t.Setenv(config.AuthStorageEnv, "auto")
	if err := MigrateAuthStorage(p, config.AuthStorageFile); err == nil {
		t.Fatal("want refusal while auth.storage would re-migrate")
	}
	if err := MigrateAuthStorage(p, "bogus"); err == nil {
		t.Fatal("want error for bogus target")
	}
}

// lockedStore is a goroutine-safe SecretStore. go-keyring's mock is a bare
// map, and the race detector cannot see the flock that serialises writers.
type lockedStore struct {
	mu sync.Mutex
	m  map[string]string
}

func (l *lockedStore) Get(a string) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	v, ok := l.m[a]
	if !ok {
		return "", ErrSecretNotFound
	}
	return v, nil
}

func (l *lockedStore) Set(a, v string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.m[a] = v
	return nil
}

func (l *lockedStore) Delete(a string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.m, a)
	return nil
}

func TestSecureBackend_ConcurrentWriters(t *testing.T) {
	store := &lockedStore{m: map[string]string{}}
	p := filepath.Join(t.TempDir(), "auth.json")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s := newAuthStorage(NewSecureFileBackend(p, store, config.AuthStorageKeyring))
			slot := "p" + string(rune('a'+i))
			if err := s.Set(slot, AuthCredential{Type: CredentialTypeAPIKey, Key: "k" + slot}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	s := newAuthStorage(NewSecureFileBackend(p, store, config.AuthStorageFile))
	for i := 0; i < 8; i++ {
		slot := "p" + string(rune('a'+i))
		if got := s.Get(slot); got == nil || got.Key != "k"+slot {
			t.Fatalf("%s = %+v", slot, got)
		}
	}
}

func TestLoadAuthStorageModeViaNewAuthStorage(t *testing.T) {
	useMockKeyring(t, nil)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"auth":{"storage":"auto"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewAuthStorage(filepath.Join(dir, "auth.json"))
	if err := s.Set("openai", AuthCredential{Type: CredentialTypeAPIKey, Key: "sk-x"}); err != nil {
		t.Fatal(err)
	}
	if raw := readFile(t, filepath.Join(dir, "auth.json")); strings.Contains(raw, "sk-x") {
		t.Fatalf("auto mode with a working store should migrate:\n%s", raw)
	}
}

func TestSecureBackend_LoadErrors(t *testing.T) {
	useMockKeyring(t, nil)
	p := filepath.Join(t.TempDir(), "auth.json")
	b := NewSecureFileBackend(p, KeyringStore{}, config.AuthStorageKeyring)

	// Corrupt plain file: raw bytes pass through, no migration attempted.
	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := b.WithLock(func(cur []byte) (any, []byte) { return string(cur), nil })
	if err != nil || got != "{not json" {
		t.Fatalf("passthrough = %v, %v", got, err)
	}

	// Unknown marker type is refused.
	if err := os.WriteFile(p, []byte(`{"_fir_storage":{"type":"vault"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := b.WithLock(func([]byte) (any, []byte) { return nil, nil }); err == nil {
		t.Fatal("want error for unknown marker")
	}

	// Undecodable keyring item is refused.
	if err := os.WriteFile(p, []byte(`{"_fir_storage":{"type":"keyring"},"x":{"type":"api_key"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (KeyringStore{}).Set(b.account("x"), "garbage"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.WithLock(func([]byte) (any, []byte) { return nil, nil }); err == nil {
		t.Fatal("want error for undecodable item")
	}
}

// setFailStore answers the probe but rejects real writes.
type setFailStore struct{ lockedStore }

func (s *setFailStore) Set(a, v string) error {
	if strings.HasSuffix(a, "probe") {
		return s.lockedStore.Set(a, v)
	}
	return errors.New("keychain locked")
}

func TestSecureBackend_AutoMigrationFailureKeepsFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(p, []byte(`{"x":{"type":"api_key","key":"sk"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &setFailStore{lockedStore{m: map[string]string{}}}
	s := newAuthStorage(NewSecureFileBackend(p, store, config.AuthStorageAuto))
	if s.loadError != nil {
		t.Fatalf("auto mode must not fail reads on a migration error: %v", s.loadError)
	}
	if got := s.Get("x"); got == nil || got.Key != "sk" {
		t.Fatalf("x = %+v", got)
	}
	if raw := readFile(t, p); !strings.Contains(raw, `"sk"`) {
		t.Fatalf("plain file lost:\n%s", raw)
	}
}
