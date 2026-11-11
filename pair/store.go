// Package pair redeems a single-use pairing code for a device credential and
// keeps that credential on disk, encrypted with Windows DPAPI at machine
// scope (the service has no user profile).
//
// The credential is never written in plaintext, and there is no shared
// secret in the installer: each till gets its own credential when paired.
package pair

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ErrNotPaired means no credential has been stored yet.
var ErrNotPaired = errors.New("not paired: run `palate-agent pair` with a code from the portal")

// Store keeps the device credential.
type Store interface {
	Load() (string, error) // ErrNotPaired if there is none
	Save(credential string) error
}

// FileStore keeps the credential in one DPAPI-protected file.
type FileStore struct {
	Path string
}

func (s FileStore) Load() (string, error) {
	blob, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return "", ErrNotPaired
	}
	if err != nil {
		return "", err
	}
	plain, err := unprotect(blob)
	if err != nil {
		return "", fmt.Errorf("decrypting %s: %w (re-pair if this till was reimaged)", s.Path, err)
	}
	cred := strings.TrimSpace(string(plain))
	if cred == "" {
		return "", ErrNotPaired
	}
	return cred, nil
}

// Save encrypts the credential and replaces the file atomically: it writes a
// temporary file beside the target and renames it over, so a crash leaves
// either the old credential or the new one, never half of either.
func (s FileStore) Save(credential string) error {
	if strings.TrimSpace(credential) == "" {
		return errors.New("refusing to save an empty credential")
	}
	blob, err := protect([]byte(credential))
	if err != nil {
		return fmt.Errorf("encrypting credential: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.Path), ".credential-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.Write(blob); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.Path)
}

// MemStore is a Store for tests.
type MemStore struct {
	mu   sync.Mutex
	cred string
}

func (m *MemStore) Load() (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cred == "" {
		return "", ErrNotPaired
	}
	return m.cred, nil
}

func (m *MemStore) Save(c string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cred = c
	return nil
}
