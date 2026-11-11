// Package queue runs one serial queue per printer: each job is rendered for
// its printer's width, written, and its outcome reported, in strict order.
// A stuck printer never blocks another.
//
// M1 has no failure ladder: a failed write ends in a recorded failure with a
// reason. The ladder (backup printers) arrives in M3, the printed-ID ledger
// in M2. Nothing here may import net/http (arch_test.go checks).
package queue

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/job"
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

// Config holds the queue's timings. Zero values take the design plan's
// defaults.
type Config struct {
	FastRetries    int            // retries after a refused connection; default 2
	RetryGap       time.Duration  // default 1s
	DelayedAfter   time.Duration  // DELAYED banner past this age; default 10m
	ReportAttempts int            // tries to report an outcome; default 5
	Location       *time.Location // order times are printed in this zone; default time.Local
	Now            func() time.Time
	Log            *slog.Logger
}

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
	q := &Queue{cfg: cfg, reporter: reporter, workers: map[string]*worker{}}
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

// Submit queues a job on its printer. It never blocks. A job for a printer
// this till doesn't own is reported as failed straight away.
func (q *Queue) Submit(ctx context.Context, j job.Job) {
	w, ok := q.workers[j.PrinterID]
	if !ok {
		go q.report(ctx, j.ID, job.Outcome{Kind: job.Failed, Reason: fmt.Sprintf("printer %q is not configured on this till", j.PrinterID)})
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

		o := q.print(ctx, w, j)

		w.mu.Lock()
		w.busy = false
		w.mu.Unlock()
		if ctx.Err() != nil {
			// Stopping: don't report a failure caused by our own shutdown.
			// The lease expires and the job is delivered again.
			return
		}
		q.report(ctx, j.ID, o)
	}
}

// print renders and writes one job, retrying only fast failures.
func (q *Queue) print(ctx context.Context, w *worker, j job.Job) job.Outcome {
	slip := j.Slip
	slip.PlacedAt = slip.PlacedAt.In(q.cfg.Location)
	if !j.CreatedAt.IsZero() && q.cfg.Now().Sub(j.CreatedAt) > q.cfg.DelayedAfter {
		slip.Delayed = true
	}
	data, err := render.Render(slip, w.p.Width)
	if err != nil {
		q.cfg.Log.Error("render failed", "job", j.ID, "printer", w.p.ID, "err", err)
		return job.Outcome{Kind: job.Failed, Reason: err.Error()}
	}

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
		q.cfg.Log.Warn("write failed", "job", j.ID, "printer", w.p.ID, "attempt", attempt+1, "kind", kind, "err", err)
		if kind != printer.Refused {
			break // hang, partial and paper out are never retried on the same printer
		}
	}
	kind := printer.KindOf(lastErr)
	q.setState(w, "down", kind.String())
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
// outcome is logged as unreported: the lease expires and the job comes back.
// (M2's ledger keeps that from printing twice.)
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
