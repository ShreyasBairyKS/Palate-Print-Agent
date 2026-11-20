package queue

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/job"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/printer"
)

// checkWriter is a printer that can be checked without printing. Each Probe
// takes the next scripted answer; past the end it repeats the last one.
type checkWriter struct {
	scriptWriter
	pmu     sync.Mutex
	answers []error
	probes  int
}

func (c *checkWriter) Probe(context.Context) error {
	c.pmu.Lock()
	defer c.pmu.Unlock()
	c.probes++
	if len(c.answers) == 0 {
		return nil
	}
	a := c.answers[0]
	if len(c.answers) > 1 {
		c.answers = c.answers[1:]
	}
	return a
}

func (c *checkWriter) probeCount() int {
	c.pmu.Lock()
	defer c.pmu.Unlock()
	return c.probes
}

func (c *checkWriter) answer(errs ...error) {
	c.pmu.Lock()
	defer c.pmu.Unlock()
	c.answers = errs
}

func refusedErr() error {
	return &printer.Error{Kind: printer.Refused, Err: errors.New("connection refused")}
}

func paperErr() error {
	return &printer.Error{Kind: printer.PaperOut, Err: errors.New("printer reports paper end")}
}

func waitState(t *testing.T, q *Queue, id, want string) State {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if st := stateOf(q, id); st.State == want {
			return st
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("printer %s state = %+v, want %s", id, stateOf(q, id), want)
	return State{}
}

var fastChecks = Config{FirstCheck: 10 * time.Millisecond, IdleCheckEvery: 30 * time.Millisecond, ProbeEvery: time.Hour}

func TestIdleCheckFindsAWorkingPrinter(t *testing.T) {
	w := &checkWriter{}
	begin := time.Now()
	q := start2(t, []Printer{{ID: "p1", Name: "Tandoor", Width: job.Width80, Writer: w}}, fastChecks)
	st := waitState(t, q, "p1", "up")
	if st.Checked.Before(begin) || st.Since.Before(begin) {
		t.Fatalf("state = %+v; want Since and Checked set by the check", st)
	}
	if len(w.calls) != 0 {
		t.Fatal("a check printed something")
	}
}

func TestIdleCheckMarksASwitchedOffPrinterDown(t *testing.T) {
	primary := &checkWriter{answers: []error{refusedErr()}}
	backup := &scriptWriter{}
	rec := newRecorder()
	q := start(t, []Printer{
		{ID: "p1", Name: "Tandoor", Width: job.Width80, Writer: primary, Backup: "p2"},
		{ID: "p2", Name: "Wok", Width: job.Width80, Writer: backup},
	}, rec, fastChecks)
	st := waitState(t, q, "p1", "down")
	if st.LastError != "refused" {
		t.Fatalf("state = %+v, want down because the connection was refused", st)
	}
	if n := primary.probeCount(); n < 3 {
		t.Fatalf("marked down after %d checks; a failure must survive the retries", n)
	}
	// The next ticket goes straight to the backup, without waiting on the primary.
	q.Submit(context.Background(), testJob("j1", "p1"))
	if r := rec.next(t); r.o.Kind != job.Fallback || r.o.PrinterID != "p2" {
		t.Fatalf("outcome = %+v; want the ticket on the backup", r.o)
	}
	if primary.n() != 0 {
		t.Fatal("the down printer was written to")
	}
}

func TestIdleCheckOneRefusalIsNotDown(t *testing.T) {
	// Busy with another connection for a moment: refused once, then fine.
	w := &checkWriter{answers: []error{refusedErr(), nil}}
	q := start2(t, []Printer{{ID: "p1", Name: "Tandoor", Width: job.Width80, Writer: w}}, fastChecks)
	waitState(t, q, "p1", "up")
	time.Sleep(50 * time.Millisecond)
	if st := stateOf(q, "p1"); st.State != "up" {
		t.Fatalf("state = %+v; one refusal followed by an answer is up", st)
	}
}

func TestIdleCheckPaperOut(t *testing.T) {
	w := &checkWriter{answers: []error{paperErr()}}
	q := start2(t, []Printer{{ID: "p1", Name: "Tandoor", Width: job.Width80, Writer: w}}, fastChecks)
	st := waitState(t, q, "p1", "down")
	if st.LastError != "paper out" {
		t.Fatalf("state = %+v, want paper out", st)
	}
	if n := w.probeCount(); n != 1 {
		t.Fatalf("asked %d times; a printer that answers paper out isn't asked again", n)
	}
}

func TestDownPrinterProbeSaysWhyAndComesBack(t *testing.T) {
	w := &checkWriter{answers: []error{refusedErr()}}
	cfg := fastChecks
	cfg.ProbeEvery = 20 * time.Millisecond
	q := start2(t, []Printer{{ID: "p1", Name: "Tandoor", Width: job.Width80, Writer: w}}, cfg)
	down := waitState(t, q, "p1", "down")
	// Back on the network but out of paper: still down, and now it says so.
	w.answer(paperErr())
	deadline := time.Now().Add(2 * time.Second)
	for stateOf(q, "p1").LastError != "paper out" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	st := stateOf(q, "p1")
	if st.State != "down" || st.LastError != "paper out" || !st.Since.Equal(down.Since) {
		t.Fatalf("state = %+v; want still down since %v, now paper out", st, down.Since)
	}
	w.answer(nil)
	up := waitState(t, q, "p1", "up")
	if !up.Since.After(down.Since) {
		t.Fatal("Since didn't move when the printer came back")
	}
}

func TestPrinterThatCantBeCheckedStaysUnknown(t *testing.T) {
	w := &scriptWriter{} // like a USB printer: no Probe
	q := start2(t, []Printer{{ID: "p1", Name: "Bar", Width: job.Width80, Writer: w}}, fastChecks)
	time.Sleep(80 * time.Millisecond)
	if st := stateOf(q, "p1"); st.State != "unknown" || !st.Checked.IsZero() {
		t.Fatalf("state = %+v; a printer that can't be checked is unknown until it prints", st)
	}
	if len(w.calls) != 0 {
		t.Fatal("something was printed")
	}
}

func TestIdleChecksCanBeTurnedOff(t *testing.T) {
	w := &checkWriter{}
	cfg := fastChecks
	cfg.IdleCheckEvery = -1
	q := start2(t, []Printer{{ID: "p1", Name: "Tandoor", Width: job.Width80, Writer: w}}, cfg)
	time.Sleep(80 * time.Millisecond)
	if w.probeCount() != 0 || stateOf(q, "p1").State != "unknown" {
		t.Fatal("a printer was checked with idle checks off")
	}
}

func start2(t *testing.T, printers []Printer, cfg Config) *Queue {
	t.Helper()
	return start(t, printers, newRecorder(), cfg)
}
