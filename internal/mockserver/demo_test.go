package mockserver

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/internal/fakeprinter"
)

func TestDemoPageIsServed(t *testing.T) {
	h := newHarness(t, Config{})
	for _, path := range []string{"/", "/demo"} {
		resp, body := h.do("GET", path, "", nil)
		if resp.StatusCode != 200 || !strings.Contains(string(body), "live demo") ||
			!strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
			t.Fatalf("GET %s: %d %s", path, resp.StatusCode, resp.Header.Get("Content-Type"))
		}
	}
}

func TestDemoPrinterControlProxy(t *testing.T) {
	// A real fake printer with its control API.
	ctl := &fakeprinter.Control{Name: "Tandoor"}
	p, err := fakeprinter.New(fakeprinter.Config{Mode: fakeprinter.Normal, OnConn: ctl.Record})
	if err != nil {
		t.Fatal(err)
	}
	ctl.Printer = p
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	go p.Serve(ln)
	defer p.Close()
	cts := httptest.NewServer(ctl.Handler())
	defer cts.Close()

	h := newHarness(t, Config{PrinterControl: map[string]string{"p-tandoor": cts.URL}})

	resp, body := h.do("GET", "/mock/demo-config", "", nil)
	var cfg struct {
		Printers []struct{ ID string }
	}
	json.Unmarshal(body, &cfg)
	if resp.StatusCode != 200 || len(cfg.Printers) != 1 || cfg.Printers[0].ID != "p-tandoor" {
		t.Fatalf("demo-config: %d %s", resp.StatusCode, body)
	}

	// Switch the printer off through the proxy.
	req, _ := http.NewRequest("PUT", h.ts.URL+"/mock/printer-control/p-tandoor/state", strings.NewReader(`{"mode":"refuse"}`))
	r, err := http.DefaultClient.Do(req)
	if err != nil || r.StatusCode != 200 {
		t.Fatalf("PUT state via proxy: %v %v", r, err)
	}
	if m, _, _ := p.Settings(); m != fakeprinter.Refuse {
		t.Fatalf("printer mode = %s, want refuse", m)
	}

	// Tickets come back through the proxy.
	p.SetMode(fakeprinter.Normal, 0)
	c, _ := net.Dial("tcp", ln.Addr().String())
	c.Write([]byte("hello\n"))
	c.Close()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		r, _ := http.Get(h.ts.URL + "/mock/printer-control/p-tandoor/tickets")
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if strings.Contains(string(b), `"text":"hello"`) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("ticket never came back through the proxy")
}

func TestDemoProxyOnlyReachesConfiguredPrinters(t *testing.T) {
	h := newHarness(t, Config{PrinterControl: map[string]string{"p1": "http://127.0.0.1:1"}})
	if resp, _ := h.do("GET", "/mock/printer-control/elsewhere/state", "", nil); resp.StatusCode != 404 {
		t.Fatalf("unknown printer: %d, want 404", resp.StatusCode)
	}
	if resp, _ := h.do("GET", "/mock/printer-control/p1/secrets", "", nil); resp.StatusCode != 404 {
		t.Fatalf("unknown path: %d, want 404", resp.StatusCode)
	}
	if resp, _ := h.do("GET", "/mock/printer-control/p1/state", "", nil); resp.StatusCode != 502 {
		t.Fatalf("unreachable printer: %d, want 502", resp.StatusCode)
	}
}

func TestSilencedTillLooksOffline(t *testing.T) {
	h := newHarness(t, Config{Hold: 50 * time.Millisecond})
	cred, dev := h.pair("r1", "p1")
	h.poll(cred)
	before := h.srv.Snapshot().Devices[0].LastPoll

	h.do("POST", "/mock/devices/"+dev+"/silence", "", map[string]bool{"on": true})
	time.Sleep(20 * time.Millisecond)
	if _, resp := h.poll(cred); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("poll while silenced: %d, want 503", resp.StatusCode)
	}
	d := h.srv.Snapshot().Devices[0]
	if !d.Silenced || !d.LastPoll.Equal(before) {
		t.Fatalf("device = %+v; want silenced with its last poll unchanged", d)
	}

	h.do("POST", "/mock/devices/"+dev+"/silence", "", map[string]bool{"on": false})
	if _, resp := h.poll(cred); resp.StatusCode != 200 {
		t.Fatalf("poll after restore: %d", resp.StatusCode)
	}
}

func TestStateCarriesOrderDetails(t *testing.T) {
	h := newHarness(t, Config{})
	h.srv.SetBackups(map[string]string{"p1": "p2"})
	h.srv.NewJob("r1", "p1", slip())
	st := h.srv.Snapshot()
	j := st.Jobs[0]
	if j.Order != "1042" || j.Station != "Tandoor" || j.Items != 1 || j.CreatedAt.IsZero() || st.Backups["p1"] != "p2" {
		t.Fatalf("state = %+v / %+v", j, st.Backups)
	}
}

func TestSilenceAlsoStopsAHeldPoll(t *testing.T) {
	h := newHarness(t, Config{Hold: 3 * time.Second})
	cred, dev := h.pair("r1", "p1")
	got := make(chan int, 1)
	go func() {
		_, resp := h.poll(cred) // held: no jobs yet
		got <- resp.StatusCode
	}()
	time.Sleep(200 * time.Millisecond)
	h.do("POST", "/mock/devices/"+dev+"/silence", "", map[string]bool{"on": true})
	h.srv.NewJob("r1", "p1", slip()) // wakes the held poll
	select {
	case code := <-got:
		if code != http.StatusServiceUnavailable {
			t.Fatalf("held poll answered %d after the network was cut; want 503", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("held poll never returned")
	}
	if j := h.srv.Snapshot().Jobs[0]; j.Deliveries != 0 {
		t.Fatalf("job delivered %d times to a silenced till", j.Deliveries)
	}
}
