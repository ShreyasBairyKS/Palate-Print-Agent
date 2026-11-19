package agent

import (
	"context"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/api"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/config"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/job"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/ledger"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/pair"
)

// oneJobSource hands out one job, then holds every later poll until ctx ends.
type oneJobSource struct{ sent atomic.Bool }

func (s *oneJobSource) Fetch(ctx context.Context) ([]job.Job, error) {
	if s.sent.CompareAndSwap(false, true) {
		return []job.Job{{ID: "j1", PrinterID: "p1", CreatedAt: time.Now(), Slip: job.Slip{
			Station: "Tandoor", OrderNumber: "1", PlacedAt: time.Now(), Items: []job.Item{{Quantity: 1, Name: "Naan"}}}}}, nil
	}
	<-ctx.Done()
	return nil, ctx.Err()
}
func (s *oneJobSource) Report(context.Context, string, job.Outcome) error     { return nil }
func (s *oneJobSource) RegisterPrinters(context.Context, []api.Printer) error { return nil }

// A stop (service stop, Windows Update) while a ticket is printing lets it
// finish instead of cutting it off.
func TestStopLetsTheTicketInHandFinish(t *testing.T) {
	// A printer that takes the bytes but never answers the status query, so
	// each print takes the whole status timeout (1.5 s here).
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan struct{}, 1)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted <- struct{}{}
			go io.Copy(io.Discard, c)
		}
	}()

	cfg := &config.Config{
		Server:     "http://127.0.0.1:1",
		LedgerFile: filepath.Join(t.TempDir(), "ledger.bin"),
		Printers:   []config.Printer{{ID: "p1", Name: "Tandoor", Kind: "network", Address: ln.Addr().String(), Width: 80}},
	}
	cfg.Timings.StatusTimeout = config.Duration(1500 * time.Millisecond)
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	a, err := New(cfg, &pair.MemStore{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	a.Cloud = &oneJobSource{}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()

	select {
	case <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("the ticket never reached the printer")
	}
	time.Sleep(200 * time.Millisecond) // mid-print
	stopAt := time.Now()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("agent did not stop within 5 s")
	}
	t.Logf("stopped %v after the stop was asked for", time.Since(stopAt).Round(10*time.Millisecond))
	e, ok := a.Ledger.Get("j1")
	if !ok || e.State != ledger.Done || e.Outcome.Kind != job.Written {
		t.Fatalf("ledger = %+v (found %v); the ticket in hand should have finished as written", e, ok)
	}
}
