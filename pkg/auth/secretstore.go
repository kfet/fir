package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/kfet/fir/pkg/config"
	firlog "github.com/kfet/fir/pkg/log"
	"github.com/zalando/go-keyring"
)

// SecretStore is an OS-level secret store: macOS Keychain, the freedesktop
// Secret Service on Linux (GNOME Keyring, KWallet), or the Windows Credential
// Manager. Get returns ErrSecretNotFound for a missing item.
type SecretStore interface {
	Get(account string) (string, error)
	Set(account, secret string) error
	Delete(account string) error
}

// ErrSecretNotFound is returned by SecretStore.Get for a missing item.
var ErrSecretNotFound = errors.New("secret not found")

// keyringService is the service name every fir secret is stored under.
const keyringService = "fir"

// probeTimeout bounds the startup probe of the OS secret store. A Linux host
// without a session bus can otherwise stall on D-Bus autolaunch.
const probeTimeout = 5 * time.Second

// KeyringStore is the SecretStore backed by github.com/zalando/go-keyring.
type KeyringStore struct{}

func (KeyringStore) Get(account string) (string, error) {
	v, err := keyring.Get(keyringService, account)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrSecretNotFound
	}
	return v, err
}

func (KeyringStore) Set(account, secret string) error {
	return keyring.Set(keyringService, account, secret)
}

func (KeyringStore) Delete(account string) error {
	err := keyring.Delete(keyringService, account)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}

// defaultSecretStore is swapped by tests.
var defaultSecretStore SecretStore = KeyringStore{}

// ProbeSecretStore checks that store can set, read back and delete an item.
func ProbeSecretStore(store SecretStore, account string) error {
	done := make(chan error, 1)
	go func() {
		const want = "fir-probe"
		if err := store.Set(account, want); err != nil {
			done <- err
			return
		}
		got, err := store.Get(account)
		_ = store.Delete(account)
		if err == nil && got != want {
			err = errors.New("read back a different value")
		}
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(probeTimeout):
		return fmt.Errorf("no answer within %s", probeTimeout)
	}
}

// storageMarkerKey is the auth.json entry that records "secrets live in the
// OS secret store". It is shaped like a credential ({"type":"keyring"}) so an
// older fir build still parses the file. Once present, it is authoritative:
// fir reads the secret store or fails — it never silently falls back to the
// stripped file. Only `fir auth storage file` removes it.
const storageMarkerKey = "_fir_storage"

const markerTypeKeyring CredentialType = "keyring"

// secretFields is the part of a credential kept in the secret store.
type secretFields struct {
	Key     string         `json:"key,omitempty"`
	Access  string         `json:"access,omitempty"`
	Refresh string         `json:"refresh,omitempty"`
	Extra   map[string]any `json:"extra,omitempty"`
}

func (f secretFields) empty() bool {
	return f.Key == "" && f.Access == "" && f.Refresh == "" && len(f.Extra) == 0
}

func splitSecrets(c AuthCredential) (AuthCredential, secretFields) {
	s := secretFields{Key: c.Key, Access: c.Access, Refresh: c.Refresh, Extra: c.Extra}
	c.Key, c.Access, c.Refresh, c.Extra = "", "", "", nil
	return c, s
}

func mergeSecrets(c AuthCredential, s secretFields) AuthCredential {
	c.Key, c.Access, c.Refresh, c.Extra = s.Key, s.Access, s.Refresh, s.Extra
	return c
}

// SecureFileBackend keeps credential metadata (and the flock that serialises
// every read-modify-write) in auth.json and, once migrated, the secrets in an
// OS SecretStore — one item per credential slot, so no item outgrows the
// macOS Keychain's size limits. Before migration it behaves exactly like
// FileAuthStorageBackend.
type SecureFileBackend struct {
	file  *FileAuthStorageBackend
	store SecretStore
	mode  string
	// prefix namespaces item accounts per auth.json path, so two config dirs
	// on one machine never share secrets.
	prefix string

	probeOnce sync.Once
	probeErr  error
}

// NewSecureFileBackend wraps auth.json at authPath with store under mode
// (config.AuthStorageFile/Auto/Keyring).
func NewSecureFileBackend(authPath string, store SecretStore, mode string) *SecureFileBackend {
	abs, err := filepath.Abs(authPath)
	if err != nil {
		abs = authPath
	}
	sum := sha256.Sum256([]byte(abs))
	return &SecureFileBackend{
		file:   NewFileAuthStorageBackend(authPath),
		store:  store,
		mode:   mode,
		prefix: "auth:" + hex.EncodeToString(sum[:6]) + ":",
	}
}

// Stamp delegates to auth.json: every write rewrites it, so a secret-only
// rotation still changes the stamp.
func (b *SecureFileBackend) Stamp() string { return b.file.Stamp() }

func (b *SecureFileBackend) account(slot string) string { return b.prefix + slot }

func (b *SecureFileBackend) probe() error {
	b.probeOnce.Do(func() { b.probeErr = ProbeSecretStore(b.store, b.prefix+"probe") })
	return b.probeErr
}

// wantKeyring reports whether a plain (unmigrated) file should move to the
// secret store, per mode. In keyring mode an unavailable store is an error.
func (b *SecureFileBackend) wantKeyring() (bool, error) {
	switch b.mode {
	case config.AuthStorageKeyring:
		if err := b.probe(); err != nil {
			return false, fmt.Errorf("auth.storage is keyring but the OS secret store is unavailable: %w", err)
		}
		return true, nil
	case config.AuthStorageAuto:
		if err := b.probe(); err != nil {
			warnOnce.Do(func() {
				firlog.Log("auth: OS secret store unavailable (%v); keeping credentials in auth.json", err)
			})
			return false, nil
		}
		return true, nil
	}
	return false, nil
}

// warnOnce limits the auto-mode fallback warning to one per process.
var warnOnce sync.Once

// load decodes auth.json and, when migrated, fills in secrets from the store.
func (b *SecureFileBackend) load(raw []byte) (data AuthStorageData, migrated bool, err error) {
	data, err = decodeStorageData(raw)
	if err != nil {
		return nil, false, err
	}
	marker, migrated := data[storageMarkerKey]
	if !migrated {
		return data, false, nil
	}
	if marker.Type != markerTypeKeyring {
		return nil, true, fmt.Errorf("auth.json: unknown storage marker type %q", marker.Type)
	}
	delete(data, storageMarkerKey)
	for slot, cred := range data {
		v, err := b.store.Get(b.account(slot))
		if errors.Is(err, ErrSecretNotFound) {
			// A slot without secrets, or one deleted from the keychain by
			// hand: it reads as logged out, which is what it now is.
			if cred.Type == CredentialTypeAPIKey || cred.Type == CredentialTypeOAuth {
				firlog.Log("auth: no OS secret store item for %q; treating it as logged out", slot)
			}
			continue
		}
		if err != nil {
			return nil, true, fmt.Errorf("read %q from OS secret store: %w", slot, err)
		}
		var s secretFields
		if err := json.Unmarshal([]byte(v), &s); err != nil {
			return nil, true, fmt.Errorf("decode %q from OS secret store: %w", slot, err)
		}
		data[slot] = mergeSecrets(cred, s)
	}
	return data, true, nil
}

// encodeKeyring writes every slot's secrets to the store (verifying each by
// read-back), deletes items of slots in prev but not in next, and returns the
// stripped auth.json content carrying the marker.
func (b *SecureFileBackend) encodeKeyring(next, prev AuthStorageData) ([]byte, error) {
	out := make(AuthStorageData, len(next)+1)
	for _, slot := range sortedSlots(next) {
		meta, sec := splitSecrets(next[slot])
		acct := b.account(slot)
		if sec.empty() {
			if err := b.store.Delete(acct); err != nil {
				return nil, fmt.Errorf("delete %q from OS secret store: %w", slot, err)
			}
		} else if _, prevSec := splitSecrets(prev[slot]); !reflect.DeepEqual(sec, prevSec) {
			blob, err := json.Marshal(sec)
			if err != nil {
				return nil, err
			}
			if err := b.store.Set(acct, string(blob)); err != nil {
				return nil, fmt.Errorf("write %q to OS secret store: %w", slot, err)
			}
			if got, err := b.store.Get(acct); err != nil || got != string(blob) {
				return nil, fmt.Errorf("verify %q in OS secret store: read-back mismatch (%v)", slot, err)
			}
		}
		out[slot] = meta
	}
	for slot := range prev {
		if _, ok := next[slot]; !ok {
			if err := b.store.Delete(b.account(slot)); err != nil {
				return nil, fmt.Errorf("delete %q from OS secret store: %w", slot, err)
			}
		}
	}
	out[storageMarkerKey] = AuthCredential{Type: markerTypeKeyring}
	return json.MarshalIndent(out, "", "  ")
}

func sortedSlots(d AuthStorageData) []string {
	keys := make([]string, 0, len(d))
	for k := range d {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (b *SecureFileBackend) WithLock(fn func(current []byte) (result any, next []byte)) (any, error) {
	return b.WithLockFallible(func(current []byte) (any, []byte, error) {
		r, n := fn(current)
		return r, n, nil
	})
}

func (b *SecureFileBackend) WithLockFallible(fn func(current []byte) (result any, next []byte, err error)) (any, error) {
	return b.file.WithLockFallible(func(raw []byte) (any, []byte, error) {
		data, migrated, err := b.load(raw)
		if err != nil {
			if !migrated {
				// Unparseable plain file: hand the raw bytes through, exactly
				// like the file backend, so the caller's corruption handling
				// (keep in-memory creds, allow a login to overwrite) applies.
				return fn(raw)
			}
			return nil, nil, err
		}
		toKeyring := migrated
		if !migrated {
			if toKeyring, err = b.wantKeyring(); err != nil {
				return nil, nil, err
			}
		}
		current := raw
		if migrated {
			if current, err = json.MarshalIndent(data, "", "  "); err != nil {
				return nil, nil, err
			}
			if len(data) == 0 {
				current = nil
			}
		}
		result, next, err := fn(current)
		if err != nil {
			return nil, nil, err
		}
		if !toKeyring {
			return result, next, nil
		}
		target := data
		if next != nil {
			if target, err = decodeStorageData(next); err != nil {
				return nil, nil, err
			}
			delete(target, storageMarkerKey)
		} else if migrated {
			return result, nil, nil // nothing changed
		}
		// Plain file → keyring migration happens here, on the first locked
		// access in auto/keyring mode: secrets are copied and verified before
		// the stripped file replaces the plain one.
		var prev AuthStorageData
		if migrated {
			prev = data
		}
		out, err := b.encodeKeyring(target, prev)
		if err != nil {
			if !migrated && b.mode == config.AuthStorageAuto {
				// auto never makes auth unusable: stay on the plain file.
				firlog.Log("auth: migration to OS secret store failed (%v); keeping credentials in auth.json", err)
				return result, next, nil
			}
			return nil, nil, err
		}
		return result, out, nil
	})
}

// StorageStatus describes where auth.json's secrets currently live.
type StorageStatus struct {
	Mode      string // configured auth.storage
	Location  string // "keyring" or "file"
	ProbeErr  error  // result of probing the OS secret store
	SlotCount int
}

// AuthStorageStatus reports the storage state of auth.json at authPath.
func AuthStorageStatus(authPath string) (StorageStatus, error) {
	mode, err := config.LoadAuthStorageMode(filepath.Dir(authPath))
	if err != nil {
		return StorageStatus{}, err
	}
	b := NewSecureFileBackend(authPath, defaultSecretStore, config.AuthStorageFile)
	st := StorageStatus{Mode: mode, Location: "file", ProbeErr: b.probe()}
	_, err = b.file.WithLock(func(raw []byte) (any, []byte) {
		data, derr := decodeStorageData(raw)
		if derr != nil {
			err = derr
			return nil, nil
		}
		if _, ok := data[storageMarkerKey]; ok {
			st.Location = "keyring"
			delete(data, storageMarkerKey)
		}
		st.SlotCount = len(data)
		return nil, nil
	})
	return st, err
}

// MigrateAuthStorage moves auth.json's secrets to target ("keyring" or
// "file"). Moving to the keyring copies and verifies every secret before the
// file is stripped; moving to the file writes the full plain file before the
// keyring items are deleted. Both directions are idempotent.
func MigrateAuthStorage(authPath, target string) error {
	switch target {
	case config.AuthStorageKeyring:
		b := NewSecureFileBackend(authPath, defaultSecretStore, config.AuthStorageKeyring)
		// Any locked access in keyring mode migrates a plain file.
		_, err := b.WithLock(func([]byte) (any, []byte) { return nil, nil })
		return err
	case config.AuthStorageFile:
		if mode, err := config.LoadAuthStorageMode(filepath.Dir(authPath)); err != nil {
			return err
		} else if mode != config.AuthStorageFile {
			return fmt.Errorf("auth.storage is %q, which would move secrets straight back into the OS secret store; set it to \"file\" first", mode)
		}
		b := NewSecureFileBackend(authPath, defaultSecretStore, config.AuthStorageFile)
		var stale []string
		_, err := b.file.WithLockFallible(func(raw []byte) (any, []byte, error) {
			data, migrated, err := b.load(raw)
			if err != nil || !migrated {
				return nil, nil, err
			}
			stale = sortedSlots(data)
			out, err := json.MarshalIndent(data, "", "  ")
			return nil, out, err
		})
		if err != nil {
			return err
		}
		// The plain file is on disk; only now drop the keyring copies.
		for _, slot := range stale {
			if derr := b.store.Delete(b.account(slot)); derr != nil {
				err = errors.Join(err, fmt.Errorf("delete %q from OS secret store: %w", slot, derr))
			}
		}
		return err
	}
	return fmt.Errorf("unknown auth storage target %q (want keyring or file)", target)
}
