// Package netprinter writes tickets to network printers over raw TCP 9100,
// one connection at a time (most thermal printers silently drop a second).
//
// It may use package net, but never net/http (arch_test.go checks).
package netprinter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/printer"
)

// Writer sends tickets to one network printer.
type Writer struct {
	Addr           string        // host:port, already checked by config
	ConnectTimeout time.Duration // default 3s
	StallTimeout   time.Duration // a write making no progress this long is a hang; default 10s
	StatusTimeout  time.Duration // wait for a DLE EOT reply; default 500ms
	// Dial replaces net.Dialer.DialContext in tests (for example to fake a
	// connect timeout, which can't be produced on loopback).
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
}

var _ printer.Writer = (*Writer)(nil)

const chunk = 4096

// statusQuery asks for the paper sensor status (DLE EOT 4). Printers treat
// it as a real-time command: it is answered, never printed.
var statusQuery = []byte{0x10, 0x04, 4}

func (w *Writer) Write(ctx context.Context, data []byte) (printer.Result, error) {
	connectTimeout := orDefault(w.ConnectTimeout, 3*time.Second)
	stall := orDefault(w.StallTimeout, 10*time.Second)
	statusWait := orDefault(w.StatusTimeout, 500*time.Millisecond)

	dial := w.Dial
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	dctx, cancel := context.WithTimeout(ctx, connectTimeout)
	conn, err := dial(dctx, "tcp", w.Addr)
	cancel()
	if err != nil {
		if isTimeout(err) || errors.Is(dctx.Err(), context.DeadlineExceeded) {
			return printer.Result{}, &printer.Error{Kind: printer.Hang, Err: fmt.Errorf("connect to %s: no answer in %s", w.Addr, connectTimeout)}
		}
		return printer.Result{}, &printer.Error{Kind: printer.Refused, Err: err}
	}
	defer conn.Close()

	// Stop at once if the caller gives up (service stopping).
	stop := context.AfterFunc(ctx, func() { conn.SetDeadline(time.Now()) })
	defer stop()

	written := 0
	for written < len(data) {
		end := min(written+chunk, len(data))
		conn.SetWriteDeadline(time.Now().Add(stall))
		n, err := conn.Write(data[written:end])
		written += n
		if err != nil {
			return printer.Result{}, classify(err, written, stall)
		}
	}

	// Every byte is accepted. Ask the printer how it is; many never answer.
	// A reset here means the printer threw the connection away, possibly
	// before printing what the socket buffer accepted: treat it as partial.
	conn.SetWriteDeadline(time.Now().Add(stall))
	if _, err := conn.Write(statusQuery); err != nil && !isTimeout(err) {
		return printer.Result{}, &printer.Error{Kind: printer.Partial, Written: written, Err: fmt.Errorf("connection lost after write: %w", err)}
	}
	conn.SetReadDeadline(time.Now().Add(statusWait))
	reply := make([]byte, 1)
	n, err := conn.Read(reply)
	switch {
	case n == 1:
	case err == nil, isTimeout(err), errors.Is(err, io.EOF):
		return printer.Result{}, nil // no answer, or a clean close: "written"
	default:
		return printer.Result{}, &printer.Error{Kind: printer.Partial, Written: written, Err: fmt.Errorf("connection lost after write: %w", err)}
	}
	if paperOut(reply[0]) {
		return printer.Result{}, &printer.Error{Kind: printer.PaperOut, Written: written, Err: errors.New("printer reports paper end")}
	}
	return printer.Result{Confirmed: true}, nil
}

// paperOut reads a DLE EOT 4 reply: bits 5 and 6 set mean paper end.
// A valid reply always has bits 1 and 4 set and bits 0 and 7 clear.
func paperOut(b byte) bool {
	valid := b&0x12 == 0x12 && b&0x81 == 0
	return valid && b&0x60 == 0x60
}

func classify(err error, written int, stall time.Duration) error {
	if written > 0 {
		return &printer.Error{Kind: printer.Partial, Written: written, Err: err}
	}
	if isTimeout(err) {
		return &printer.Error{Kind: printer.Hang, Err: fmt.Errorf("write made no progress in %s", stall)}
	}
	return &printer.Error{Kind: printer.Refused, Err: err}
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.Is(err, os.ErrDeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout())
}

func orDefault(d, def time.Duration) time.Duration {
	if d <= 0 {
		return def
	}
	return d
}
