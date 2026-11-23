//go:build !windows

// Package dpapi protects the agent's secrets on disk so they can be read
// back on this computer only. On Windows it is DPAPI at machine scope; here
// it does the same job: AES-256-GCM with a key derived from this computer's
// machine id (/etc/machine-id on Linux, the hardware UUID on macOS) and the
// caller's entropy.
//
// As with DPAPI at machine scope, this stops the files being used on another
// computer (a copied disk or backup) but not by someone with administrator
// rights on this one: the files themselves are readable by root and the
// service only (see internal/fileacl).
package dpapi

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
)

var (
	magic         = []byte("PPA1")
	errEmptyInput = errors.New("dpapi: empty input")
)

// Protect encrypts data for this computer.
func Protect(data, entropy []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, errEmptyInput
	}
	aead, err := sealer(entropy)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := append(append([]byte{}, magic...), nonce...)
	return aead.Seal(out, nonce, data, magic), nil
}

// Unprotect decrypts what Protect made on this computer with the same
// entropy.
func Unprotect(blob, entropy []byte) ([]byte, error) {
	if len(blob) == 0 {
		return nil, errEmptyInput
	}
	if !bytes.HasPrefix(blob, magic) {
		return nil, errors.New("dpapi: not a protected file (or made on Windows)")
	}
	aead, err := sealer(entropy)
	if err != nil {
		return nil, err
	}
	rest := blob[len(magic):]
	if len(rest) < aead.NonceSize() {
		return nil, errors.New("dpapi: file is cut short")
	}
	plain, err := aead.Open(nil, rest[:aead.NonceSize()], rest[aead.NonceSize():], magic)
	if err != nil {
		return nil, errors.New("dpapi: can't decrypt: made on another computer, or damaged")
	}
	return plain, nil
}

func sealer(entropy []byte) (cipher.AEAD, error) {
	id, err := machineID()
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	h.Write([]byte("palate-print-agent machine key v1\x00"))
	h.Write([]byte(id))
	h.Write([]byte{0})
	h.Write(entropy)
	block, err := aes.NewCipher(h.Sum(nil))
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

var reUUID = regexp.MustCompile(`"IOPlatformUUID" = "([0-9A-Fa-f-]+)"`)

// machineID is this computer's stable id.
func machineID() (string, error) {
	if runtime.GOOS == "darwin" {
		out, err := exec.Command("/usr/sbin/ioreg", "-rd1", "-c", "IOPlatformExpertDevice").Output()
		if err != nil {
			return "", fmt.Errorf("dpapi: reading this Mac's hardware id: %w", err)
		}
		if m := reUUID.FindSubmatch(out); m != nil {
			return string(m[1]), nil
		}
		return "", errors.New("dpapi: this Mac's hardware id wasn't found")
	}
	for _, p := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id", "/etc/hostid"} {
		if b, err := os.ReadFile(p); err == nil {
			if id := strings.TrimSpace(string(b)); id != "" {
				return id, nil
			}
		}
	}
	return "", errors.New("dpapi: this computer has no machine id (/etc/machine-id); create one with: sudo systemd-machine-id-setup")
}
