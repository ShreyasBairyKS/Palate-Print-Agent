package ledger

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/job"
)

func open(t *testing.T, path string, opt Options) *Ledger {
	t.Helper()
	if runtime.GOOS != "windows" && opt.Protect == nil {
		t.Skip("DPAPI is Windows-only")
	}
	l, err := Open(path, opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

func TestSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.bin")
	l := open(t, path, Options{})
	if err := l.Begin("j1"); err != nil {
		t.Fatal(err)
	}
	l.Begin("j2")
	l.Finish("j2", job.Outcome{Kind: job.Printed, PrinterID: "p1"})
	l.Close() // the agent "crashes" here

	l2 := open(t, path, Options{})
	e1, ok := l2.Get("j1")
	if !ok || e1.State != Attempting {
		t.Fatalf("j1 = %+v, %v; want attempting", e1, ok)
	}
	e2, _ := l2.Get("j2")
	if e2.State != Done || e2.Outcome.Kind != job.Printed || e2.Outcome.PrinterID != "p1" {
		t.Fatalf("j2 = %+v; want done/printed on p1", e2)
	}
	if _, ok := l2.Get("j3"); ok {
		t.Fatal("unknown job found")
	}
}

func TestEncryptedOnDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.bin")
	l := open(t, path, Options{})
	l.Begin("job-secret-id-123")
	raw, _ := os.ReadFile(path)
	if bytes.Contains(raw, []byte("job-secret-id-123")) {
		t.Fatal("ledger is readable in plaintext")
	}
}

func TestTornTailIsIgnored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.bin")
	l := open(t, path, Options{})
	l.Begin("j1")
	l.Finish("j1", job.Outcome{Kind: job.Written, PrinterID: "p1"})
	l.Close()

	// A crash mid-append: a length prefix promising more than was written.
	f, _ := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	f.Write([]byte{0, 0, 1, 0, 0xde, 0xad})
	f.Close()

	l2 := open(t, path, Options{})
	if e, _ := l2.Get("j1"); e.State != Done {
		t.Fatalf("j1 = %+v; the good records before the torn tail must survive", e)
	}
	// And the ledger keeps working after the damage.
	if err := l2.Begin("j2"); err != nil {
		t.Fatal(err)
	}
	l2.Close()
	l3 := open(t, path, Options{})
	if _, ok := l3.Get("j2"); !ok {
		t.Fatal("record written after recovering from a torn tail was lost")
	}
}

func TestCorruptRecordIsSkipped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.bin")
	l := open(t, path, Options{})
	l.Begin("j1")
	l.Begin("j2")
	l.Close()
	raw, _ := os.ReadFile(path)
	// Damage the last byte of the first record: the end of a DPAPI blob is
	// its signature, which Windows verifies.
	n := int(raw[0])<<24 | int(raw[1])<<16 | int(raw[2])<<8 | int(raw[3])
	raw[4+n-1] ^= 0xFF
	os.WriteFile(path, raw, 0o600)

	l2 := open(t, path, Options{})
	if l2.Len() != 1 {
		t.Fatalf("Len = %d, want 1 (the undamaged record)", l2.Len())
	}
}

func TestRetention(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.bin")
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	l := open(t, path, Options{Now: clock})
	l.Finish("old", job.Outcome{Kind: job.Written})
	l.Close()

	now = now.Add(8 * 24 * time.Hour)
	l2 := open(t, path, Options{Now: clock})
	if _, ok := l2.Get("old"); ok {
		t.Fatal("an 8-day-old entry survived the 7-day retention")
	}
}

func TestCompactsAsItGrows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.bin")
	plain := func(b []byte) ([]byte, error) { return append([]byte(nil), b...), nil }
	l := open(t, path, Options{Protect: plain, Unprotect: plain})
	for i := 0; i < 3000; i++ {
		l.Begin("same-job")
	}
	if l.records > 1100 {
		t.Fatalf("%d records for one job; the ledger should compact", l.records)
	}
	if l.Len() != 1 {
		t.Fatalf("Len = %d", l.Len())
	}
}
