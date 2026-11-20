package status

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/internal/fileacl"
)

func TestWriteReadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	now := time.Date(2026, 11, 20, 15, 42, 0, 0, time.UTC)
	want := File{UpdatedAt: now, AgentVersion: "0.4.0-m4", Till: "FRONT-TILL",
		Server: Server{Connected: true, Since: now, LastOK: now},
		Printers: []Printer{{ID: "p-pass", Name: "Pass", Kind: "network", Where: "192.168.1.51:9100", Width: 80,
			State: "down", LastError: "paper out", Since: now, Checked: now}}}
	for range 2 { // the second write replaces the first
		if err := Write(path, want); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Till != want.Till || len(got.Printers) != 1 || got.Printers[0].LastError != "paper out" || !got.Printers[0].Since.Equal(now) {
		t.Fatalf("read back %+v", got)
	}
	if _, err := os.Stat(path + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the temporary file was left behind")
	}
}

func TestReadMissing(t *testing.T) {
	if _, err := Read(filepath.Join(t.TempDir(), FileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want ErrNotExist", err)
	}
}

func TestStale(t *testing.T) {
	now := time.Now()
	if (File{UpdatedAt: now.Add(-10 * time.Second)}).Stale(now) {
		t.Fatal("a 10 s old file is stale")
	}
	if !(File{UpdatedAt: now.Add(-time.Minute)}).Stale(now) {
		t.Fatal("a minute-old file isn't stale")
	}
}

func TestLocalUsersCanOnlyRead(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows access lists")
	}
	path := filepath.Join(t.TempDir(), FileName)
	if err := Write(path, File{Till: "T"}); err != nil {
		t.Fatal(err)
	}
	d, err := fileacl.Describe(path)
	if err != nil {
		t.Fatal(err)
	}
	// Protected, read-only for BUILTIN\Users, nothing more for them.
	if !strings.HasPrefix(d, "D:P") || !strings.Contains(d, ";FR;;;BU)") || strings.Contains(d, ";FA;;;BU)") || strings.Contains(d, ";;;WD)") {
		t.Fatalf("access list = %s", d)
	}
}
