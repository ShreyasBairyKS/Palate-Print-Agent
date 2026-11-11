// Package queue runs one serial queue per printer: each job is rendered for
// its printer's width, written, and its outcome reported, in strict order.
// A stuck printer never blocks another.
//
// Every write goes through the printed-ID ledger (package ledger), so a crash
// never prints a ticket twice unmarked and never loses one silently.
//
// The failure ladder, for each ticket:
//
//  1. Primary up: print there. A refused connection is retried twice.
//  2. Primary failed or already down, and it has a backup on this till: the
//     ticket is handed to the backup's own queue (most printers accept one
//     connection at a time, so only the backup's worker writes to it) and
//     prints there with a BACKUP FOR banner. Outcome: fallback, with why.
//     A partial write on the primary also marks the backup copy DUPLICATE.
//  3. No backup, or the backup fails too: a recorded failure with the reason.
//
// A printer is marked down after a hang, a partial write, paper out, or a
// refused connection that survives the retries. While it is down its tickets
// go straight to the backup, and it is probed every 30 s until it answers.
//
// Nothing here may import net/http (arch_test.go checks).
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
	Backup string // id of another printer on this till, or ""
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
	ProbeEvery     time.Duration  // how often a down printer is checked; default 30s
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
	ctx      context.Context

	mu      sync.Mutex
	pending map[string]bool // job ids queued or printing
}

// task is a job waiting in a worker's queue. handoff is set when a down
// primary passed it to this printer as its backup.
type task struct {
	j       job.Job
	handoff *handoff
}

type handoff struct {
	from    Printer
	reason  string
	partial bool // the primary may hold a fragment of this ticket
}

type worker struct {
	p       Printer
	mu      sync.Mutex
	tasks   []task
	busy    bool
	wake    chan struct{}
	st      State
	probing bool
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
	if cfg.ProbeEvery == 0 {
		cfg.ProbeEvery = 30 * time.Second
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
	for _, p := range printers {
		if p.Backup == "" {
			continue
		}
		if _, ok := q.workers[p.Backup]; !ok || p.Backup == p.ID {
			panic(fmt.Sprintf("queue: printer %q has backup %q, which is not another printer on this till", p.ID, p.Backup))
		}
	}
	return q
}

// Start runs the workers until ctx is done. Wait returns once they stop.
func (q *Queue) Start(ctx context.Context) {
	q.ctx = ctx
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
	w.push(task{j: j})
}

func (w *worker) push(t task) {
	w.mu.Lock()
	w.tasks = append(w.tasks, t)
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
		n += len(w.tasks)
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
		if len(w.tasks) == 0 {
			w.mu.Unlock()
			select {
			case <-w.wake:
				continue
			case <-ctx.Done():
				return
			}
		}
		t := w.tasks[0]
		w.tasks = w.tasks[1:]
		w.busy = true
		w.mu.Unlock()

		o, next := q.process(ctx, w, t)

		w.mu.Lock()
		w.busy = false
		w.mu.Unlock()
		switch next {
		case stepReport:
			q.report(ctx, t.j.ID, o)
			q.done(t.j.ID)
		case stepStopping:
			q.done(t.j.ID)
		case stepHandedOff:
			// The backup's worker reports it.
		}
		if ctx.Err() != nil {
			return
		}
	}
}

type nextStep int

const (
	stepReport    nextStep = iota // report the outcome
	stepStopping                  // agent stopping mid-job: don't report; the lease brings it back
	stepHandedOff                 // passed to the backup printer's queue
)

// process takes one task through the ledger, the renderer and the printer.
func (q *Queue) process(ctx context.Context, w *worker, t task) (job.Outcome, nextStep) {
	j, h := t.j, t.handoff
	var notes []string

	recovering := false
	if h == nil {
		prior, seen := q.cfg.Ledger.Get(j.ID)
		if seen && prior.State == ledger.Done {
			// Printed (or failed) before; the report was lost. Report it again.
			q.cfg.Log.Info("job already done; reporting the stored outcome again", "job", j.ID, "outcome", prior.Outcome.Kind)
			return prior.Outcome, stepReport
		}
		recovering = seen && prior.State == ledger.Attempting

		// A down printer with a backup sends its tickets straight there.
		if w.down() && w.p.Backup != "" {
			q.handOff(w, t, "printer marked down ("+w.lastError()+")", false)
			return job.Outcome{}, stepHandedOff
		}
	}

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
		notes = append(notes, CrashRecovery)
		q.cfg.Log.Warn("job was mid-write when the agent stopped; printing again as DUPLICATE", "job", j.ID)
	}
	if h != nil {
		slip.FallbackFrom = h.from.Name
		if h.partial {
			slip.Duplicate = true
			slip.DuplicateReason = "partial ticket on " + strings.ToUpper(h.from.Name)
		}
	}

	data, err := render.Render(slip, w.p.Width)
	if err != nil {
		q.cfg.Log.Error("render failed", "job", j.ID, "printer", w.p.ID, "err", err)
		o := job.Outcome{Kind: job.Failed, Reason: err.Error()}
		q.finish(j.ID, o)
		return o, stepReport
	}

	// The ledger record must be on disk before the first byte goes out. If
	// the ledger can't be written, print anyway (paper keeps flowing) and say
	// so in the outcome: a crash now could print this job twice.
	if err := q.cfg.Ledger.Begin(j.ID); err != nil {
		q.cfg.Log.Error("ledger write failed; printing anyway", "job", j.ID, "err", err)
		notes = append(notes, "ledger unavailable, a crash could print this twice")
	}

	o, werr := q.write(ctx, w, j.ID, data)
	if ctx.Err() != nil {
		// Stopping mid-job: the ledger still says Attempting, so if it comes
		// back after the lease it prints as a marked duplicate.
		return o, stepStopping
	}

	switch {
	case werr != nil && h == nil && w.p.Backup != "":
		// Next rung of the ladder. The ledger stays at Attempting until the
		// backup finishes, which is right: if the agent dies now, the job
		// comes back as a marked duplicate.
		q.handOff(w, t, fmt.Sprintf("%v", werr), printer.KindOf(werr) == printer.Partial)
		return job.Outcome{}, stepHandedOff
	case h != nil && werr == nil:
		o = job.Outcome{Kind: job.Fallback, PrinterID: w.p.ID,
			Reason: fmt.Sprintf("printed on backup %s (%s): primary %s (%s): %s", w.p.Name, w.p.ID, h.from.Name, h.from.ID, h.reason)}
	case h != nil:
		o = job.Outcome{Kind: job.Failed,
			Reason: fmt.Sprintf("primary %s (%s): %s; backup %s (%s): %v", h.from.Name, h.from.ID, h.reason, w.p.Name, w.p.ID, werr)}
	}
	if len(notes) > 0 {
		o.Reason = strings.Trim(strings.Join(append(notes, o.Reason), "; "), "; ")
	}
	q.finish(j.ID, o)
	return o, stepReport
}

func (q *Queue) handOff(w *worker, t task, reason string, partial bool) {
	b := q.workers[w.p.Backup]
	q.cfg.Log.Warn("printer down; sending ticket to its backup", "job", t.j.ID, "printer", w.p.ID, "backup", b.p.ID, "reason", reason)
	b.push(task{j: t.j, handoff: &handoff{from: w.p, reason: reason, partial: partial}})
}

func (q *Queue) finish(id string, o job.Outcome) {
	if err := q.cfg.Ledger.Finish(id, o); err != nil {
		q.cfg.Log.Error("ledger write failed after printing", "job", id, "err", err)
	}
}

// write sends the bytes, retrying only fast failures. On failure it marks the
// printer down and returns the outcome and the error.
func (q *Queue) write(ctx context.Context, w *worker, id string, data []byte) (job.Outcome, error) {
	var lastErr error
	for attempt := 0; attempt <= q.cfg.FastRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(q.cfg.RetryGap):
			case <-ctx.Done():
				return job.Outcome{Kind: job.Failed, Reason: "agent stopping"}, ctx.Err()
			}
		}
		res, err := w.p.Writer.Write(ctx, data)
		if err == nil {
			q.setState(w, "up", "")
			if res.Confirmed {
				return job.Outcome{Kind: job.Printed, PrinterID: w.p.ID}, nil
			}
			return job.Outcome{Kind: job.Written, PrinterID: w.p.ID}, nil
		}
		lastErr = err
		kind := printer.KindOf(err)
		q.cfg.Log.Warn("write failed", "job", id, "printer", w.p.ID, "attempt", attempt+1, "kind", kind, "err", err)
		if kind != printer.Refused {
			break // hang, partial and paper out are never retried on the same printer
		}
	}
	q.markDown(w, printer.KindOf(lastErr).String())
	return job.Outcome{Kind: job.Failed, Reason: fmt.Sprintf("printer %s (%s): %v", w.p.Name, w.p.ID, lastErr)}, lastErr
}

func (w *worker) down() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.st.State == "down"
}

func (w *worker) lastError() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.st.LastError
}

func (q *Queue) setState(w *worker, state, lastErr string) {
	w.mu.Lock()
	w.st.State = state
	if lastErr != "" {
		w.st.LastError = lastErr
	}
	w.mu.Unlock()
}

// markDown records the printer as down and starts probing it.
func (q *Queue) markDown(w *worker, lastErr string) {
	w.mu.Lock()
	w.st.State = "down"
	w.st.LastError = lastErr
	start := !w.probing && q.ctx != nil
	if start {
		w.probing = true
	}
	w.mu.Unlock()
	if start {
		go q.probe(w)
	}
}

// probe checks a down printer every ProbeEvery until it answers. A printer
// that can't be probed (USB) goes back to "unknown" after one interval, so
// the next ticket tries it again.
func (q *Queue) probe(w *worker) {
	defer func() {
		w.mu.Lock()
		w.probing = false
		w.mu.Unlock()
	}()
	t := time.NewTicker(q.cfg.ProbeEvery)
	defer t.Stop()
	for {
		select {
		case <-q.ctx.Done():
			return
		case <-t.C:
		}
		if !w.down() {
			return // a write succeeded meanwhile
		}
		p, ok := w.p.Writer.(printer.Prober)
		if !ok {
			q.setState(w, "unknown", "")
			return
		}
		if err := p.Probe(q.ctx); err == nil {
			q.cfg.Log.Info("printer answering again", "printer", w.p.ID)
			q.setState(w, "up", "")
			return
		}
	}
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
