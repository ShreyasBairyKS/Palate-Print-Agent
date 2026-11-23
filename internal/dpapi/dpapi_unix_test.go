//go:build !windows

package dpapi

import (
	"bytes"
	"testing"
)

func TestRoundTripAndEntropy(t *testing.T) {
	if _, err := machineID(); err != nil {
		t.Skip(err)
	}
	blob, err := Protect([]byte("secret credential"), []byte("a"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(blob, []byte("secret")) {
		t.Fatal("plaintext in the blob")
	}
	got, err := Unprotect(blob, []byte("a"))
	if err != nil || string(got) != "secret credential" {
		t.Fatalf("Unprotect = %q, %v", got, err)
	}
	if _, err := Unprotect(blob, []byte("b")); err == nil {
		t.Fatal("other entropy decrypted the blob")
	}
	blob[len(blob)-1] ^= 1
	if _, err := Unprotect(blob, []byte("a")); err == nil {
		t.Fatal("a damaged blob decrypted")
	}
}
