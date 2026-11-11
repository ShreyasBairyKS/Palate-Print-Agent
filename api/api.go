// Package api is the wire format between the agent and Palate's server.
//
// PROVISIONAL. backend-notes.md defines the four real endpoints and wins on
// any difference; it was not available when this was written. These paths
// and shapes follow the design plan's illustrative table. When
// backend-notes.md arrives, only this package, source/cloud and the mock
// server should need to change.
package api

import "time"

// Paths. All but Pair require "Authorization: Bearer <device credential>".
const (
	PathPair     = "/v1/pair"              // POST  PairRequest -> PairResponse
	PathPrinters = "/v1/printers"          // PUT   PrintersRequest -> 204
	PathPoll     = "/v1/jobs/poll"         // POST  PollRequest -> PollResponse (long poll)
	PathOutcome  = "/v1/jobs/{id}/outcome" // POST  OutcomeRequest -> 204
)

// MaxResponseBytes is the most the agent will read from any response. A
// larger body is an attack or a bug; the agent must refuse it.
const MaxResponseBytes = 1 << 20 // 1 MiB

// PairRequest redeems a single-use pairing code.
type PairRequest struct {
	Code      string `json:"code"`
	AgentName string `json:"agent_name"` // e.g. the till's hostname, for the portal
}

type PairResponse struct {
	DeviceID   string `json:"device_id"`
	Credential string `json:"credential"`
}

// Printer is one printer the agent can see. Addresses are never sent: the
// server knows printers only by id and name.
type Printer struct {
	ID    string `json:"id"`
	Name  string `json:"name"`  // "Tandoor"
	Kind  string `json:"kind"`  // "network" or "usb"
	Width int    `json:"width"` // 58 or 80
}

type PrintersRequest struct {
	Printers []Printer `json:"printers"`
}

// PollRequest carries the agent's health on every poll. No customer data.
type PollRequest struct {
	Health Health `json:"health"`
}

type Health struct {
	AgentVersion string          `json:"agent_version"`
	QueueDepth   int             `json:"queue_depth"`
	LastError    string          `json:"last_error,omitempty"` // an error code, never slip contents
	Printers     []PrinterHealth `json:"printers"`
	BadVersion   string          `json:"bad_version,omitempty"` // an update that was reverted
}

type PrinterHealth struct {
	ID        string `json:"id"`
	State     string `json:"state"` // "up", "down", "unknown"
	LastError string `json:"last_error,omitempty"`
}

// PollResponse is returned as soon as a job exists, or with no jobs once the
// server's hold time passes.
type PollResponse struct {
	Jobs         []Job `json:"jobs"`
	LeaseSeconds int   `json:"lease_seconds"`
	// NewCredential is set when the server rotates the credential. The agent
	// stores it before using it; the old one stays valid until first use.
	NewCredential string `json:"new_credential,omitempty"`
}

type Job struct {
	ID        string    `json:"id"`
	PrinterID string    `json:"printer_id"`
	CreatedAt time.Time `json:"created_at"`
	Slip      Slip      `json:"slip"`
}

type Slip struct {
	Station     string    `json:"station"`
	Table       string    `json:"table,omitempty"`
	OrderNumber string    `json:"order_number"`
	PlacedAt    time.Time `json:"placed_at"`
	Items       []Item    `json:"items"`
	Notes       string    `json:"notes,omitempty"`
	Duplicate   bool      `json:"duplicate,omitempty"`
	MovedFrom   string    `json:"moved_from,omitempty"`
}

type Item struct {
	Quantity int    `json:"quantity"`
	Name     string `json:"name"`
	Notes    string `json:"notes,omitempty"`
}

type OutcomeRequest struct {
	Kind      string `json:"kind"` // printed, written, fallback, failed, screen_only
	PrinterID string `json:"printer_id,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// Error is the body of any non-2xx response.
type Error struct {
	Code    string `json:"code"` // "unauthorized", "repair_needed", "not_found", ...
	Message string `json:"message"`
}
