package queue

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/job"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/printer"
)

// scriptWriter returns scripted results and records what it was sent.
type scriptWriter struct {
	mu      sync.Mutex
	results []error // one per call; nil = success; past the end = success
	confirm bool
	calls   [][]byte
	block   chan struct{} // if set, Write waits on it
}

func (w *scriptWriter) Write(ctx context.Context, data []byte) (printer.Result, error) {
	if w.block != nil {
		select {
		case <-w.block:
		case <-ctx.Done():
			return printer.Result{}, ctx.Err()
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls = append(w.calls, data)
	if i := len(w.calls) - 1; i < len(w.results) && w.results[i] != nil {
		return printer.Result{}, w.results[i]
	}
	return printer.Result{Confirmed: w.confirm}, nil
}

func (w *scriptWriter) n() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.calls)
}

type report struct {
	id string
	o  job.Outcome
}

type recorder struct {
	ch   chan report
	fail int // fail this many reports first
	mu   sync.Mutex
}

func newRecorder() *recorder { return &recorder{ch: make(chan report, 32)} }

func (r *recorder) Report(_ context.Context, id string, o job.Outcome) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail > 0 {
		r.fail--
		return errors.New("network down")
	}
	r.ch <- report{id, o}
	return nil
}

func (r *recorder) next(t *testing.T) report {
	t.Helper()
	select {
	case rep := <-r.ch:
		return rep
	case <-time.After(5 * time.Second):
		t.Fatal("no outcome reported")
		return report{}
	}
}

func testJob(id, printerID string) job.Job {
	return job.Job{ID: id, PrinterID: printerID, CreatedAt: time.Now(), Slip: job.Slip{
		Station: "Tandoor", OrderNumber: id, PlacedAt: time.Now(),
		Items: []job.Item{{Quantity: 1, Name: "Naan"}},
	}}
}

func start(t *testing.T, printers []Printer, rep Reporter, cfg Config) *Queue {
	t.Helper()
	if cfg.RetryGap == 0 {
		cfg.RetryGap = 10 * time.Millisecond
	}
	q := New(printers, rep, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	q.Start(ctx)
	t.Cleanup(func() { cancel(); q.Wait() })
	return q
}

func TestPrintedAndWritten(t *testing.T) {
	confirmed := &scriptWriter{confirm: true}
	silent := &scriptWriter{}
	rec := newRecorder()
	q := start(t, []Printer{
		{ID: "p1", Name: "Tandoor", Width: job.Width80, Writer: confirmed},
		{ID: "p2", Name: "Wok", Width: job.Width58, Writer: silent},
	}, rec, Config{})
	q.Submit(context.Background(), testJob("j1", "p1"))
	q.Submit(context.Background(), testJob("j2", "p2"))
	got := map[string]job.Kind{}
	for i := 0; i < 2; i++ {
		r := rec.next(t)
		got[r.id] = r.o.Kind
	}
	if got["j1"] != job.Printed || got["j2"] != job.Written {
		t.Fatalf("outcomes = %v; want j1 printed, j2 written", got)
	}
}

func TestStrictOrderPerPrinter(t *testing.T) {
	w := &scriptWriter{}
	rec := newRecorder()
	q := start(t, []Printer{{ID: "p1", Name: "A", Width: job.Width80, Writer: w}}, rec, Config{})
	for _, id := range []string{"j1", "j2", "j3", "j4"} {
		q.Submit(context.Background(), testJob(id, "p1"))
	}
	for _, want := range []string{"j1", "j2", "j3", "j4"} {
		if got := rec.next(t).id; got != want {
			t.Fatalf("reported %s, want %s next", got, want)
		}
	}
}

func TestStuckPrinterDoesNotBlockAnother(t *testing.T) {
	stuck := &scriptWriter{block: make(chan struct{})}
	ok := &scriptWriter{}
	rec := newRecorder()
	q := start(t, []Printer{
		{ID: "stuck", Name: "A", Width: job.Width80, Writer: stuck},
		{ID: "ok", Name: "B", Width: job.Width80, Writer: ok},
	}, rec, Config{})
	q.Submit(context.Background(), testJob("j1", "stuck"))
	q.Submit(context.Background(), testJob("j2", "ok"))
	if r := rec.next(t); r.id != "j2" {
		t.Fatalf("first outcome = %s, want j2 from the healthy printer", r.id)
	}
	if q.Depth() != 1 {
		t.Fatalf("depth = %d, want 1 (the stuck job)", q.Depth())
	}
	close(stuck.block)
}

func TestRefusedIsRetried(t *testing.T) {
	refused := &printer.Error{Kind: printer.Refused, Err: errors.New("connection refused")}
	w := &scriptWriter{results: []error{refused, refused}}
	rec := newRecorder()
	q := start(t, []Printer{{ID: "p1", Name: "A", Width: job.Width80, Writer: w}}, rec, Config{})
	q.Submit(context.Background(), testJob("j1", "p1"))
	if r := rec.next(t); r.o.Kind != job.Written {
		t.Fatalf("outcome = %+v; want written on the third try", r.o)
	}
	if w.n() != 3 {
		t.Fatalf("%d attempts, want 3", w.n())
	}
}

func TestRefusedThreeTimesFails(t *testing.T) {
	refused := &printer.Error{Kind: printer.Refused, Err: errors.New("connection refused")}
	w := &scriptWriter{results: []error{refused, refused, refused}}
	rec := newRecorder()
	q := start(t, []Printer{{ID: "p1", Name: "Tandoor", Width: job.Width80, Writer: w}}, rec, Config{})
	q.Submit(context.Background(), testJob("j1", "p1"))
	r := rec.next(t)
	if r.o.Kind != job.Failed || !strings.Contains(r.o.Reason, "Tandoor") || !strings.Contains(r.o.Reason, "refused") {
		t.Fatalf("outcome = %+v; want failed naming the printer and the cause", r.o)
	}
	if st := q.States()[0]; st.State != "down" || st.LastError != "refused" {
		t.Fatalf("state = %+v, want down/refused", st)
	}
}

func TestHangPartialPaperOutAreNotRetried(t *testing.T) {
	for _, k := range []printer.Kind{printer.Hang, printer.Partial, printer.PaperOut} {
		t.Run(k.String(), func(t *testing.T) {
			w := &scriptWriter{results: []error{&printer.Error{Kind: k, Written: 5, Err: errors.New("x")}}}
			rec := newRecorder()
			q := start(t, []Printer{{ID: "p1", Name: "A", Width: job.Width80, Writer: w}}, rec, Config{})
			q.Submit(context.Background(), testJob("j1", "p1"))
			if r := rec.next(t); r.o.Kind != job.Failed {
				t.Fatalf("outcome = %+v, want failed", r.o)
			}
			if w.n() != 1 {
				t.Fatalf("%d attempts; a %s is never retried on the same printer", w.n(), k)
			}
		})
	}
}

func TestUnknownPrinterFails(t *testing.T) {
	rec := newRecorder()
	q := start(t, []Printer{{ID: "p1", Name: "A", Width: job.Width80, Writer: &scriptWriter{}}}, rec, Config{})
	q.Submit(context.Background(), testJob("j1", "nope"))
	if r := rec.next(t); r.o.Kind != job.Failed || !strings.Contains(r.o.Reason, "not configured") {
		t.Fatalf("outcome = %+v", r.o)
	}
}

func TestRenderFailureIsRecorded(t *testing.T) {
	w := &scriptWriter{}
	rec := newRecorder()
	q := start(t, []Printer{{ID: "p1", Name: "A", Width: job.Width80, Writer: w}}, rec, Config{})
	j := testJob("j1", "p1")
	j.Slip.Items[0].Name = "ಮಸಾಲೆ ದೋಸೆ"
	q.Submit(context.Background(), j)
	if r := rec.next(t); r.o.Kind != job.Failed || !strings.Contains(r.o.Reason, "raster") {
		t.Fatalf("outcome = %+v; want failed, saying raster is needed", r.o)
	}
	if w.n() != 0 {
		t.Fatal("nothing should be written when rendering fails")
	}
}

func TestDelayedBanner(t *testing.T) {
	w := &scriptWriter{}
	rec := newRecorder()
	now := time.Date(2026, 11, 11, 14, 30, 0, 0, time.UTC)
	q := start(t, []Printer{{ID: "p1", Name: "A", Width: job.Width80, Writer: w}}, rec,
		Config{Now: func() time.Time { return now }, DelayedAfter: 10 * time.Minute})
	j := testJob("j1", "p1")
	j.CreatedAt = now.Add(-11 * time.Minute)
	q.Submit(context.Background(), j)
	rec.next(t)
	if !strings.Contains(string(w.calls[0]), "DELAYED") {
		t.Fatal("an 11-minute-old job printed without a DELAYED banner")
	}
}

func TestReportIsRetried(t *testing.T) {
	rec := newRecorder()
	rec.fail = 2
	q := start(t, []Printer{{ID: "p1", Name: "A", Width: job.Width80, Writer: &scriptWriter{}}}, rec, Config{})
	q.Submit(context.Background(), testJob("j1", "p1"))
	if r := rec.next(t); r.id != "j1" {
		t.Fatalf("got %s", r.id)
	}
}
