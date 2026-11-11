// Package fakeprinter is a stand-in for a thermal printer on raw TCP 9100.
//
// It can behave like a healthy printer or like each of the broken ones the
// agent has to survive. Real restaurant printers are not used until M4.
package fakeprinter

import (
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// Mode is how the fake printer treats each connection.
type Mode string

const (
	// Normal reads everything, keeps it, and answers DLE EOT status queries.
	Normal Mode = "normal"
	// Refuse resets every connection as soon as it is accepted.
	Refuse Mode = "refuse"
	// Discard reads everything and keeps none of it (paper out, never reported).
	Discard Mode = "discard"
	// Single behaves like Normal for one connection at a time. A second
	// concurrent connection is accepted and never read: its bytes are
	// silently lost, as on most real thermal printers.
	Single Mode = "single"
	// Stall accepts and never reads. Writes stall once socket buffers fill.
	Stall Mode = "stall"
	// Midstream reads StopAfter bytes, then stops reading but keeps the
	// connection open (goes silent mid-stream).
	Midstream Mode = "midstream"
	// Drop reads StopAfter bytes, then resets the connection (partial write).
	Drop Mode = "drop"
)

var Modes = []Mode{Normal, Refuse, Discard, Single, Stall, Midstream, Drop}

func ParseMode(s string) (Mode, error) {
	for _, m := range Modes {
		if string(m) == s {
			return m, nil
		}
	}
	return "", fmt.Errorf("unknown mode %q (want one of %v)", s, Modes)
}

// Status is how the printer answers DLE EOT queries.
type Status string

const (
	StatusOK       Status = "ok"
	StatusPaperOut Status = "paper-out"
	StatusNone     Status = "none" // never answers, like many cheap printers
)

func ParseStatus(s string) (Status, error) {
	switch Status(s) {
	case StatusOK, StatusPaperOut, StatusNone:
		return Status(s), nil
	}
	return "", fmt.Errorf("unknown status %q (want ok, paper-out or none)", s)
}

// Config sets up a fake printer.
type Config struct {
	Mode      Mode
	StopAfter int    // bytes read before Midstream/Drop misbehave
	Status    Status // DLE EOT answers in Normal and Single modes
	// OnConn, if set, is called when each connection ends, with what was kept.
	OnConn func(Conn)
}

// Conn is the record of one finished connection.
type Conn struct {
	Seq     int
	Remote  string
	Mode    Mode
	Data    []byte // bytes kept (empty for Refuse, Discard, Stall and dropped connections)
	Read    int    // bytes read from the socket, kept or not
	Note    string // what the printer did, e.g. "second connection, ignored"
	Started time.Time
	Ended   time.Time
}

// Printer is a running fake printer.
type Printer struct {
	cfg Config

	mu     sync.Mutex
	ln     net.Listener
	conns  map[net.Conn]struct{}
	active int // connections currently being served normally (for Single)
	seq    int
	closed bool
	done   chan struct{}
	wg     sync.WaitGroup
}

func New(cfg Config) (*Printer, error) {
	if cfg.Mode == "" {
		cfg.Mode = Normal
	}
	if _, err := ParseMode(string(cfg.Mode)); err != nil {
		return nil, err
	}
	if cfg.Status == "" {
		cfg.Status = StatusOK
	}
	if (cfg.Mode == Midstream || cfg.Mode == Drop) && cfg.StopAfter < 0 {
		return nil, errors.New("StopAfter must be >= 0")
	}
	return &Printer{cfg: cfg, conns: map[net.Conn]struct{}{}, done: make(chan struct{})}, nil
}

// Serve accepts connections on ln until Close is called.
func (p *Printer) Serve(ln net.Listener) error {
	p.mu.Lock()
	p.ln = ln
	p.mu.Unlock()
	for {
		c, err := ln.Accept()
		if err != nil {
			p.mu.Lock()
			closed := p.closed
			p.mu.Unlock()
			if closed {
				return nil
			}
			return err
		}
		p.mu.Lock()
		p.seq++
		seq := p.seq
		p.conns[c] = struct{}{}
		p.wg.Add(1)
		p.mu.Unlock()
		go p.handle(c, seq)
	}
}

// Close stops accepting, closes every open connection and waits for them.
func (p *Printer) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	close(p.done)
	if p.ln != nil {
		p.ln.Close()
	}
	for c := range p.conns {
		c.Close()
	}
	p.mu.Unlock()
	p.wg.Wait()
	return nil
}

func (p *Printer) handle(c net.Conn, seq int) {
	rec := Conn{Seq: seq, Remote: c.RemoteAddr().String(), Mode: p.cfg.Mode, Started: time.Now()}
	defer func() {
		c.Close()
		rec.Ended = time.Now()
		p.mu.Lock()
		delete(p.conns, c)
		p.mu.Unlock()
		if p.cfg.OnConn != nil {
			p.cfg.OnConn(rec)
		}
		p.wg.Done()
	}()

	switch p.cfg.Mode {
	case Refuse:
		reset(c)
		rec.Note = "reset on accept"

	case Discard:
		n, _ := io.Copy(io.Discard, c)
		rec.Read = int(n)
		rec.Note = "read and discarded"

	case Stall:
		rec.Note = "accepted, never read"
		p.holdOpen()

	case Midstream:
		rec.Data, rec.Read = readUpTo(c, p.cfg.StopAfter)
		rec.Note = fmt.Sprintf("went silent after %d bytes", rec.Read)
		p.holdOpen()

	case Drop:
		rec.Data, rec.Read = readUpTo(c, p.cfg.StopAfter)
		reset(c)
		rec.Note = fmt.Sprintf("reset after %d bytes", rec.Read)

	case Single:
		p.mu.Lock()
		busy := p.active > 0
		if !busy {
			p.active++
		}
		p.mu.Unlock()
		if busy {
			rec.Note = "second connection, never read"
			p.holdOpen()
			return
		}
		rec.Data, rec.Read = p.serveNormal(c)
		rec.Note = "read to end"
		p.mu.Lock()
		p.active--
		p.mu.Unlock()

	default: // Normal
		rec.Data, rec.Read = p.serveNormal(c)
		rec.Note = "read to end"
	}
}

// serveNormal reads until EOF, answering DLE EOT n queries as it goes.
func (p *Printer) serveNormal(c net.Conn) ([]byte, int) {
	var data []byte
	buf := make([]byte, 4096)
	var q statusParser
	for {
		n, err := c.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			data = append(data, chunk...)
			for _, b := range chunk {
				if fn, ok := q.feed(b); ok {
					if reply, ok := statusReply(p.cfg.Status, fn); ok {
						c.Write([]byte{reply})
					}
				}
			}
		}
		if err != nil {
			return data, len(data)
		}
	}
}

// statusParser spots DLE EOT n (0x10 0x04 n) in a byte stream, including
// sequences split across reads.
type statusParser struct{ state int }

func (s *statusParser) feed(b byte) (fn byte, ok bool) {
	switch s.state {
	case 0:
		if b == 0x10 {
			s.state = 1
		}
	case 1:
		if b == 0x04 {
			s.state = 2
		} else if b != 0x10 {
			s.state = 0
		}
	case 2:
		s.state = 0
		if b >= 1 && b <= 4 {
			return b, true
		}
	}
	return 0, false
}

// statusReply gives the one-byte answer to DLE EOT fn, following the Epson
// ESC/POS reference: bits 1 and 4 are always set (0x12).
func statusReply(st Status, fn byte) (byte, bool) {
	const base = 0x12
	switch st {
	case StatusNone:
		return 0, false
	case StatusPaperOut:
		switch fn {
		case 1:
			return base | 0x08, true // offline
		case 2:
			return base | 0x20, true // stopped by paper end
		case 3:
			return base, true
		case 4:
			return base | 0x60, true // paper end detected
		}
	default:
		return base, true
	}
	return 0, false
}

func readUpTo(c net.Conn, limit int) ([]byte, int) {
	data := make([]byte, 0, limit)
	buf := make([]byte, 4096)
	for len(data) < limit {
		want := limit - len(data)
		if want > len(buf) {
			want = len(buf)
		}
		n, err := c.Read(buf[:want])
		data = append(data, buf[:n]...)
		if err != nil {
			break
		}
	}
	return data, len(data)
}

// holdOpen keeps a connection open without reading from it, so the peer's
// writes back up in the socket buffers, until the printer is closed. A peer
// that gives up and closes its end is not noticed (that needs a Read); the
// connection is cleaned up when the fake printer stops.
func (p *Printer) holdOpen() { <-p.done }

// reset closes c with an RST instead of a FIN, as a refusing printer does.
func reset(c net.Conn) {
	if tc, ok := c.(*net.TCPConn); ok {
		tc.SetLinger(0)
	}
	c.Close()
}
