package fakeprinter

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/internal/escpos"
)

// Ticket is one finished connection as the demo page shows it: what arrived,
// what the printer did, and the paper it would have produced.
type Ticket struct {
	Seq    int           `json:"seq"`
	At     time.Time     `json:"at"`
	Mode   Mode          `json:"mode"`
	Read   int           `json:"read"`
	Kept   int           `json:"kept"`
	Note   string        `json:"note"`
	Lines  []escpos.Line `json:"lines"`
	Millis int64         `json:"millis"`
}

// Control is a small HTTP API over a running fake printer, for the demo page:
// read and change its mode, and see the tickets it printed. It is rig
// tooling, meant to listen on loopback only.
type Control struct {
	Name    string
	Addr    string // where the printer listens, shown on the page
	Printer *Printer

	mu      sync.Mutex
	tickets []Ticket // oldest first, at most keep
}

const keep = 30

// Record stores a finished connection. Use it as (part of) Config.OnConn.
func (c *Control) Record(conn Conn) {
	t := Ticket{Seq: conn.Seq, At: conn.Started, Mode: conn.Mode, Read: conn.Read, Kept: len(conn.Data),
		Note: conn.Note, Millis: conn.Ended.Sub(conn.Started).Milliseconds(), Lines: []escpos.Line{}}
	if lines := escpos.Preview(conn.Data); len(lines) > 0 {
		t.Lines = lines
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tickets = append(c.tickets, t)
	if len(c.tickets) > keep {
		c.tickets = c.tickets[len(c.tickets)-keep:]
	}
}

type controlState struct {
	Name      string `json:"name"`
	Addr      string `json:"addr"`
	Mode      Mode   `json:"mode"`
	Status    Status `json:"status"`
	StopAfter int    `json:"stop_after"`
}

func (c *Control) state() controlState {
	m, s, n := c.Printer.Settings()
	return controlState{Name: c.Name, Addr: c.Addr, Mode: m, Status: s, StopAfter: n}
}

// Handler serves:
//
//	GET    /state    name, address, mode, status
//	PUT    /state    {"mode": "refuse", "status": "ok", "stop_after": 64} (any subset)
//	GET    /tickets  the last tickets, newest first, with previews
//	DELETE /tickets  forget them
func (c *Control) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /state", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, c.state())
	})
	mux.HandleFunc("PUT /state", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Mode      *Mode   `json:"mode"`
			Status    *Status `json:"status"`
			StopAfter *int    `json:"stop_after"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mode, _, stop := c.Printer.Settings()
		if req.Mode != nil {
			mode = *req.Mode
		}
		if req.StopAfter != nil {
			stop = *req.StopAfter
		}
		if err := c.Printer.SetMode(mode, stop); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.Status != nil {
			if err := c.Printer.SetStatus(*req.Status); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		writeJSON(w, http.StatusOK, c.state())
	})
	mux.HandleFunc("GET /tickets", func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		out := make([]Ticket, 0, len(c.tickets))
		for i := len(c.tickets) - 1; i >= 0; i-- {
			out = append(out, c.tickets[i])
		}
		c.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"tickets": out})
	})
	mux.HandleFunc("DELETE /tickets", func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		c.tickets = nil
		c.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
