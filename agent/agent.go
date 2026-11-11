// Package agent wires the pieces together and runs the poll loop: register
// printers, then fetch jobs and hand them to the per-printer queues, forever,
// with backoff and jitter on errors.
package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"slices"
	"sync"
	"time"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/api"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/config"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/job"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/ledger"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/pair"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/printer"
	netprinter "github.com/ShreyasBairyKS/Palate-Print-Agent/printer/net"
	usbprinter "github.com/ShreyasBairyKS/Palate-Print-Agent/printer/usb"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/queue"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/source/cloud"
)

// Version is reported in every poll's health payload.
const Version = "0.2.0-m2"

// Agent is a configured, runnable agent.
type Agent struct {
	Cloud  *cloud.Client
	Queue  *queue.Queue
	Ledger *ledger.Ledger
	Log    *slog.Logger

	printers []api.Printer

	mu      sync.Mutex
	lastErr string
}

// New builds an agent from a validated config and a credential store, and
// opens the ledger. Close releases it.
func New(cfg *config.Config, store pair.Store, log *slog.Logger) (*Agent, error) {
	if log == nil {
		log = slog.Default()
	}
	led, err := ledger.Open(cfg.LedgerFile, ledger.Options{})
	if err != nil {
		return nil, fmt.Errorf("opening ledger %s: %w", cfg.LedgerFile, err)
	}
	a := &Agent{Log: log, Ledger: led}
	var qps []queue.Printer
	for _, p := range cfg.Printers {
		qps = append(qps, queue.Printer{ID: p.ID, Name: p.Name, Width: job.Width(p.Width), Writer: WriterFor(p, cfg.Timings)})
		a.printers = append(a.printers, api.Printer{ID: p.ID, Name: p.Name, Kind: p.Kind, Width: p.Width})
	}
	a.Cloud = &cloud.Client{Server: cfg.Server, Store: store, Health: a.health, Log: log}
	a.Queue = queue.New(qps, a.Cloud, queue.Config{
		Ledger:       led,
		FastRetries:  cfg.Timings.FastRetries,
		RetryGap:     cfg.Timings.RetryGap.D(),
		DelayedAfter: cfg.Timings.DelayedAfter.D(),
		Log:          log,
	})
	return a, nil
}

// Close releases the ledger. Call it after Run returns.
func (a *Agent) Close() error { return a.Ledger.Close() }

// WriterFor builds the writer for one configured printer. Network and USB
// printers look the same to everything above this.
func WriterFor(p config.Printer, t config.Timings) printer.Writer {
	if p.Kind == "usb" {
		return &usbprinter.Writer{Name: p.PrinterName, StallTimeout: t.StallTimeout.D()}
	}
	return &netprinter.Writer{
		Addr:           p.Address,
		ConnectTimeout: t.ConnectTimeout.D(),
		StallTimeout:   t.StallTimeout.D(),
		StatusTimeout:  t.StatusTimeout.D(),
	}
}

// CheckUSB warns about USB printers this process can't see: the service
// runs as LocalSystem, which only sees printers installed for all users.
func CheckUSB(cfg *config.Config, log *slog.Logger) {
	var want []string
	for _, p := range cfg.Printers {
		if p.Kind == "usb" {
			want = append(want, p.PrinterName)
		}
	}
	if len(want) == 0 {
		return
	}
	seen, err := usbprinter.List()
	if err != nil {
		log.Warn("could not list Windows printers", "err", err)
		return
	}
	for _, name := range want {
		if !slices.Contains(seen, name) {
			log.Warn("USB printer not visible to this process; install it for all users", "printer_name", name, "visible", seen)
		}
	}
}

// fatal errors stop the agent: polling again can't fix them.
func fatal(err error) bool {
	return errors.Is(err, cloud.ErrRepairNeeded) || errors.Is(err, cloud.ErrUnauthorized) || errors.Is(err, pair.ErrNotPaired)
}

// Run polls until ctx is done or the credential stops working.
func (a *Agent) Run(ctx context.Context) error {
	// The queue stops when Run returns for any reason, not only when the
	// caller cancels: defers run in reverse, so cancel comes before Wait.
	ctx, cancel := context.WithCancel(ctx)
	a.Queue.Start(ctx)
	defer a.Queue.Wait()
	defer cancel()

	b := backoff{min: time.Second, max: 30 * time.Second}

	for ctx.Err() == nil {
		err := a.Cloud.RegisterPrinters(ctx, a.printers)
		if err == nil {
			a.Log.Info("printers registered", "count", len(a.printers))
			break
		}
		if fatal(err) {
			return err
		}
		a.setErr(err)
		a.Log.Warn("registering printers failed; will retry", "err", err)
		if !b.sleep(ctx) {
			return nil
		}
	}
	b.reset()

	for ctx.Err() == nil {
		jobs, err := a.Cloud.Fetch(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if fatal(err) {
				a.Log.Error("stopping: re-pair needed", "err", err)
				return err
			}
			a.setErr(err)
			a.Log.Warn("poll failed; backing off", "err", err)
			if !b.sleep(ctx) {
				return nil
			}
			continue
		}
		b.reset()
		a.setErr(nil)
		for _, j := range jobs {
			a.Log.Info("job received", "job", j.ID, "printer", j.PrinterID)
			a.Queue.Submit(ctx, j)
		}
	}
	return nil
}

func (a *Agent) setErr(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err == nil {
		a.lastErr = ""
		return
	}
	msg := err.Error()
	if len(msg) > 200 {
		msg = msg[:200]
	}
	a.lastErr = msg
}

// health is sent with every poll. It carries no customer data.
func (a *Agent) health() api.Health {
	a.mu.Lock()
	lastErr := a.lastErr
	a.mu.Unlock()
	h := api.Health{AgentVersion: Version, QueueDepth: a.Queue.Depth(), LastError: lastErr}
	for _, s := range a.Queue.States() {
		h.Printers = append(h.Printers, api.PrinterHealth{ID: s.ID, State: s.State, LastError: s.LastError})
	}
	return h
}

// backoff doubles from min to max, with ±20% jitter so a fleet of tills
// doesn't retry in step after an outage.
type backoff struct {
	min, max, cur time.Duration
}

func (b *backoff) reset() { b.cur = 0 }

func (b *backoff) sleep(ctx context.Context) bool {
	if b.cur == 0 {
		b.cur = b.min
	} else {
		b.cur = min(b.cur*2, b.max)
	}
	d := time.Duration(float64(b.cur) * (0.8 + 0.4*rand.Float64()))
	select {
	case <-time.After(d):
		return true
	case <-ctx.Done():
		return false
	}
}
