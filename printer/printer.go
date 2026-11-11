// Package printer defines how the queue talks to a printer, whatever kind it
// is. Network (printer/net) and USB (printer/usb, M2) writers both satisfy
// Writer, so callers treat them identically.
package printer

import (
	"context"
	"errors"
	"fmt"
)

// Writer sends one ticket's bytes to one printer.
type Writer interface {
	Write(ctx context.Context, data []byte) (Result, error)
}

// Result describes a successful write.
type Result struct {
	// Confirmed is true when the printer answered a status query after the
	// write and reported nothing wrong: the outcome is "printed". Otherwise
	// every byte was accepted but nobody can say more: "written".
	Confirmed bool
}

// Kind classifies a failed write, because each kind is handled differently.
type Kind int

const (
	// Refused: the connection was refused or reset before any byte was
	// written. Worth a quick retry.
	Refused Kind = iota + 1
	// Hang: connecting or writing made no progress within its timeout, and
	// nothing was written. No retry: the printer is marked down at once.
	Hang
	// Partial: the connection failed after some bytes were written, so a
	// fragment may be on paper. Never resent blind to the same printer.
	Partial
	// PaperOut: every byte was written but the printer reports paper end.
	PaperOut
)

func (k Kind) String() string {
	switch k {
	case Refused:
		return "refused"
	case Hang:
		return "hang"
	case Partial:
		return "partial write"
	case PaperOut:
		return "paper out"
	}
	return "unknown"
}

// Error is a failed write.
type Error struct {
	Kind    Kind
	Written int // bytes accepted before the failure
	Err     error
}

func (e *Error) Error() string {
	if e.Kind == Partial {
		return fmt.Sprintf("%s after %d bytes: %v", e.Kind, e.Written, e.Err)
	}
	return fmt.Sprintf("%s: %v", e.Kind, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

// KindOf returns the Kind of err, or 0 if it isn't a *Error.
func KindOf(err error) Kind {
	var pe *Error
	if errors.As(err, &pe) {
		return pe.Kind
	}
	return 0
}
