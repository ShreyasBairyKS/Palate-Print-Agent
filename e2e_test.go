package main_test

// End-to-end tests: the real agent against the mock server and fake printers.
// TestM1MockOrderPrintsWithinOneSecond is M1's "done when".

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/agent"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/api"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/config"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/internal/escpos"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/internal/fakeprinter"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/internal/mockserver"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/pair"
)

type rig struct {
	srv   *mockserver.Server
	conns map[string]chan fakeprinter.Conn
	cfg   *config.Config
	store *pair.MemStore
}

// newRig starts a mock server and one fake printer per mode, pairs a store,
// and writes a config naming the printers p0, p1, ...
func newRig(t *testing.T, scfg mockserver.Config, modes ...fakeprinter.Config) *rig {
	t.Helper()
	srv := mockserver.New(scfg)
	ts := httptest.NewServer(srv)
	t.Cleanup(func() { ts.Close(); srv.Close() })

	r := &rig{srv: srv, conns: map[string]chan fakeprinter.Conn{}, store: &pair.MemStore{}}
	cfg := &config.Config{Server: ts.URL, AgentName: "e2e-till", LedgerFile: filepath.Join(t.TempDir(), "ledger.bin")}
	for i, m := range modes {
		id := "p" + string(rune('0'+i))
		ch := make(chan fakeprinter.Conn, 16)
		r.conns[id] = ch
		m.OnConn = func(c fakeprinter.Conn) { ch <- c }
		fp, err := fakeprinter.New(m)
		if err != nil {
			t.Fatal(err)
		}
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		go fp.Serve(ln)
		t.Cleanup(func() { fp.Close() })
		cfg.Printers = append(cfg.Printers, config.Printer{
			ID: id, Name: "Station " + id, Kind: "network", Address: ln.Addr().String(), Width: 80,
		})
	}
	cfg.Timings.RetryGap = config.Duration(50 * time.Millisecond)
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	r.cfg = cfg

	code, _ := srv.NewPairingCode("demo")
	if _, err := pair.Redeem(context.Background(), http.DefaultClient, ts.URL, code, "e2e-till", r.store); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *rig) agent(t *testing.T) *agent.Agent {
	t.Helper()
	a, err := agent.New(r.cfg, r.store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	return a
}

// run starts the agent and waits until its printers are registered.
func (r *rig) run(t *testing.T) {
	t.Helper()
	a := r.agent(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("agent did not stop within 5 s of cancel")
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		st := r.srv.Snapshot()
		if len(st.Devices) == 1 && len(st.Devices[0].Printers) == len(r.cfg.Printers) && !st.Devices[0].LastPoll.IsZero() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("agent never registered its printers and polled")
}

func (r *rig) outcome(t *testing.T, id string) *api.OutcomeRequest {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, j := range r.srv.Snapshot().Jobs {
			if j.ID == id && j.Outcome != nil {
				return j.Outcome
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job %s: no outcome reported", id)
	return nil
}

func order(number string) api.Slip {
	return api.Slip{Station: "Tandoor", Table: "T4", OrderNumber: number, PlacedAt: time.Now(),
		Items: []api.Item{{Quantity: 2, Name: "Paneer Tikka", Notes: "less spicy"}, {Quantity: 1, Name: "Butter Naan"}}}
}

func TestM1MockOrderPrintsWithinOneSecond(t *testing.T) {
	r := newRig(t, mockserver.Config{Hold: 25 * time.Second}, fakeprinter.Config{Mode: fakeprinter.Normal, Status: fakeprinter.StatusOK})
	r.run(t)
	time.Sleep(200 * time.Millisecond) // let the agent settle into a held long poll

	confirmed := time.Now()
	id := r.srv.NewJob("demo", "p0", order("1042"))

	var conn fakeprinter.Conn
	select {
	case conn = <-r.conns["p0"]:
	case <-time.After(3 * time.Second):
		t.Fatal("nothing reached the printer")
	}
	latency := conn.Started.Sub(confirmed)
	t.Logf("order confirmed -> printer connection in %v (ticket fully written by %v)", latency.Round(time.Millisecond), conn.Ended.Sub(confirmed).Round(time.Millisecond))
	if conn.Ended.Sub(confirmed) > time.Second {
		t.Fatalf("ticket took %v to print; M1 needs it within 1 s", conn.Ended.Sub(confirmed))
	}

	var text []string
	for _, c := range escpos.Decode(conn.Data) {
		if c.Name == "text" {
			text = append(text, string(c.Raw))
		}
	}
	all := strings.Join(text, "\n")
	for _, want := range []string{"TANDOOR", "Table T4", "#1042", "2 x Paneer Tikka", "- less spicy", "1 x Butter Naan"} {
		if !strings.Contains(all, want) {
			t.Errorf("ticket is missing %q:\n%s", want, all)
		}
	}
	if o := r.outcome(t, id); o.Kind != "printed" || o.PrinterID != "p0" {
		t.Fatalf("outcome = %+v; want printed on p0", o)
	}
}

func TestTwoStationsPrintOnTheirOwnPrinters(t *testing.T) {
	r := newRig(t, mockserver.Config{Hold: 2 * time.Second},
		fakeprinter.Config{Mode: fakeprinter.Normal, Status: fakeprinter.StatusNone},
		fakeprinter.Config{Mode: fakeprinter.Normal, Status: fakeprinter.StatusOK})
	r.run(t)
	a := r.srv.NewJob("demo", "p0", order("2001"))
	b := r.srv.NewJob("demo", "p1", order("2001"))
	if o := r.outcome(t, a); o.Kind != "written" {
		t.Errorf("p0 (never answers status) outcome = %+v, want written", o)
	}
	if o := r.outcome(t, b); o.Kind != "printed" {
		t.Errorf("p1 (confirms) outcome = %+v, want printed", o)
	}
}

func TestDeadPrinterEndsInRecordedFailure(t *testing.T) {
	r := newRig(t, mockserver.Config{Hold: 2 * time.Second}, fakeprinter.Config{Mode: fakeprinter.Normal})
	// Point p0 at a port nothing listens on.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	r.cfg.Printers[0].Address = ln.Addr().String()
	ln.Close()
	r.run(t)
	id := r.srv.NewJob("demo", "p0", order("3001"))
	o := r.outcome(t, id)
	if o.Kind != "failed" || !strings.Contains(o.Reason, "refused") {
		t.Fatalf("outcome = %+v; want failed with the reason", o)
	}
	h := r.srv.Snapshot().Devices[0].Health
	if len(h.Printers) != 1 || h.Printers[0].State != "down" {
		// Health is sent on the next poll; give it one.
		r.srv.NewJob("demo", "p0", order("3002"))
		r.outcome(t, "job-000002")
		h = r.srv.Snapshot().Devices[0].Health
	}
	if h.Printers[0].State != "down" {
		t.Fatalf("health = %+v; want p0 reported down", h)
	}
}

func TestHostileJobNeverPrints(t *testing.T) {
	r := newRig(t, mockserver.Config{Hold: 2 * time.Second}, fakeprinter.Config{Mode: fakeprinter.Normal})
	r.run(t)
	r.srv.SetHostile(mockserver.IPInJob)
	id := r.srv.NewJob("demo", "p0", order("4001"))
	if o := r.outcome(t, id); o.Kind != "failed" {
		t.Fatalf("outcome = %+v; want the job refused", o)
	}
	select {
	case c := <-r.conns["p0"]:
		t.Fatalf("a job carrying an address reached the printer: %q", c.Data)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestRevokedCredentialStopsTheAgent(t *testing.T) {
	r := newRig(t, mockserver.Config{Hold: 300 * time.Millisecond}, fakeprinter.Config{Mode: fakeprinter.Normal})
	a := r.agent(t)
	done := make(chan error, 1)
	go func() { done <- a.Run(context.Background()) }()
	time.Sleep(200 * time.Millisecond)
	dev := r.srv.Snapshot().Devices[0].ID
	req, _ := http.NewRequest("POST", r.cfg.Server+"/mock/devices/"+dev+"/revoke", nil)
	http.DefaultClient.Do(req)
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "re-pair") {
			t.Fatalf("Run returned %v; want a re-pair error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("agent kept running after its credential was revoked")
	}
}

func TestKannadaOrderPrintsAsRaster(t *testing.T) {
	r := newRig(t, mockserver.Config{Hold: 2 * time.Second}, fakeprinter.Config{Mode: fakeprinter.Normal, Status: fakeprinter.StatusOK})
	r.run(t)
	slip := order("6001")
	slip.Items = []api.Item{{Quantity: 2, Name: "ಮಸಾಲೆ ದೋಸೆ", Notes: "ಕಡಿಮೆ ಖಾರ"}, {Quantity: 1, Name: "Filter Coffee"}}
	id := r.srv.NewJob("demo", "p0", slip)
	if o := r.outcome(t, id); o.Kind != "printed" {
		t.Fatalf("outcome = %+v, want printed", o)
	}
	conn := <-r.conns["p0"]
	rasters := 0
	for _, c := range escpos.Decode(conn.Data) {
		if c.Name == "GS v 0" {
			rasters++
		}
	}
	if rasters < 2 || !strings.Contains(ticketText(conn.Data), "1 x Filter Coffee") {
		t.Fatalf("got %d raster lines and text %q; want Kannada as raster, English as text", rasters, ticketText(conn.Data))
	}
}
