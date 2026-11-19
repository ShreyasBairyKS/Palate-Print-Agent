package discover

import (
	"context"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/internal/fakeprinter"
)

func startFake(t *testing.T, st fakeprinter.Status) (string, *fakeprinter.Printer) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p, err := fakeprinter.New(fakeprinter.Config{Status: st})
	if err != nil {
		t.Fatal(err)
	}
	go p.Serve(ln)
	t.Cleanup(func() { p.Close() })
	return ln.Addr().String(), p
}

func TestScanTellsReceiptPrintersFromOtherDevices(t *testing.T) {
	receipt, _ := startFake(t, fakeprinter.StatusOK)
	silent, _ := startFake(t, fakeprinter.StatusNone) // a printer that never answers, or not a printer
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	closed := ln.Addr().String()
	ln.Close()

	start := time.Now()
	got := Scan(context.Background(), []string{silent, closed, receipt, receipt}, Options{StatusTimeout: 300 * time.Millisecond})
	if len(got) != 2 {
		t.Fatalf("found %+v; want the two listening devices, once each", got)
	}
	if got[0].Addr != receipt || !got[0].Answers {
		t.Errorf("first = %+v; want the receipt printer, answering", got[0])
	}
	if got[1].Addr != silent || got[1].Answers {
		t.Errorf("second = %+v; want the silent device, not answering", got[1])
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("scan took %s", d)
	}
}

func TestScanPrintsNothing(t *testing.T) {
	conns := make(chan fakeprinter.Conn, 1)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	p, _ := fakeprinter.New(fakeprinter.Config{OnConn: func(c fakeprinter.Conn) { conns <- c }})
	go p.Serve(ln)
	defer p.Close()
	Scan(context.Background(), []string{ln.Addr().String()}, Options{})
	select {
	case c := <-conns:
		if len(c.Data) != 3 || c.Data[0] != 0x10 || c.Data[1] != 0x04 {
			t.Errorf("the probe sent % x; want only DLE EOT", c.Data)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the fake printer saw no connection")
	}
}

func TestScanStopsWhenCancelled(t *testing.T) {
	var targets []string
	for i := range 2000 {
		targets = append(targets, netip.AddrPortFrom(netip.AddrFrom4([4]byte{10, 255, byte(i >> 8), byte(i)}), Port).String())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	slow := func(ctx context.Context, _, _ string) (net.Conn, error) { <-ctx.Done(); return nil, ctx.Err() }
	start := time.Now()
	Scan(ctx, targets, Options{Dial: slow})
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("a cancelled scan took %s", d)
	}
}

func TestNarrowSearchesOnlyTheLocalPrivateSlash24(t *testing.T) {
	for _, c := range []struct {
		ip   string
		ones int
		want string // "" = not searched
	}{
		{"192.168.1.37", 24, "192.168.1.0/24"},
		{"10.20.30.40", 8, "10.20.30.0/24"}, // big office network: only this PC's /24
		{"172.16.5.9", 16, "172.16.5.0/24"},
		{"192.168.1.37", 26, "192.168.1.0/26"},
		{"203.0.113.5", 24, ""},  // public
		{"169.254.10.1", 16, ""}, // link-local
		{"10.0.0.1", 32, ""},     // point-to-point (VPN)
	} {
		n, ok := narrow("eth", netip.MustParseAddr(c.ip), c.ones)
		got := ""
		if ok {
			got = n.Prefix.String()
		}
		if got != c.want {
			t.Errorf("%s/%d: searched %q, want %q", c.ip, c.ones, got, c.want)
		}
	}
}

func TestHostsSkipsNetworkBroadcastAndSelf(t *testing.T) {
	n, _ := narrow("eth", netip.MustParseAddr("192.168.1.37"), 24)
	h := Hosts(n)
	if len(h) != 253 {
		t.Fatalf("%d hosts; want 253 (254 minus this PC)", len(h))
	}
	if h[0].String() != "192.168.1.1" || h[len(h)-1].String() != "192.168.1.254" {
		t.Errorf("hosts run %s..%s", h[0], h[len(h)-1])
	}
	for _, a := range h {
		if a == n.Self {
			t.Fatal("this PC is in the list")
		}
	}
}

func TestJoinPortAcceptsOnlyPrivateIPs(t *testing.T) {
	for _, c := range []struct {
		host string
		port uint64
		want string
		ok   bool
	}{
		{"192.168.1.50", 9100, "192.168.1.50:9100", true},
		{"192.168.1.50", 0, "192.168.1.50:9100", true},
		{"10.0.0.9", 9101, "10.0.0.9:9101", true},
		{"8.8.8.8", 9100, "8.8.8.8:9100", false},
		{"printer.local", 9100, "", false},
	} {
		got, ok := joinPort(c.host, c.port)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("joinPort(%q, %d) = %q, %v", c.host, c.port, got, ok)
		}
	}
}

func TestValidStatus(t *testing.T) {
	for b, want := range map[byte]bool{0x12: true, 0x16: true, 0x1A: true, 0x00: false, 0xFF: false, 0x13: false, 0x92: false} {
		if validStatus(b) != want {
			t.Errorf("validStatus(%#x) = %v", b, !want)
		}
	}
}
