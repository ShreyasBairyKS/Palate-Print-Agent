//go:build windows

package dpapi

import (
	"bytes"
	"strings"
	"testing"
)

func TestCrypt32LoadsFromSystem32(t *testing.T) {
	if err := crypt32.Load(); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(strings.ToLower(crypt32.Name), `\system32\crypt32.dll`) {
		t.Fatalf("crypt32 loaded as %q; want a full System32 path", crypt32.Name)
	}
}

func TestProtectRoundTrip(t *testing.T) {
	secret := []byte("device credential")
	blob, err := Protect(secret, []byte("entropy"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(blob, secret) {
		t.Fatal("the protected blob contains the secret in the clear")
	}
	got, err := Unprotect(blob, []byte("entropy"))
	if err != nil || !bytes.Equal(got, secret) {
		t.Fatalf("round trip = %q, %v", got, err)
	}
	if _, err := Unprotect(blob, []byte("other")); err == nil {
		t.Fatal("unprotecting with the wrong entropy should fail")
	}
}
