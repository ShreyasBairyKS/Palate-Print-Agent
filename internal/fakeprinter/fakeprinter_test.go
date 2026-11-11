package fakeprinter

import (
	"bytes"
	"errors"
	"net"
	"os"
	"testing"
	"time"
)

// start runs a fake printer on a free loopback port and returns its address
// and a channel of finished connections.
func start(t *testing.T, cfg Config) (string, <-chan Conn) {
	t.Helper()
	done := make(chan Conn, 16)
	cfg.OnConn = func(c Conn) { done <- c }
	p, err := New(cfg)
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

func dial(t *testing.T, addr string) net.Conn {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func next(t *testing.T, ch <-chan Conn) Conn {
	t.Helper()
	select {
	case c := <-ch:
		return c
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for the fake printer to finish a connection")
		return Conn{}
	}
}

func TestNormalKeepsEveryByte(t *testing.T) {
	addr, done := start(t, Config{Mode: Normal})
	c := dial(t, addr)
	payload := bytes.Repeat([]byte("ticket "), 10000) // 70 kB, several reads
	if _, err := c.Write(payload); err != nil {
		t.Fatal(err)
	}
	c.Close()
	got := next(t, done)
	if !bytes.Equal(got.Data, payload) {
		t.Fatalf("kept %d bytes, want %d", len(got.Data), len(payload))
	}
}

func TestNormalAnswersStatusQueries(t *testing.T) {
	cases := []struct {
		status Status
		query  byte
		want   []byte
	}{
		{StatusOK, 4, []byte{0x12}},
		{StatusPaperOut, 4, []byte{0x72}},
		{StatusPaperOut, 1, []byte{0x1A}},
		{StatusNone, 4, nil},
	}
	for _, tc := range cases {
		t.Run(string(tc.status), func(t *testing.T) {
			addr, _ := start(t, Config{Mode: Normal, Status: tc.status})
			c := dial(t, addr)
			// Split the query across two writes to exercise the parser.
			c.Write([]byte{'x', 0x10})
			time.Sleep(20 * time.Millisecond)
			c.Write([]byte{0x04, tc.query})
			c.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
			buf := make([]byte, 4)
			n, err := c.Read(buf)
			if tc.want == nil {
				var ne net.Error
				if !errors.As(err, &ne) || !ne.Timeout() {
					t.Fatalf("got %x, %v; want no reply", buf[:n], err)
				}
				return
			}
			if !bytes.Equal(buf[:n], tc.want) {
				t.Fatalf("reply = %x, want %x", buf[:n], tc.want)
			}
		})
	}
}

func TestRefuseResets(t *testing.T) {
	addr, done := start(t, Config{Mode: Refuse})
	c := dial(t, addr)
	next(t, done)
	c.SetDeadline(time.Now().Add(2 * time.Second))
	// After an RST, a read returns an error rather than a clean EOF on most
	// stacks; either way nothing is ever printed.
	_, err := c.Read(make([]byte, 1))
	if err == nil {
		t.Fatal("read succeeded on a refused connection")
	}
}

func TestSingleIgnoresSecondConnection(t *testing.T) {
	addr, done := start(t, Config{Mode: Single})
	first := dial(t, addr)
	first.Write([]byte("first"))
	time.Sleep(50 * time.Millisecond) // let the printer take the first connection

	second := dial(t, addr)
	second.Write([]byte("second")) // accepted by the kernel, never read
	first.Close()

	got := next(t, done)
	if string(got.Data) != "first" {
		t.Fatalf("first connection kept %q, want %q", got.Data, "first")
	}
	select {
	case c := <-done:
		t.Fatalf("second connection finished early: %+v", c)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestStallBlocksTheWriter(t *testing.T) {
	addr, _ := start(t, Config{Mode: Stall})
	c := dial(t, addr)
	c.SetWriteDeadline(time.Now().Add(500 * time.Millisecond))
	chunk := make([]byte, 64<<10)
	var err error
	for i := 0; i < 1000 && err == nil; i++ { // up to 64 MB, far beyond any socket buffer
		_, err = c.Write(chunk)
	}
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("write error = %v, want a deadline timeout", err)
	}
}

func TestMidstreamGoesSilent(t *testing.T) {
	addr, done := start(t, Config{Mode: Midstream, StopAfter: 10})
	c := dial(t, addr)
	c.Write([]byte("0123456789abcdef"))
	c.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Fatal("connection closed; want it held open")
	}
	select {
	case got := <-done:
		t.Fatalf("connection finished: %+v", got)
	default:
	}
}

func TestDropResetsAfterPartialWrite(t *testing.T) {
	addr, done := start(t, Config{Mode: Drop, StopAfter: 10})
	c := dial(t, addr)
	c.Write([]byte("0123456789abcdef"))
	got := next(t, done)
	if string(got.Data) != "0123456789" {
		t.Fatalf("kept %q, want the first 10 bytes", got.Data)
	}
}

func TestStatusParserRejectsNonQueries(t *testing.T) {
	var p statusParser
	var found []byte
	for _, b := range []byte{0x10, 0x10, 0x04, 0x04, 0x10, 0x04, 0x09, 0x10, 0x04, 0x01} {
		if fn, ok := p.feed(b); ok {
			found = append(found, fn)
		}
	}
	if !bytes.Equal(found, []byte{4, 1}) {
		t.Fatalf("found queries %v, want [4 1]", found)
	}
}
