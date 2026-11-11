// Package usbprinter writes tickets to printers installed in Windows (usually
// USB), through the print spooler with datatype RAW, so the ESC/POS bytes
// reach the printer untouched by any driver.
//
// The spooler gives no paper status, so the best outcome is "written": the
// spooler accepted every byte. It also accepts jobs for a printer that is
// unplugged or out of paper; they wait in its queue. Checking the spooler
// job's status afterwards is left for M3.
//
// The service runs as LocalSystem, which sees printers installed for all
// users but not ones added for a single user. `palate-agent printers` lists
// what this process can see. Nothing here may import net/http.
package usbprinter

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/printer"
)

// Writer sends tickets to one spooler printer.
type Writer struct {
	Name         string        // the printer's name in Windows, e.g. "EPSON TM-T82 Receipt"
	StallTimeout time.Duration // a spooler call making no progress this long is a hang; default 10s
}

var _ printer.Writer = (*Writer)(nil)

// ErrUnsupported is returned off Windows.
var ErrUnsupported = errors.New("USB printers need the Windows print spooler")

func (w *Writer) Write(ctx context.Context, data []byte) (printer.Result, error) {
	stall := w.StallTimeout
	if stall <= 0 {
		stall = 10 * time.Second
	}
	type result struct {
		written int
		err     error
		stage   string
	}
	done := make(chan result, 1)
	go func() {
		n, stage, err := spool(w.Name, data)
		done <- result{n, err, stage}
	}()
	select {
	case r := <-done:
		switch {
		case r.err == nil:
			return printer.Result{}, nil // spooler has no status: "written"
		case r.written > 0:
			return printer.Result{}, &printer.Error{Kind: printer.Partial, Written: r.written, Err: fmt.Errorf("%s: %w", r.stage, r.err)}
		default:
			return printer.Result{}, &printer.Error{Kind: printer.Refused, Err: fmt.Errorf("%s %q: %w", r.stage, w.Name, r.err)}
		}
	case <-time.After(stall):
		// The spooler is stuck. The call carries on in the background; there
		// is no safe way to abandon a spooler handle mid-call.
		return printer.Result{}, &printer.Error{Kind: printer.Hang, Err: fmt.Errorf("print spooler made no progress in %s", stall)}
	case <-ctx.Done():
		return printer.Result{}, ctx.Err()
	}
}

// List returns the printers this process can see, as the spooler reports them.
func List() ([]string, error) { return list() }
