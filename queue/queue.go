// Package queue runs one serial queue per printer: each job is rendered for
// its printer's width, written, and its outcome reported, in strict order.
// A stuck printer never blocks another.
//
// Every write goes through the printed-ID ledger (package ledger), so a crash
// never prints a ticket twice unmarked and never loses one silently.
//
// There is no failure ladder yet: a failed write ends in a recorded failure
// with a reason. Backup printers arrive in M3. Nothing here may import
// net/http (arch_test.go checks).
package queue

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/job"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/ledger"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/printer"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/render"
)

// Printer is one printer the queue can write to.
type Printer struct {
	ID     string
	Name   string
	Width  job.Width
	Writer printer.Writer
}

// Reporter records outcomes. source.JobSource satisfies it.
type Reporter interface {
	Report(ctx context.Context, jobID string, o job.Outcome) error
}

// Ledger is the part of *ledger.Ledger the queue uses.
type Ledger interface {
	Get(id string) (ledger.Entry, bool)
	Begin(id string) error
	Finish(id string, o job.Outcome) error
}

// Config holds the queue's ledger and timings. Zero timings take the design
// plan's defaults.
type Config struct {
	Ledger         Ledger         // required
	FastRetries    int            // retries after a refused connection; default 2
	RetryGap       time.Duration  // default 1s
	DelayedAfter   time.Duration  // DELAYED banner past this age; default 10m
	ReportAttempts int            // tries to report an outcome; default 5
	Location       *time.Location // order times are printed in this zone; default time.Local
	Now            func() time.Time
	Log            *slog.Logger
}

// CrashRecovery is the reason given when a job is printed again because the
// agent died while writing it.
const CrashRecovery = "crash-recovery"

// State is a printer's health as last seen, for the poll's health payload.
type State struct {
	ID        string
	State     string // "up", "down" or "unknown"
	LastError string // an error class, never slip contents
}

// Queue owns one worker per printer.
type Queue struct {
	cfg      Config
	reporter Reporter
	workers  map[string]*worker
	wg       sync.WaitGroup

	mu      sync.Mutex
	pending map[string]bool // job ids queued or printing
}

type worker struct {
	p    Printer
	mu   sync.Mutex
	jobs []job.Job
	busy bool
	wake chan struct{}
	st   State
}

func New(printers []Printer, reporter Reporter, cfg Config) *Queue {
	if cfg.Ledger == nil {
		panic("queue: Config.Ledger is required")
	}
	if cfg.FastRetries == 0 {
		cfg.FastRetries = 2
	}
	if cfg.RetryGap == 0 {
		cfg.RetryGap = time.Second
	}
	if cfg.DelayedAfter == 0 {
		cfg.DelayedAfter = 10 * time.Minute
	}
	if cfg.ReportAttempts == 0 {
		cfg.ReportAttempts = 5
	}
	if cfg.Location == nil {
		cfg.Location = time.Local
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	q := &Queue{cfg: cfg, reporter: reporter, workers: map[string]*worker{}, pending: map[string]bool{}}
	for _, p := range printers {
		q.workers[p.ID] = &worker{p: p, wake: make(chan struct{}, 1), st: State{ID: p.ID, State: "unknown"}}
	}
	return q
}

// Start runs the workers until ctx is done. Wait returns once they stop.
func (q *Queue) Start(ctx context.Context) {
	for _, w := range q.workers {
		q.wg.Add(1)
		go func() {
			defer q.wg.Done()
			q.run(ctx, w)
		}()
	}
}

func (q *Queue) Wait() { q.wg.Wait() }

// Submit queues a job on its printer. It never blocks. A job already queued
// or printing (delivered again after its lease expired) is ignored. A job
// for a printer this till doesn't own is reported as failed straight away.
func (q *Queue) Submit(ctx context.Context, j job.Job) {
	q.mu.Lock()
	if q.pending[j.ID] {
		q.mu.Unlock()
		q.cfg.Log.Info("job already queued; ignoring the repeat", "job", j.ID)
		return
	}
	q.pending[j.ID] = true
	q.mu.Unlock()

	w, ok := q.workers[j.PrinterID]
	if !ok {
		go func() {
			q.report(ctx, j.ID, job.Outcome{Kind: job.Failed, Reason: fmt.Sprintf("printer %q is not configured on this till", j.PrinterID)})
			q.done(j.ID)
		}()
		return
	}
	w.mu.Lock()
	w.jobs = append(w.jobs, j)
	w.mu.Unlock()
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (q *Queue) done(id string) {
	q.mu.Lock()
	delete(q.pending, id)
	q.mu.Unlock()
}

// Depth is the number of jobs waiting or printing, across all printers.
func (q *Queue) Depth() int {
	n := 0
	for _, w := range q.workers {
		w.mu.Lock()
		n += len(w.jobs)
		if w.busy {
			n++
		}
		w.mu.Unlock()
	}
	return n
}

// States reports each printer's last known health.
func (q *Queue) States() []State {
	out := make([]State, 0, len(q.workers))
	for _, w := range q.workers {
		w.mu.Lock()
		out = append(out, w.st)
		w.mu.Unlock()
	}
	return out
}

func (q *Queue) run(ctx context.Context, w *worker) {
	for {
		w.mu.Lock()
		if len(w.jobs) == 0 {
			w.mu.Unlock()
			select {
			case <-w.wake:
				continue
			case <-ctx.Done():
				return
			}
		}
		j := w.jobs[0]
		w.jobs = w.jobs[1:]
		w.busy = true
		w.mu.Unlock()

		o, report := q.process(ctx, w, j)

		w.mu.Lock()
		w.busy = false
		w.mu.Unlock()
		if report {
			q.report(ctx, j.ID, o)
		}
		q.done(j.ID)
		if ctx.Err() != nil {
			return
		}
	}
}

// process takes one job through the ledger, the renderer and the printer.
// It returns the outcome and whether to report it (not when the agent is
// stopping mid-job: the lease expires and the job comes back).
func (q *Queue) process(ctx context.Context, w *worker, j job.Job) (job.Outcome, bool) {
	prior, seen := q.cfg.Ledger.Get(j.ID)
	if seen && prior.State == ledger.Done {
		// Printed (or failed) before; the report was lost. Report it again.
		q.cfg.Log.Info("job already done; reporting the stored outcome again", "job", j.ID, "outcome", prior.Outcome.Kind)
		return prior.Outcome, true
	}
	recovering := seen && prior.State == ledger.Attempting

	slip := j.Slip
	slip.PlacedAt = slip.PlacedAt.In(q.cfg.Location)
	if !j.CreatedAt.IsZero() && q.cfg.Now().Sub(j.CreatedAt) > q.cfg.DelayedAfter {
		slip.Delayed = true
	}
	if recovering {
		// The agent died while writing this job. It may or may not be on
		// paper, so it prints again, marked so the kitchen can tell.
		slip.Duplicate = true
		slip.DuplicateReason = CrashRecovery
		q.cfg.Log.Warn("job was mid-write when the agent stopped; printing again as DUPLICATE", "job", j.ID)
	}

	data, err := render.Render(slip, w.p.Width)
	if err != nil {
		q.cfg.Log.Error("render failed", "job", j.ID, "printer", w.p.ID, "err", err)
		o := job.Outcome{Kind: job.Failed, Reason: err.Error()}
		q.finish(j.ID, o)
		return o, true
	}

	// The ledger record must be on disk before the first byte goes out. If
	// the ledger can't be written, print anyway (paper keeps flowing) and say
	// so in the outcome: a crash now could print this job twice.
	var notes []string
	if recovering {
		notes = append(notes, CrashRecovery)
	}
	if err := q.cfg.Ledger.Begin(j.ID); err != nil {
		q.cfg.Log.Error("ledger write failed; printing anyway", "job", j.ID, "err", err)
		notes = append(notes, "ledger unavailable, a crash could print this twice")
	}

	o := q.write(ctx, w, j.ID, data)
	if ctx.Err() != nil {
		// Stopping mid-job: the ledger still says Attempting, so if it comes
		// back after the lease it prints as a marked duplicate.
		return o, false
	}
	if len(notes) > 0 {
		notes = append(notes, o.Reason)
		o.Reason = strings.Trim(strings.Join(notes, "; "), "; ")
	}
	q.finish(j.ID, o)
	return o, true
}

func (q *Queue) finish(id string, o job.Outcome) {
	if err := q.cfg.Ledger.Finish(id, o); err != nil {
		q.cfg.Log.Error("ledger write failed after printing", "job", id, "err", err)
	}
}

// write sends the bytes, retrying only fast failures.
func (q *Queue) write(ctx context.Context, w *worker, id string, data []byte) job.Outcome {
	var lastErr error
	for attempt := 0; attempt <= q.cfg.FastRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(q.cfg.RetryGap):
			case <-ctx.Done():
				return job.Outcome{Kind: job.Failed, Reason: "agent stopping"}
			}
		}
		res, err := w.p.Writer.Write(ctx, data)
		if err == nil {
			q.setState(w, "up", "")
			if res.Confirmed {
				return job.Outcome{Kind: job.Printed, PrinterID: w.p.ID}
			}
			return job.Outcome{Kind: job.Written, PrinterID: w.p.ID}
		}
		lastErr = err
		kind := printer.KindOf(err)
		q.cfg.Log.Warn("write failed", "job", id, "printer", w.p.ID, "attempt", attempt+1, "kind", kind, "err", err)
		if kind != printer.Refused {
			break // hang, partial and paper out are never retried on the same printer
		}
	}
	q.setState(w, "down", printer.KindOf(lastErr).String())
	return job.Outcome{Kind: job.Failed, Reason: fmt.Sprintf("printer %s (%s): %v", w.p.Name, w.p.ID, lastErr)}
}

func (q *Queue) setState(w *worker, state, lastErr string) {
	w.mu.Lock()
	w.st.State = state
	if lastErr != "" {
		w.st.LastError = lastErr
	}
	w.mu.Unlock()
}

// report sends an outcome, retrying with backoff. If every attempt fails the
// outcome is logged as unreported. The lease then expires and the job comes
// back; the ledger says Done, so its stored outcome is reported, not printed.
func (q *Queue) report(ctx context.Context, id string, o job.Outcome) {
	gap := 500 * time.Millisecond
	var err error
	for attempt := 1; attempt <= q.cfg.ReportAttempts; attempt++ {
		if err = q.reporter.Report(ctx, id, o); err == nil {
			q.cfg.Log.Info("job done", "job", id, "outcome", o.Kind, "printer", o.PrinterID, "reason", o.Reason)
			return
		}
		if errors.Is(err, context.Canceled) || attempt == q.cfg.ReportAttempts {
			break
		}
		select {
		case <-time.After(gap):
			gap *= 2
		case <-ctx.Done():
			attempt = q.cfg.ReportAttempts
		}
	}
	q.cfg.Log.Error("outcome not reported", "job", id, "outcome", o.Kind, "err", err)
}
