package queue

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/job"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/ledger"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/printer"
)

func hangErr() error {
	return &printer.Error{Kind: printer.Hang, Err: errors.New("connect: no answer in 3s")}
}

func stateOf(q *Queue, id string) State {
	for _, s := range q.States() {
		if s.ID == id {
			return s
		}
	}
	return State{}
}

func TestFallbackToBackup(t *testing.T) {
	primary := &scriptWriter{results: []error{hangErr()}}
	backup := &scriptWriter{confirm: true}
	rec := newRecorder()
	q := start(t, []Printer{
		{ID: "p1", Name: "Tandoor", Width: job.Width80, Writer: primary, Backup: "p2"},
		{ID: "p2", Name: "Wok", Width: job.Width58, Writer: backup},
	}, rec, Config{})
	q.Submit(context.Background(), testJob("j1", "p1"))
	r := rec.next(t)
	if r.o.Kind != job.Fallback || r.o.PrinterID != "p2" || !strings.Contains(r.o.Reason, "Tandoor") || !strings.Contains(r.o.Reason, "hang") {
		t.Fatalf("outcome = %+v; want fallback on p2 saying why", r.o)
	}
	if primary.n() != 1 {
		t.Fatalf("primary tried %d times; a hang is never retried", primary.n())
	}
	ticket := string(backup.calls[0])
	if !strings.Contains(ticket, "BACKUP FOR TANDOOR") || strings.Contains(ticket, "DUPLICATE") {
		t.Fatal("backup ticket should say BACKUP FOR TANDOOR and not DUPLICATE")
	}
	// Rendered for the backup's paper, not the primary's.
	if !strings.Contains(ticket, strings.Repeat("-", 32)+"\n") || strings.Contains(ticket, strings.Repeat("-", 48)) {
		t.Fatal("the backup copy was not rendered for 58 mm paper")
	}
	if st := stateOf(q, "p1"); st.State != "down" || st.LastError != "hang" {
		t.Fatalf("primary state = %+v, want down/hang", st)
	}
}

func TestPartialOnPrimaryMarksBackupCopyDuplicate(t *testing.T) {
	primary := &scriptWriter{results: []error{&printer.Error{Kind: printer.Partial, Written: 40, Err: errors.New("reset")}}}
	backup := &scriptWriter{}
	rec := newRecorder()
	q := start(t, []Printer{
		{ID: "p1", Name: "Tandoor", Width: job.Width80, Writer: primary, Backup: "p2"},
		{ID: "p2", Name: "Wok", Width: job.Width80, Writer: backup},
	}, rec, Config{})
	q.Submit(context.Background(), testJob("j1", "p1"))
	if r := rec.next(t); r.o.Kind != job.Fallback {
		t.Fatalf("outcome = %+v", r.o)
	}
	ticket := string(backup.calls[0])
	if !strings.Contains(ticket, "DUPLICATE") || !strings.Contains(ticket, "partial ticket on TANDOOR") {
		t.Fatal("after a partial write the backup copy must be marked DUPLICATE")
	}
}

func TestDownPrinterSendsStraightToBackup(t *testing.T) {
	primary := &scriptWriter{results: []error{hangErr()}}
	backup := &scriptWriter{}
	rec := newRecorder()
	q := start(t, []Printer{
		{ID: "p1", Name: "Tandoor", Width: job.Width80, Writer: primary, Backup: "p2"},
		{ID: "p2", Name: "Wok", Width: job.Width80, Writer: backup},
	}, rec, Config{ProbeEvery: time.Hour})
	q.Submit(context.Background(), testJob("j1", "p1"))
	rec.next(t)
	q.Submit(context.Background(), testJob("j2", "p1"))
	r := rec.next(t)
	if r.o.Kind != job.Fallback || !strings.Contains(r.o.Reason, "marked down") {
		t.Fatalf("outcome = %+v; want fallback because the primary is down", r.o)
	}
	if primary.n() != 1 {
		t.Fatalf("primary was tried %d times; a down printer's tickets go straight to the backup", primary.n())
	}
}

func TestBackupFailsToo(t *testing.T) {
	primary := &scriptWriter{results: []error{hangErr()}}
	backup := &scriptWriter{results: []error{&printer.Error{Kind: printer.PaperOut, Err: errors.New("paper end")}}}
	rec := newRecorder()
	q := start(t, []Printer{
		{ID: "p1", Name: "Tandoor", Width: job.Width80, Writer: primary, Backup: "p2"},
		{ID: "p2", Name: "Wok", Width: job.Width80, Writer: backup, Backup: "p1"},
	}, rec, Config{})
	q.Submit(context.Background(), testJob("j1", "p1"))
	r := rec.next(t)
	if r.o.Kind != job.Failed || !strings.Contains(r.o.Reason, "Tandoor") || !strings.Contains(r.o.Reason, "Wok") {
		t.Fatalf("outcome = %+v; want failed naming both printers", r.o)
	}
	if primary.n() != 1 {
		t.Fatal("the ladder went back to the primary; it has only one rung")
	}
}

func TestNoBackupIsRecordedFailure(t *testing.T) {
	w := &scriptWriter{results: []error{hangErr()}}
	rec := newRecorder()
	q := start(t, []Printer{{ID: "p1", Name: "Tandoor", Width: job.Width80, Writer: w}}, rec, Config{})
	q.Submit(context.Background(), testJob("j1", "p1"))
	if r := rec.next(t); r.o.Kind != job.Failed {
		t.Fatalf("outcome = %+v, want failed", r.o)
	}
	// Down with no backup: the next ticket still tries it (it may be back).
	q.Submit(context.Background(), testJob("j2", "p1"))
	if r := rec.next(t); r.o.Kind != job.Written {
		t.Fatalf("outcome = %+v; a down printer with no backup is still tried", r.o)
	}
}

type probeWriter struct {
	scriptWriter
	pmu sync.Mutex
	up  bool
}

func (p *probeWriter) Probe(context.Context) error {
	p.pmu.Lock()
	defer p.pmu.Unlock()
	if !p.up {
		return errors.New("still down")
	}
	return nil
}

func TestProbeBringsPrinterBack(t *testing.T) {
	primary := &probeWriter{scriptWriter: scriptWriter{results: []error{hangErr()}}}
	backup := &scriptWriter{}
	rec := newRecorder()
	q := start(t, []Printer{
		{ID: "p1", Name: "Tandoor", Width: job.Width80, Writer: primary, Backup: "p2"},
		{ID: "p2", Name: "Wok", Width: job.Width80, Writer: backup},
	}, rec, Config{ProbeEvery: 20 * time.Millisecond})
	q.Submit(context.Background(), testJob("j1", "p1"))
	rec.next(t)
	time.Sleep(100 * time.Millisecond)
	if stateOf(q, "p1").State != "down" {
		t.Fatal("printer came back while its probe was failing")
	}
	primary.pmu.Lock()
	primary.up = true
	primary.pmu.Unlock()
	deadline := time.Now().Add(2 * time.Second)
	for stateOf(q, "p1").State != "up" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if stateOf(q, "p1").State != "up" {
		t.Fatal("printer never came back after its probe succeeded")
	}
	q.Submit(context.Background(), testJob("j2", "p1"))
	if r := rec.next(t); r.o.Kind != job.Written || r.o.PrinterID != "p1" {
		t.Fatalf("outcome = %+v; want it back on the primary", r.o)
	}
}

type stallWriter struct{ after time.Duration }

func (s *stallWriter) Write(ctx context.Context, _ []byte) (printer.Result, error) {
	select {
	case <-time.After(s.after):
		return printer.Result{}, &printer.Error{Kind: printer.Hang, Err: errors.New("write made no progress in 10s")}
	case <-ctx.Done():
		return printer.Result{}, ctx.Err()
	}
}

// M3's "done when", queue level: the worst case, a write that stalls for
// the full 10 s before the printer counts as hung.
func TestHangingPrinterReachesBackupWithin15s(t *testing.T) {
	if testing.Short() {
		t.Skip("uses the real 10 s stall timeout")
	}
	backup := &scriptWriter{}
	rec := newRecorder()
	q := start(t, []Printer{
		{ID: "p1", Name: "Tandoor", Width: job.Width80, Writer: &stallWriter{after: 10 * time.Second}, Backup: "p2"},
		{ID: "p2", Name: "Wok", Width: job.Width80, Writer: backup},
	}, rec, Config{})
	begin := time.Now()
	q.Submit(context.Background(), testJob("j1", "p1"))
	r := rec.next20s(t)
	took := time.Since(begin)
	t.Logf("hanging printer -> backup in %v", took.Round(time.Millisecond))
	if r.o.Kind != job.Fallback || took > 15*time.Second {
		t.Fatalf("outcome %+v after %v; want fallback within 15 s", r.o, took)
	}
}

func (r *recorder) next20s(t *testing.T) report {
	t.Helper()
	select {
	case rep := <-r.ch:
		return rep
	case <-time.After(20 * time.Second):
		t.Fatal("no outcome reported within 20 s")
		return report{}
	}
}

func TestCrashDuringFallbackLeavesAttempting(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "ledger.bin"))
	primary := &scriptWriter{results: []error{hangErr()}}
	backup := &scriptWriter{block: make(chan struct{})}
	q := New([]Printer{
		{ID: "p1", Name: "Tandoor", Width: job.Width80, Writer: primary, Backup: "p2"},
		{ID: "p2", Name: "Wok", Width: job.Width80, Writer: backup},
	}, newRecorder(), Config{Ledger: l, RetryGap: 10 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	q.Start(ctx)
	q.Submit(ctx, testJob("j1", "p1"))
	time.Sleep(100 * time.Millisecond) // primary hung; backup is mid-write
	cancel()                           // the agent dies
	q.Wait()
	if e, _ := l.Get("j1"); e.State != ledger.Attempting {
		t.Fatalf("ledger = %+v; want attempting after dying mid-fallback", e)
	}
}

func TestBadBackupConfigPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("a backup that is not on this till was accepted")
		}
	}()
	New([]Printer{{ID: "p1", Name: "A", Width: job.Width80, Writer: &scriptWriter{}, Backup: "elsewhere"}},
		newRecorder(), Config{Ledger: openLedger(t, "")})
}
