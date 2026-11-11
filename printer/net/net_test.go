package netprinter

import (
	"bytes"
	"context"
	"net"
	"testing"
	"time"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/internal/fakeprinter"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/printer"
)

// fake starts a fake printer and returns its address and finished connections.
func fake(t *testing.T, cfg fakeprinter.Config) (string, <-chan fakeprinter.Conn) {
	t.Helper()
	done := make(chan fakeprinter.Conn, 16)
	cfg.OnConn = func(c fakeprinter.Conn) { done <- c }
	p, err := fakeprinter.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go p.Serve(ln)
	t.Cleanup(func() { p.Close() })
	return ln.Addr().String(), done
}

var ticket = []byte("\x1b@TANDOOR\n2 x Paneer Tikka\n\x1dVB\x00")

func fast(addr string) *Writer {
	return &Writer{Addr: addr, ConnectTimeout: 3 * time.Second, StallTimeout: 300 * time.Millisecond, StatusTimeout: 200 * time.Millisecond}
}

func TestPrintedWhenStatusConfirms(t *testing.T) {
	addr, done := fake(t, fakeprinter.Config{Mode: fakeprinter.Normal, Status: fakeprinter.StatusOK})
	res, err := fast(addr).Write(context.Background(), ticket)
	if err != nil || !res.Confirmed {
		t.Fatalf("res = %+v, err = %v; want confirmed", res, err)
	}
	got := <-done
	if !bytes.HasPrefix(got.Data, ticket) {
		t.Fatalf("printer kept %q; want the ticket first", got.Data)
	}
}

func TestWrittenWhenPrinterNeverAnswers(t *testing.T) {
	addr, _ := fake(t, fakeprinter.Config{Mode: fakeprinter.Normal, Status: fakeprinter.StatusNone})
	res, err := fast(addr).Write(context.Background(), ticket)
	if err != nil || res.Confirmed {
		t.Fatalf("res = %+v, err = %v; want written, not confirmed", res, err)
	}
}

func TestPaperOut(t *testing.T) {
	addr, _ := fake(t, fakeprinter.Config{Mode: fakeprinter.Normal, Status: fakeprinter.StatusPaperOut})
	_, err := fast(addr).Write(context.Background(), ticket)
	if printer.KindOf(err) != printer.PaperOut {
		t.Fatalf("err = %v, want paper out", err)
	}
}

func TestRefusedWhenNothingListens(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	_, err := fast(addr).Write(context.Background(), ticket)
	if printer.KindOf(err) != printer.Refused {
		t.Fatalf("err = %v, want refused", err)
	}
}

func TestHangOnConnect(t *testing.T) {
	w := fast("192.168.1.50:9100")
	w.ConnectTimeout = 200 * time.Millisecond
	w.Dial = func(ctx context.Context, _, _ string) (net.Conn, error) {
		<-ctx.Done() // a printer that never answers the SYN
		return nil, ctx.Err()
	}
	start := time.Now()
	_, err := w.Write(context.Background(), ticket)
	if printer.KindOf(err) != printer.Hang {
		t.Fatalf("err = %v, want hang", err)
	}
	if time.Since(start) > time.Second {
		t.Fatalf("took %v; want about the 200 ms connect timeout", time.Since(start))
	}
}

func TestResetAfterWriteIsPartial(t *testing.T) {
	for _, mode := range []fakeprinter.Mode{fakeprinter.Drop, fakeprinter.Refuse} {
		t.Run(string(mode), func(t *testing.T) {
			addr, _ := fake(t, fakeprinter.Config{Mode: mode, StopAfter: 10})
			_, err := fast(addr).Write(context.Background(), ticket)
			if k := printer.KindOf(err); k != printer.Partial && k != printer.Refused {
				t.Fatalf("err = %v, want partial (or refused if the reset beat the first byte)", err)
			}
		})
	}
}

func TestStallOnBigTicketIsPartial(t *testing.T) {
	addr, _ := fake(t, fakeprinter.Config{Mode: fakeprinter.Stall})
	big := make([]byte, 64<<20) // far larger than any socket buffer
	start := time.Now()
	_, err := fast(addr).Write(context.Background(), big)
	if printer.KindOf(err) != printer.Partial {
		t.Fatalf("err = %v, want partial", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("took %v; the stall timeout is 300 ms", time.Since(start))
	}
}

func TestCancelStopsTheWrite(t *testing.T) {
	addr, _ := fake(t, fakeprinter.Config{Mode: fakeprinter.Stall})
	w := fast(addr)
	w.StallTimeout = time.Minute
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	start := time.Now()
	_, err := w.Write(ctx, make([]byte, 64<<20))
	if err == nil || time.Since(start) > 3*time.Second {
		t.Fatalf("err = %v after %v; want a prompt failure on cancel", err, time.Since(start))
	}
}

func TestProbe(t *testing.T) {
	ok, _ := fake(t, fakeprinter.Config{Mode: fakeprinter.Normal, Status: fakeprinter.StatusOK})
	if err := fast(ok).Probe(context.Background()); err != nil {
		t.Fatalf("healthy printer: %v", err)
	}
	silent, _ := fake(t, fakeprinter.Config{Mode: fakeprinter.Normal, Status: fakeprinter.StatusNone})
	if err := fast(silent).Probe(context.Background()); err != nil {
		t.Fatalf("printer that never answers status: %v; reachable counts as up", err)
	}
	paper, _ := fake(t, fakeprinter.Config{Mode: fakeprinter.Normal, Status: fakeprinter.StatusPaperOut})
	if err := fast(paper).Probe(context.Background()); err == nil {
		t.Fatal("paper-out printer probed as up")
	}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	gone := ln.Addr().String()
	ln.Close()
	if err := fast(gone).Probe(context.Background()); err == nil {
		t.Fatal("nothing listening, probed as up")
	}
}

func TestPaperOutReplyDecoding(t *testing.T) {
	cases := map[byte]bool{0x12: false, 0x72: true, 0x1E: false, 0xFF: false, 0x60: false}
	for b, want := range cases {
		if paperOut(b) != want {
			t.Errorf("paperOut(0x%02x) = %v, want %v", b, !want, want)
		}
	}
}
