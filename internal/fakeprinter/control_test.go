package fakeprinter

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSetModeWhileRunning(t *testing.T) {
	addr, done := start(t, Config{Mode: Normal})
	p := printerAt(t, addr)
	c := dial(t, addr)
	c.Write([]byte("one"))
	c.Close()
	if got := next(t, done); string(got.Data) != "one" {
		t.Fatalf("normal mode kept %q", got.Data)
	}
	if err := p.SetMode(Refuse, 0); err != nil {
		t.Fatal(err)
	}
	c2 := dial(t, addr)
	c2.Write([]byte("two"))
	if got := next(t, done); got.Mode != Refuse || len(got.Data) != 0 {
		t.Fatalf("after SetMode(refuse): %+v", got)
	}
	if err := p.SetMode("bogus", 0); err == nil {
		t.Fatal("bogus mode accepted")
	}
}

// printers maps a test address to its Printer, so tests using start() can
// reach the Printer value.
var printers = map[string]*Printer{}

func printerAt(t *testing.T, addr string) *Printer {
	t.Helper()
	p, ok := printers[addr]
	if !ok {
		t.Fatalf("no printer recorded for %s", addr)
	}
	return p
}

func TestControlAPI(t *testing.T) {
	ctl := &Control{Name: "Tandoor", Addr: "127.0.0.1:0"}
	p, err := New(Config{Mode: Normal, OnConn: ctl.Record})
	if err != nil {
		t.Fatal(err)
	}
	ctl.Printer = p
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	go p.Serve(ln)
	defer p.Close()
	ts := httptest.NewServer(ctl.Handler())
	defer ts.Close()

	// A ticket arrives.
	c, _ := net.Dial("tcp", ln.Addr().String())
	c.Write([]byte("\x1b@\x1bE\x012 x Naan\n\x1bE\x00\x1dVB\x00"))
	c.Close()
	var tickets struct{ Tickets []Ticket }
	deadline := time.Now().Add(3 * time.Second)
	for len(tickets.Tickets) == 0 && time.Now().Before(deadline) {
		resp, _ := http.Get(ts.URL + "/tickets")
		json.NewDecoder(resp.Body).Decode(&tickets)
		resp.Body.Close()
		time.Sleep(20 * time.Millisecond)
	}
	if len(tickets.Tickets) != 1 || tickets.Tickets[0].Lines[0].Text != "2 x Naan" || !tickets.Tickets[0].Lines[0].Bold {
		t.Fatalf("tickets = %+v", tickets)
	}

	// Switch it to hang.
	req, _ := http.NewRequest("PUT", ts.URL+"/state", strings.NewReader(`{"mode":"stall","status":"none"}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("PUT /state: %v %v", resp, err)
	}
	if m, s, _ := p.Settings(); m != Stall || s != StatusNone {
		t.Fatalf("settings = %s %s", m, s)
	}
	bad, _ := http.NewRequest("PUT", ts.URL+"/state", bytes.NewReader([]byte(`{"mode":"explode"}`)))
	if resp, _ := http.DefaultClient.Do(bad); resp.StatusCode != 400 {
		t.Fatalf("bad mode: %d", resp.StatusCode)
	}
	del, _ := http.NewRequest("DELETE", ts.URL+"/tickets", nil)
	http.DefaultClient.Do(del)
	resp, _ = http.Get(ts.URL + "/tickets")
	json.NewDecoder(resp.Body).Decode(&tickets)
	if len(tickets.Tickets) != 0 {
		t.Fatal("tickets not cleared")
	}
}

func TestOffStopsListeningAndComesBack(t *testing.T) {
	addr, done := start(t, Config{Mode: Normal})
	p := printerAt(t, addr)
	if err := p.SetMode(Off, 0); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		c.Close()
		t.Fatal("connected to a printer that is switched off")
	}
	if err := p.SetMode(Normal, 0); err != nil {
		t.Fatal(err)
	}
	var c net.Conn
	var err error
	for i := 0; i < 50; i++ {
		if c, err = net.DialTimeout("tcp", addr, time.Second); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("printer did not come back on: %v", err)
	}
	c.Write([]byte("back"))
	c.Close()
	if got := next(t, done); string(got.Data) != "back" {
		t.Fatalf("after switching on, kept %q", got.Data)
	}
}
