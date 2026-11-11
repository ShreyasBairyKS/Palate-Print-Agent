package main_test

// M3's "done when": every hostile mock flag ends in a correct recorded
// outcome, and a hanging printer reaches the backup within 15 s. Plus the
// till-not-answering rule with two real agents.

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/agent"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/config"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/internal/fakeprinter"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/internal/mockserver"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/pair"
)

func jobFinal(t *testing.T, srv *mockserver.Server, id string, within time.Duration) mockserver.JobState {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		for _, j := range srv.Snapshot().Jobs {
			if j.ID == id && j.State == "done" {
				return j
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("job %s: no recorded outcome within %s", id, within)
	return mockserver.JobState{}
}

// countTickets drains a printer's finished connections for a while and counts
// those carrying the order number.
func countTickets(conns chan fakeprinter.Conn, order string, wait time.Duration) int {
	n := 0
	deadline := time.After(wait)
	for {
		select {
		case c := <-conns:
			if strings.Contains(ticketText(c.Data), "#"+order) {
				n++
			}
		case <-deadline:
			return n
		}
	}
}

func TestM3EveryHostileFlagEndsInARecordedOutcome(t *testing.T) {
	cases := []struct {
		flag       mockserver.Flag
		kind       string
		recordedBy string
		reason     string // substring
		tickets    int    // copies on paper
		slow       bool   // printer takes 3 s, longer than a 2 s lease
	}{
		{mockserver.IPInJob, "failed", "agent", "refused", 0, false},
		{mockserver.RawBytes, "failed", "agent", "refused", 0, false},
		{mockserver.HugePayload, "failed", "server", "delivered", 0, false},
		{mockserver.DropMidway, "failed", "server", "delivered", 0, false},
		{mockserver.ShortLease, "written", "agent", "", 1, true},
		{mockserver.RepeatID, "printed", "agent", "", 1, false},
	}
	for _, tc := range cases {
		t.Run(string(tc.flag), func(t *testing.T) {
			t.Parallel()
			status := fakeprinter.StatusOK
			if tc.slow {
				status = fakeprinter.StatusNone // the agent waits status_timeout for a reply
			}
			r := newRig(t, mockserver.Config{Hold: 300 * time.Millisecond, Lease: 500 * time.Millisecond, MaxDeliveries: 3},
				fakeprinter.Config{Mode: fakeprinter.Normal, Status: status})
			if tc.slow {
				r.cfg.Timings.StatusTimeout = config.Duration(3 * time.Second)
			}
			r.run(t)
			r.srv.SetHostile(tc.flag)
			id := r.srv.NewJob("demo", "p0", order("7001"))

			j := jobFinal(t, r.srv, id, 30*time.Second)
			if j.Outcome.Kind != tc.kind || j.RecordedBy != tc.recordedBy || !strings.Contains(j.Outcome.Reason, tc.reason) {
				t.Fatalf("outcome = %+v recorded by %q; want %s by %s (reason containing %q)",
					j.Outcome, j.RecordedBy, tc.kind, tc.recordedBy, tc.reason)
			}
			if got := countTickets(r.conns["p0"], "7001", 3*time.Second); got != tc.tickets {
				t.Fatalf("%d copies on paper, want %d", got, tc.tickets)
			}
		})
	}
}

// blackholed returns a private address that swallows connection attempts,
// or skips the test if this network answers them instead.
func blackholed(t *testing.T) string {
	t.Helper()
	const addr = "10.255.255.1:9100"
	start := time.Now()
	c, err := net.DialTimeout("tcp", addr, time.Second)
	if err == nil {
		c.Close()
		t.Skip(addr + " answered; can't fake a hanging printer on this network")
	}
	if time.Since(start) < 900*time.Millisecond {
		t.Skipf("%s failed fast (%v); can't fake a hanging printer on this network", addr, err)
	}
	return addr
}

func TestM3HangingPrinterReachesBackupWithin15s(t *testing.T) {
	if testing.Short() {
		t.Skip("uses the real 3 s connect timeout")
	}
	addr := blackholed(t)
	r := newRig(t, mockserver.Config{Hold: 2 * time.Second},
		fakeprinter.Config{Mode: fakeprinter.Normal, Status: fakeprinter.StatusOK}, // p0: replaced below
		fakeprinter.Config{Mode: fakeprinter.Normal, Status: fakeprinter.StatusOK}) // p1: the backup
	// p0 is a printer that has stopped answering: connections hang.
	r.cfg.Printers[0].Address = addr
	r.cfg.Printers[0].Backup = "p1"
	if err := r.cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	r.run(t)

	confirmed := time.Now()
	id := r.srv.NewJob("demo", "p0", order("8001"))
	j := jobFinal(t, r.srv, id, 20*time.Second)
	took := time.Since(confirmed)
	t.Logf("hanging printer -> backup in %v (default 3 s connect, 10 s stall timeouts)", took.Round(time.Millisecond))
	if j.Outcome.Kind != "fallback" || j.Outcome.PrinterID != "p1" || took > 15*time.Second {
		t.Fatalf("outcome %+v after %v; want fallback on p1 within 15 s", j.Outcome, took)
	}
	c := <-r.conns["p1"]
	if !strings.Contains(ticketText(c.Data), "BACKUP FOR STATION P0") {
		t.Fatalf("backup ticket has no BACKUP FOR banner:\n%s", ticketText(c.Data))
	}

	// The next ticket doesn't wait for the hang: p0 is down, so it goes
	// straight to the backup.
	confirmed = time.Now()
	id2 := r.srv.NewJob("demo", "p0", order("8002"))
	j2 := jobFinal(t, r.srv, id2, 10*time.Second)
	if took := time.Since(confirmed); j2.Outcome.Kind != "fallback" || took > time.Second {
		t.Fatalf("second ticket: %+v after %v; want fallback within 1 s", j2.Outcome, took)
	}
	h := r.srv.Snapshot().Devices[0].Health
	for _, p := range h.Printers {
		if p.ID == "p0" && p.State != "down" {
			t.Fatalf("health says p0 is %q, want down", p.State)
		}
	}
}

func TestM3SilentTillsJobsPrintOnAnotherTill(t *testing.T) {
	srv := mockserver.New(mockserver.Config{Hold: 300 * time.Millisecond, Lease: time.Second, NotAnswering: time.Second})
	ts := httptest.NewServer(srv)
	t.Cleanup(func() { ts.Close(); srv.Close() })

	type till struct {
		printer printerSpy
		cancel  context.CancelFunc
		done    chan struct{} // closed when the agent has stopped
	}
	start := func(name, printerID, printerName string) till {
		spy := spyPrinter(t, fakeprinter.Config{Mode: fakeprinter.Normal, Status: fakeprinter.StatusOK})
		cfg := &config.Config{Server: ts.URL, AgentName: name, LedgerFile: filepath.Join(t.TempDir(), "ledger.bin"),
			Printers: []config.Printer{{ID: printerID, Name: printerName, Kind: "network", Address: spy.addr, Width: 80}}}
		if err := cfg.Validate(); err != nil {
			t.Fatal(err)
		}
		store := &pair.MemStore{}
		code, _ := srv.NewPairingCode("demo")
		if _, err := pair.Redeem(context.Background(), ts.Client(), ts.URL, code, name, store); err != nil {
			t.Fatal(err)
		}
		a, err := agent.New(cfg, store, slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { a.Run(ctx); a.Close(); close(done) }()
		t.Cleanup(func() { cancel(); <-done })
		return till{printer: spy, cancel: cancel, done: done}
	}
	front := start("front-till", "p-tandoor", "Tandoor")
	back := start("back-till", "p-wok", "Wok")
	srv.SetBackups(map[string]string{"p-tandoor": "p-wok"})
	time.Sleep(500 * time.Millisecond) // both polling

	// The front till goes dark (unplugged, crashed, Windows Update...).
	front.cancel()
	<-front.done
	time.Sleep(1500 * time.Millisecond) // past NotAnswering

	id := srv.NewJob("demo", "p-tandoor", order("9001"))
	j := jobFinal(t, srv, id, 10*time.Second)
	if j.Outcome.Kind != "printed" || j.Outcome.PrinterID != "p-wok" || j.MovedFrom != "p-tandoor" {
		t.Fatalf("job = %+v; want printed on p-wok after being moved from p-tandoor", j)
	}
	var c fakeprinter.Conn
	select {
	case c = <-back.printer.conns:
	case <-time.After(5 * time.Second):
		t.Fatal("nothing reached the back till's printer")
	}
	text := ticketText(c.Data)
	if !strings.Contains(text, "MOVED FROM TANDOOR") || !strings.Contains(text, "check for duplicate") || !strings.Contains(text, "#9001") {
		t.Fatalf("moved ticket is not marked:\n%s", text)
	}
}
