package agent

import (
	"context"
	"errors"
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
	"github.com/ShreyasBairyKS/Palate-Print-Agent/pair"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/status"
)

// flakySource fails every call while down is set, like a till whose
// internet is out; otherwise polls come back empty.
type flakySource struct{ down atomic.Bool }

func (s *flakySource) Fetch(ctx context.Context) ([]job.Job, error) {
	if s.down.Load() {
		return nil, errors.New("dial tcp: no route to host")
	}
	select {
	case <-time.After(20 * time.Millisecond):
	case <-ctx.Done():
	}
	return nil, nil
}
func (s *flakySource) Report(context.Context, string, job.Outcome) error     { return nil }
func (s *flakySource) RegisterPrinters(context.Context, []api.Printer) error { return nil }

func waitFor(t *testing.T, path, what string, ok func(status.File) bool) status.File {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var f status.File
	for time.Now().Before(deadline) {
		var err error
		if f, err = status.Read(path); err == nil && ok(f) {
			return f
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("status file never showed %s; last: %+v", what, f)
	return f
}

// The status file shows a switched-off printer as down before any ticket
// tries it, says when the internet is out, and says when the agent stops.
func TestStatusFileShowsPrintersAndConnection(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	off := ln.Addr().String()
	ln.Close() // nothing listens: a printer that's switched off

	dir := t.TempDir()
	cfg := &config.Config{
		Server:     "http://127.0.0.1:1",
		AgentName:  "FRONT-TILL",
		LedgerFile: filepath.Join(dir, "ledger.bin"),
		StatusFile: filepath.Join(dir, status.FileName),
		Printers: []config.Printer{
			{ID: "p-pass", Name: "Pass", Kind: "network", Address: off, Width: 80},
			{ID: "p-bar", Name: "Bar", Kind: "usb", PrinterName: "EPSON TM-T20", Width: 58, Dots: 360},
		},
	}
	cfg.Timings.RetryGap = config.Duration(10 * time.Millisecond)
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	a, err := New(cfg, &pair.MemStore{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	src := &flakySource{}
	src.down.Store(true)
	a.Cloud = src
	a.RetryMax = 50 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()

	f := waitFor(t, cfg.StatusFile, "the printer down and the server unreachable", func(f status.File) bool {
		return len(f.Printers) == 2 && f.Printers[0].State == "down" && !f.Server.Connected && f.Server.Error != ""
	})
	pass, bar := f.Printers[0], f.Printers[1]
	if pass.Name != "Pass" || pass.LastError != "refused" || pass.Where != off || pass.Since.IsZero() {
		t.Fatalf("Pass = %+v; want down, refused, at its address", pass)
	}
	if bar.State != "unknown" || bar.Where != "EPSON TM-T20" {
		t.Fatalf("Bar = %+v; a USB printer is unknown until it prints", bar)
	}
	if bar.Dots != 360 || f.ServerURL != cfg.Server {
		t.Fatalf("dots %d, server %q; the window shows both without reading the settings", bar.Dots, f.ServerURL)
	}
	if f.Till != "FRONT-TILL" || f.AgentVersion != Version || f.Stopped != "" {
		t.Fatalf("file = %+v", f)
	}

	src.down.Store(false)
	f = waitFor(t, cfg.StatusFile, "the server connected", func(f status.File) bool { return f.Server.Connected })
	if f.Server.Error != "" || f.Server.LastOK.IsZero() {
		t.Fatalf("server = %+v", f.Server)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if f, _ = status.Read(cfg.StatusFile); f.Stopped != "stopped" {
		t.Fatalf("after stopping, Stopped = %q", f.Stopped)
	}
}
