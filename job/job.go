// Package job holds the types both tracks agree on: a Job as it arrives from
// a JobSource, the Slip inside it that Render turns into bytes, and the
// Outcome reported back.
//
// This package is one of the two seams from the design plan. Changing it
// changes the contract between the wire track and the paper track, so both
// interns review any change here.
package job

import (
	"fmt"
	"time"
)

// Width is the paper width a printer takes.
type Width int

const (
	Width58 Width = 58 // 58 mm roll, 32 characters per line in font A
	Width80 Width = 80 // 80 mm roll, 48 characters per line in font A
)

func (w Width) Valid() bool { return w == Width58 || w == Width80 }

func (w Width) String() string { return fmt.Sprintf("%dmm", int(w)) }

// Job is one station's ticket for one order, as fetched from a JobSource.
//
// A job names its printer by id only. It never carries a printer address and
// never carries raw printer bytes: addresses live in the till's local config,
// and bytes are made on the till by Render.
type Job struct {
	ID        string    // unique per job; the ledger key
	PrinterID string    // id from the till's local config, never an address
	Slip      Slip      // what to print
	CreatedAt time.Time // when the order was confirmed (server clock)
}

// Slip is everything a kitchen order ticket (KOT) shows, and nothing more.
// No prices, no phone numbers, no addresses (DPDP Act 2023).
type Slip struct {
	Station     string    // "Tandoor"
	Table       string    // "T12"; empty for takeaway
	OrderNumber string    // "1042"
	PlacedAt    time.Time // original order time, always printed
	Items       []Item
	Notes       string // order-level note, e.g. "allergy: peanuts"

	// Banners. Decided by the wire track; Render only draws them.
	Duplicate       bool   // every reprint is marked DUPLICATE
	DuplicateReason string // e.g. "crash-recovery", "partial ticket on TANDOOR"
	MovedFrom       string // printer the server moved this job from (its till stopped answering)
	FallbackFrom    string // printer on this till that was down, so this prints on its backup
	Delayed         bool   // job older than the configured age
}

// Item is one line on the ticket.
type Item struct {
	Quantity int
	Name     string // may be Kannada, Devanagari or Tamil
	Notes    string // "less spicy"
}

// Kind is the recorded end state of a job. Every job ends in exactly one.
type Kind string

const (
	// Printed: the printer confirmed by status query after the write.
	Printed Kind = "printed"
	// Written: every byte was accepted, but the printer can't confirm.
	Written Kind = "written"
	// Fallback: printed or written on the station's nominated backup.
	Fallback Kind = "fallback"
	// Failed: not written anywhere. Reason says why.
	Failed Kind = "failed"
	// ScreenOnly: the station has no printer by choice. Not a failure.
	ScreenOnly Kind = "screen_only"
)

func (k Kind) Valid() bool {
	switch k {
	case Printed, Written, Fallback, Failed, ScreenOnly:
		return true
	}
	return false
}

// Outcome is what the agent reports for a job.
type Outcome struct {
	Kind      Kind
	PrinterID string // the printer that actually took the bytes, if any
	Reason    string // required for Fallback and Failed
}

// Validate enforces "swallowing an error is a bug": a fallback or a failure
// must say why.
func (o Outcome) Validate() error {
	if !o.Kind.Valid() {
		return fmt.Errorf("outcome: unknown kind %q", o.Kind)
	}
	if (o.Kind == Fallback || o.Kind == Failed) && o.Reason == "" {
		return fmt.Errorf("outcome: %s needs a reason", o.Kind)
	}
	return nil
}
