package pair

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/internal/fileacl"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/internal/mockserver"
)

func TestFileStoreRoundTripIsEncrypted(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("DPAPI is Windows-only")
	}
	path := filepath.Join(t.TempDir(), "sub", "credential.bin")
	s := FileStore{Path: path}
	if _, err := s.Load(); !errors.Is(err, ErrNotPaired) {
		t.Fatalf("Load before Save = %v, want ErrNotPaired", err)
	}
	const cred = "8b2d8961aa55c0ffee"
	if err := s.Save(cred); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if bytes.Contains(raw, []byte(cred)) {
		t.Fatal("credential is in the file in plaintext")
	}
	got, err := s.Load()
	if err != nil || got != cred {
		t.Fatalf("Load = %q, %v; want %q", got, err, cred)
	}
	// Replace it (rotation) and make sure no temp files are left behind.
	if err := s.Save("rotated"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Load(); got != "rotated" {
		t.Fatalf("after rotation Load = %q", got)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("directory has %d entries, want only credential.bin", len(entries))
	}
}

func TestFileStoreRejectsTamperedFile(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("DPAPI is Windows-only")
	}
	path := filepath.Join(t.TempDir(), "credential.bin")
	os.WriteFile(path, []byte("not a DPAPI blob"), 0o600)
	if _, err := (FileStore{Path: path}).Load(); err == nil || errors.Is(err, ErrNotPaired) {
		t.Fatalf("Load of a tampered file = %v, want a decryption error", err)
	}
}

func TestRedeem(t *testing.T) {
	srv := mockserver.New(mockserver.Config{})
	defer srv.Close()
	ts := httptest.NewServer(srv)
	defer ts.Close()

	code, _ := srv.NewPairingCode("r1")
	store := &MemStore{}
	dev, err := Redeem(context.Background(), http.DefaultClient, ts.URL, " "+code+" ", "till-1", store)
	if err != nil {
		t.Fatal(err)
	}
	if dev == "" {
		t.Fatal("no device id")
	}
	if c, _ := store.Load(); c == "" {
		t.Fatal("credential was not saved")
	}
	// Single use.
	if _, err := Redeem(context.Background(), http.DefaultClient, ts.URL, code, "till-1", &MemStore{}); err == nil {
		t.Fatal("second redeem succeeded")
	}
}

func TestCredentialFileIsLockedDown(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("DPAPI is Windows-only")
	}
	path := filepath.Join(t.TempDir(), "credential.bin")
	if err := (FileStore{Path: path}).Save("8b2d8961aa55c0ffee"); err != nil {
		t.Fatal(err)
	}
	desc, err := fileacl.Describe(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(desc, "D:P") || strings.Contains(desc, ";;;BU)") || strings.Contains(desc, ";;;AU)") || strings.Contains(desc, ";;;WD)") {
		t.Fatalf("credential file access list %q lets more than SYSTEM, Administrators and the agent in", desc)
	}
}

func TestRedeemWrongAddressSaysSo(t *testing.T) {
	ts := httptest.NewServer(http.NotFoundHandler())
	defer ts.Close()
	store := &MemStore{}
	_, err := Redeem(context.Background(), http.DefaultClient, ts.URL, "K7Q2-M9XD", "till-1", store)
	if err == nil || !strings.Contains(err.Error(), "404") || !strings.Contains(err.Error(), "server address") {
		t.Fatalf("err = %v; want a message about the server address", err)
	}
	if c, _ := store.Load(); c != "" {
		t.Fatal("a credential was saved")
	}
}
