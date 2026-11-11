package main_test

// M2's "done when", part 1: no silent duplicates or losses when the agent
// process is killed mid-write. These tests build the real palate-agent,
// run it as a separate process, and kill it hard (TerminateProcess on
// Windows) at the worst moments.

import (
	"encoding/json"
	"net"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/internal/escpos"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/internal/fakeprinter"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/internal/mockserver"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/ledger"
)

func buildAgent(t *testing.T) string {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "palate-agent.exe")
	out, err := exec.Command("go", "build", "-o", exe, "./cmd/palate-agent").CombinedOutput()
	if err != nil {
		t.Fatalf("building palate-agent: %v\n%s", err, out)
	}
	return exe
}

type printerSpy struct {
	addr     string
	accepted chan int
	conns    chan fakeprinter.Conn
}

func spyPrinter(t *testing.T, cfg fakeprinter.Config) printerSpy {
	t.Helper()
	s := printerSpy{accepted: make(chan int, 8), conns: make(chan fakeprinter.Conn, 8)}
	cfg.OnAccept = func(seq int) { s.accepted <- seq }
	cfg.OnConn = func(c fakeprinter.Conn) { s.conns <- c }
	fp, err := fakeprinter.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go fp.Serve(ln)
	t.Cleanup(func() { fp.Close() })
	s.addr = ln.Addr().String()
	return s
}

func writeConfig(t *testing.T, dir, server, printerAddr, statusTimeout string) string {
	t.Helper()
	cfg := map[string]any{
		"server":     server,
		"agent_name": "crash-till",
		"printers":   []map[string]any{{"id": "p0", "name": "Tandoor", "kind": "network", "address": printerAddr, "width": 80}},
		"timings":    map[string]any{"status_timeout": statusTimeout},
	}
	b, _ := json.MarshalIndent(cfg, "", "  ")
	path := filepath.Join(dir, "agent.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func startAgent(t *testing.T, exe, cfg string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(exe, "run", "-config", cfg)
	cmd.Stderr = &strings.Builder{}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	return cmd
}

func kill(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	if err := cmd.Process.Kill(); err != nil { // TerminateProcess: no cleanup, no defers
		t.Fatal(err)
	}
	cmd.Wait()
}

func waitOutcome(t *testing.T, srv *mockserver.Server, id string, within time.Duration) mockserver.JobState {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		for _, j := range srv.Snapshot().Jobs {
			if j.ID == id && j.Outcome != nil {
				return j
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("job %s: no outcome within %s", id, within)
	return mockserver.JobState{}
}

func ticketText(data []byte) string {
	var lines []string
	for _, c := range escpos.Decode(data) {
		if c.Name == "text" {
			lines = append(lines, string(c.Raw))
		}
	}
	return strings.Join(lines, "\n")
}

func TestM2KillMidWrite(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the agent stores its credential and ledger with DPAPI")
	}
	if testing.Short() {
		t.Skip("builds and kills real processes")
	}
	exe := buildAgent(t)

	cases := []struct {
		name string
		// how the first printer behaves
		mode fakeprinter.Mode
		// when to kill, after the printer accepts the connection
		killAfter time.Duration
		// whether the first printer ends up with the whole ticket
		firstGetsTicket bool
	}{
		// The printer took the connection but no bytes yet (it never reads):
		// killed with the ledger at Attempting and nothing on paper.
		{"before the printer reads", fakeprinter.Stall, 300 * time.Millisecond, false},
		// Every byte reached the printer; the agent was waiting for the
		// status reply. The ticket IS on paper, but the ledger never heard.
		{"after the printer reads", fakeprinter.Normal, 400 * time.Millisecond, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			srv := mockserver.New(mockserver.Config{Hold: time.Second, Lease: time.Second})
			ts := httptest.NewServer(srv)
			t.Cleanup(func() { ts.Close(); srv.Close() })

			// Printer A never answers status queries, and the agent is told to
			// wait 30 s for one, so it is guaranteed to be mid-write when killed.
			first := spyPrinter(t, fakeprinter.Config{Mode: tc.mode, Status: fakeprinter.StatusNone})
			cfg := writeConfig(t, dir, ts.URL, first.addr, "30s")

			code, _ := srv.NewPairingCode("demo")
			if out, err := exec.Command(exe, "pair", "-config", cfg, "-code", code).CombinedOutput(); err != nil {
				t.Fatalf("pair: %v\n%s", err, out)
			}

			agent1 := startAgent(t, exe, cfg)
			id := srv.NewJob("demo", "p0", order("5001"))
			select {
			case <-first.accepted:
			case <-time.After(10 * time.Second):
				t.Fatalf("agent never connected to the printer\n%s", agent1.Stderr)
			}
			time.Sleep(tc.killAfter)
			kill(t, agent1)

			// The ledger on disk must say Attempting: the write began, the
			// outcome is unknown.
			led, err := ledger.Open(filepath.Join(dir, "ledger.bin"), ledger.Options{})
			if err != nil {
				t.Fatal(err)
			}
			e, ok := led.Get(id)
			led.Close()
			if !ok || e.State != ledger.Attempting {
				t.Fatalf("ledger after the kill = %+v, %v; want attempting", e, ok)
			}
			var conn fakeprinter.Conn
			select {
			case conn = <-first.conns:
			case <-time.After(2 * time.Second):
				// A stalled printer holds the connection until it is closed.
			}
			gotAll := strings.Contains(ticketText(conn.Data), "#5001")
			if gotAll != tc.firstGetsTicket {
				t.Fatalf("first printer got the ticket = %v, want %v", gotAll, tc.firstGetsTicket)
			}

			// Restart on a healthy printer. The lease expires, the job comes
			// back, and the ledger turns it into a marked duplicate.
			second := spyPrinter(t, fakeprinter.Config{Mode: fakeprinter.Normal, Status: fakeprinter.StatusOK})
			writeConfig(t, dir, ts.URL, second.addr, "500ms")
			agent2 := startAgent(t, exe, cfg)

			j := waitOutcome(t, srv, id, 15*time.Second)
			if j.Outcome.Kind != "printed" || !strings.Contains(j.Outcome.Reason, "crash-recovery") {
				t.Fatalf("outcome = %+v; want printed with reason crash-recovery\n%s", j.Outcome, agent2.Stderr)
			}
			var reprint fakeprinter.Conn
			select {
			case reprint = <-second.conns:
			case <-time.After(5 * time.Second):
				t.Fatal("the job never reached the second printer")
			}
			text := ticketText(reprint.Data)
			if !strings.Contains(text, "DUPLICATE") || !strings.Contains(text, "crash-recovery") || !strings.Contains(text, "#5001") {
				t.Fatalf("reprint is not marked:\n%s", text)
			}
			if j.Deliveries != 2 {
				t.Fatalf("deliveries = %d, want 2 (once to each agent run)", j.Deliveries)
			}

			// And a third run doesn't print it again: the ledger says Done.
			kill(t, agent2)
			srv.NewJob("demo", "p0", order("5002")) // something to poll for
			agent3 := startAgent(t, exe, cfg)
			select {
			case c := <-second.conns:
				if strings.Contains(ticketText(c.Data), "#5001") {
					t.Fatal("job printed a third time")
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("third run printed nothing\n%s", agent3.Stderr)
			}
		})
	}
}
