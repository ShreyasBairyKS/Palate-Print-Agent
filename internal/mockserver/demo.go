package mockserver

import (
	_ "embed"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// The demo page: a browser front end to the mock server for live demos.
// It places orders, shows every ticket's outcome, the tills and printers,
// and the paper each fake printer produced, and can break things on purpose.

//go:embed demo/index.html
var demoPage []byte

var controlClient = &http.Client{Timeout: 3 * time.Second}

func (s *Server) addDemoRoutes() {
	serve := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(demoPage)
	}
	s.mux.HandleFunc("GET /{$}", serve)
	s.mux.HandleFunc("GET /demo", serve)
	s.mux.HandleFunc("GET /mock/demo-config", s.handleDemoConfig)
	s.mux.HandleFunc("GET /mock/printer-control/{id}/{what}", s.proxyControl)
	s.mux.HandleFunc("PUT /mock/printer-control/{id}/{what}", s.proxyControl)
	s.mux.HandleFunc("DELETE /mock/printer-control/{id}/{what}", s.proxyControl)
}

type demoPrinter struct {
	ID string `json:"id"`
}

func (s *Server) handleDemoConfig(w http.ResponseWriter, r *http.Request) {
	ids := make([]string, 0, len(s.cfg.PrinterControl))
	for id := range s.cfg.PrinterControl {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	printers := make([]demoPrinter, len(ids))
	for i, id := range ids {
		printers[i] = demoPrinter{ID: id}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"printers":      printers,
		"hold_seconds":  int(s.cfg.Hold / time.Second),
		"lease_seconds": int(s.cfg.Lease / time.Second),
		"not_answering": int(s.cfg.NotAnswering / time.Second),
	})
}

// proxyControl forwards to a fake printer's control API, so the page talks
// to one origin. Only printers configured at startup can be reached, and
// only their state and tickets.
func (s *Server) proxyControl(w http.ResponseWriter, r *http.Request) {
	base, ok := s.cfg.PrinterControl[r.PathValue("id")]
	what := r.PathValue("what")
	if !ok || (what != "state" && what != "tickets") {
		writeError(w, http.StatusNotFound, "not_found", "no such printer control")
		return
	}
	target, err := url.JoinPath(base, what)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, http.MaxBytesReader(w, r.Body, 4<<10))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := controlClient.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "printer_unreachable", "fake printer control not answering: "+strings.TrimSpace(err.Error()))
		return
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, io.LimitReader(resp.Body, 8<<20))
}
