package agent

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/api"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/config"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/job"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/pair"
)

// stuckSource's first Fetch hangs forever and ignores cancellation, like a
// bug deep in a network stack. Later calls return at once.
type stuckSource struct {
	mu    sync.Mutex
	calls int
}

func (s *stuckSource) Fetch(ctx context.Context) ([]job.Job, error) {
	s.mu.Lock()
	s.calls++
	n := s.calls
	s.mu.Unlock()
	if n == 1 {
		select {} // never returns, even when ctx is cancelled
	}
	time.Sleep(20 * time.Millisecond)
	return nil, nil
}

func (s *stuckSource) Report(context.Context, string, job.Outcome) error     { return nil }
func (s *stuckSource) RegisterPrinters(context.Context, []api.Printer) error { return nil }

func (s *stuckSource) n() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func TestWatchdogRestartsAStalledPollLoop(t *testing.T) {
	cfg := &config.Config{
		Server:     "http://127.0.0.1:1",
		LedgerFile: filepath.Join(t.TempDir(), "ledger.bin"),
		Printers:   []config.Printer{{ID: "p1", Name: "A", Kind: "network", Address: "127.0.0.1:9", Width: 80}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	a, err := New(cfg, &pair.MemStore{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	src := &stuckSource{}
	a.Cloud = src
	a.Watchdog = 200 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()

	deadline := time.Now().Add(5 * time.Second)
	for src.n() < 3 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if src.n() < 3 {
		t.Fatalf("Fetch called %d times; the loop never recovered from the stuck poll", src.n())
	}
	if h := a.health(); h.WatchdogRestarts != 1 {
		t.Fatalf("health reports %d watchdog restarts, want 1", h.WatchdogRestarts)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("agent did not stop")
	}
}
