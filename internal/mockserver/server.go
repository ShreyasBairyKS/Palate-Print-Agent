// Package mockserver is a stand-in for Palate's server: the four agent
// endpoints from package api, plus /mock/ control endpoints a test or a
// person uses to queue jobs, issue pairing codes and switch on hostile
// behaviour.
//
// PROVISIONAL: the endpoints follow package api, which follows the design
// plan's illustrative table until backend-notes.md is available.
package mockserver

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"palate-print-agent/api"
	"palate-print-agent/job"
)

// Flag switches on one hostile behaviour. Each one is something a real
// server, network or attacker could do, and the agent must survive it.
type Flag string

const (
	// IPInJob adds a printer address to every job. The agent must refuse it.
	IPInJob Flag = "ip-in-job"
	// RawBytes adds raw printer bytes to every job. The agent must refuse them.
	RawBytes Flag = "raw-bytes"
	// HugePayload pads poll responses to about 50 MB. The agent must stop
	// reading at api.MaxResponseBytes.
	HugePayload Flag = "huge-payload"
	// DropMidway sends half a poll response, then closes the connection. The
	// jobs stay leased until the lease expires.
	DropMidway Flag = "drop-midway"
	// ShortLease leases jobs for 2 s, so the lease expires mid-print.
	ShortLease Flag = "short-lease"
	// RepeatID sends every job twice in the same poll response.
	RepeatID Flag = "repeat-id"
)

var Flags = []Flag{IPInJob, RawBytes, HugePayload, DropMidway, ShortLease, RepeatID}

func ParseFlags(s string) ([]Flag, error) {
	var out []Flag
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !slices.Contains(Flags, Flag(part)) {
			return nil, fmt.Errorf("unknown hostile flag %q (want some of %v)", part, Flags)
		}
		out = append(out, Flag(part))
	}
	return out, nil
}

// Config sets the server's timings.
type Config struct {
	Hold           time.Duration // how long a poll is held open with no jobs (default 25 s)
	Lease          time.Duration // how long a fetched job is leased (default 60 s)
	PairingCodeTTL time.Duration // default 10 min
}

type jobState string

const (
	queued jobState = "queued"
	leased jobState = "leased"
	done   jobState = "done"
)

type device struct {
	ID          string
	Restaurant  string
	Name        string
	Credential  string
	Pending     string // rotated credential not yet used
	Revoked     bool
	Printers    []api.Printer
	Health      api.Health
	LastPoll    time.Time
	PairedAt    time.Time
	credHistory []string // superseded credentials, refused as repair_needed
}

type serverJob struct {
	Job        api.Job
	Restaurant string
	State      jobState
	LeasedTo   string
	LeaseUntil time.Time
	Deliveries int
	Outcome    *api.OutcomeRequest
	Reports    int
}

type pairingCode struct {
	Restaurant string
	Expires    time.Time
}

// Server is a running mock. Create it with New and stop it with Close.
type Server struct {
	cfg Config

	mu        sync.Mutex
	codes     map[string]pairingCode
	devices   map[string]*device // by device id
	byCred    map[string]*device // current and pending credentials
	jobs      map[string]*serverJob
	jobOrder  []string
	hostile   map[Flag]bool
	failedPar map[string][]time.Time // pairing failures by remote IP
	wake      chan struct{}          // closed and replaced when jobs become available
	seq       int

	stop chan struct{}
	mux  *http.ServeMux
}

func New(cfg Config) *Server {
	if cfg.Hold == 0 {
		cfg.Hold = 25 * time.Second
	}
	if cfg.Lease == 0 {
		cfg.Lease = 60 * time.Second
	}
	if cfg.PairingCodeTTL == 0 {
		cfg.PairingCodeTTL = 10 * time.Minute
	}
	s := &Server{
		cfg:       cfg,
		codes:     map[string]pairingCode{},
		devices:   map[string]*device{},
		byCred:    map[string]*device{},
		jobs:      map[string]*serverJob{},
		hostile:   map[Flag]bool{},
		failedPar: map[string][]time.Time{},
		wake:      make(chan struct{}),
		stop:      make(chan struct{}),
		mux:       http.NewServeMux(),
	}

	s.mux.HandleFunc("POST "+api.PathPair, s.handlePair)
	s.mux.HandleFunc("PUT "+api.PathPrinters, s.handlePrinters)
	s.mux.HandleFunc("POST "+api.PathPoll, s.handlePoll)
	s.mux.HandleFunc("POST "+api.PathOutcome, s.handleOutcome)

	s.mux.HandleFunc("POST /mock/pairing-codes", s.handleNewCode)
	s.mux.HandleFunc("POST /mock/jobs", s.handleNewJobs)
	s.mux.HandleFunc("GET /mock/state", s.handleState)
	s.mux.HandleFunc("PUT /mock/hostile", s.handleHostile)
	s.mux.HandleFunc("POST /mock/devices/{id}/revoke", s.handleRevoke)
	s.mux.HandleFunc("POST /mock/devices/{id}/rotate", s.handleRotate)

	go s.expireLeases()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

// Close stops the lease-expiry loop. It does not close any listener.
func (s *Server) Close() { close(s.stop) }

// SetHostile replaces the active hostile flags.
func (s *Server) SetHostile(flags ...Flag) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hostile = map[Flag]bool{}
	for _, f := range flags {
		s.hostile[f] = true
	}
}

// NewPairingCode issues a single-use code for a restaurant. In production
// only a logged-in restaurant admin can do this.
func (s *Server) NewPairingCode(restaurant string) (string, time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw := strings.ToUpper(randHex(4))
	code := raw[:4] + "-" + raw[4:]
	exp := time.Now().Add(s.cfg.PairingCodeTTL)
	s.codes[code] = pairingCode{Restaurant: restaurant, Expires: exp}
	return code, exp
}

// NewJob queues a job for a restaurant's printer and wakes waiting polls.
func (s *Server) NewJob(restaurant, printerID string, slip api.Slip) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	id := fmt.Sprintf("job-%06d", s.seq)
	s.jobs[id] = &serverJob{
		Job:        api.Job{ID: id, PrinterID: printerID, CreatedAt: time.Now().UTC(), Slip: slip},
		Restaurant: restaurant,
		State:      queued,
	}
	s.jobOrder = append(s.jobOrder, id)
	s.broadcastLocked()
	return id
}

func (s *Server) broadcastLocked() {
	close(s.wake)
	s.wake = make(chan struct{})
}

// expireLeases returns unreported jobs to the queue when their lease ends.
func (s *Server) expireLeases() {
	t := time.NewTicker(200 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case now := <-t.C:
			s.mu.Lock()
			expired := false
			for _, j := range s.jobs {
				if j.State == leased && now.After(j.LeaseUntil) {
					j.State = queued
					j.LeasedTo = ""
					expired = true
				}
			}
			if expired {
				s.broadcastLocked()
			}
			s.mu.Unlock()
		}
	}
}

// --- agent endpoints ---------------------------------------------------------

const maxRequestBytes = 64 << 10

func (s *Server) handlePair(w http.ResponseWriter, r *http.Request) {
	var req api.PairRequest
	if !decode(w, r, &req) {
		return
	}
	ip := remoteIP(r)

	s.mu.Lock()
	defer s.mu.Unlock()

	// Rate limit: 5 failed attempts per minute per address.
	recent := s.failedPar[ip][:0]
	for _, t := range s.failedPar[ip] {
		if time.Since(t) < time.Minute {
			recent = append(recent, t)
		}
	}
	s.failedPar[ip] = recent
	if len(recent) >= 5 {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many pairing attempts; wait a minute")
		return
	}

	pc, ok := s.codes[strings.ToUpper(strings.TrimSpace(req.Code))]
	if !ok || time.Now().After(pc.Expires) {
		s.failedPar[ip] = append(s.failedPar[ip], time.Now())
		writeError(w, http.StatusUnauthorized, "invalid_code", "pairing code is wrong, used or expired")
		return
	}
	delete(s.codes, strings.ToUpper(strings.TrimSpace(req.Code))) // single use

	d := &device{
		ID:         fmt.Sprintf("dev-%04d", len(s.devices)+1),
		Restaurant: pc.Restaurant,
		Name:       req.AgentName,
		Credential: randHex(32),
		PairedAt:   time.Now(),
	}
	s.devices[d.ID] = d
	s.byCred[d.Credential] = d
	writeJSON(w, http.StatusOK, api.PairResponse{DeviceID: d.ID, Credential: d.Credential})
}

func (s *Server) handlePrinters(w http.ResponseWriter, r *http.Request) {
	var req api.PrintersRequest
	if !decode(w, r, &req) {
		return
	}
	for _, p := range req.Printers {
		if p.ID == "" || (p.Kind != "network" && p.Kind != "usb") || !job.Width(p.Width).Valid() {
			writeError(w, http.StatusBadRequest, "bad_printer", fmt.Sprintf("printer %q: need id, kind network|usb, width 58|80", p.ID))
			return
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.authLocked(w, r)
	if !ok {
		return
	}
	d.Printers = req.Printers
	s.broadcastLocked() // jobs for these printers may now be deliverable
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePoll(w http.ResponseWriter, r *http.Request) {
	var req api.PollRequest
	if !decode(w, r, &req) {
		return
	}
	deadline := time.NewTimer(s.cfg.Hold)
	defer deadline.Stop()

	s.mu.Lock()
	d, ok := s.authLocked(w, r)
	if !ok {
		s.mu.Unlock()
		return
	}
	d.Health = req.Health
	d.LastPoll = time.Now()

	for {
		jobs := s.leaseLocked(d)
		if len(jobs) > 0 {
			hostile := s.copyHostileLocked()
			lease := s.leaseDuration()
			newCred := d.Pending
			s.mu.Unlock()
			s.writePoll(w, jobs, lease, newCred, hostile)
			return
		}
		wake := s.wake
		s.mu.Unlock()

		select {
		case <-wake:
		case <-deadline.C:
			s.mu.Lock()
			newCred := d.Pending
			lease := s.leaseDuration()
			s.mu.Unlock()
			writeJSON(w, http.StatusOK, api.PollResponse{Jobs: []api.Job{}, LeaseSeconds: int(lease / time.Second), NewCredential: newCred})
			return
		case <-r.Context().Done():
			return
		}
		s.mu.Lock()
	}
}

func (s *Server) leaseDuration() time.Duration {
	if s.hostile[ShortLease] {
		return 2 * time.Second
	}
	return s.cfg.Lease
}

// leaseLocked leases every queued job for printers this device registered.
// The restaurant comes from the credential, never from the request.
func (s *Server) leaseLocked(d *device) []api.Job {
	owned := map[string]bool{}
	for _, p := range d.Printers {
		owned[p.ID] = true
	}
	var out []api.Job
	until := time.Now().Add(s.leaseDuration())
	for _, id := range s.jobOrder {
		j := s.jobs[id]
		if j.State != queued || j.Restaurant != d.Restaurant || !owned[j.Job.PrinterID] {
			continue
		}
		j.State = leased
		j.LeasedTo = d.ID
		j.LeaseUntil = until
		j.Deliveries++
		out = append(out, j.Job)
	}
	return out
}

func (s *Server) copyHostileLocked() map[Flag]bool {
	c := make(map[Flag]bool, len(s.hostile))
	for k, v := range s.hostile {
		c[k] = v
	}
	return c
}

// wireJob is api.Job plus the fields only a hostile server would send.
type wireJob struct {
	api.Job
	PrinterAddress string `json:"printer_address,omitempty"`
	Raw            string `json:"raw,omitempty"`
	Padding        string `json:"padding,omitempty"`
}

type wirePoll struct {
	Jobs          []wireJob `json:"jobs"`
	LeaseSeconds  int       `json:"lease_seconds"`
	NewCredential string    `json:"new_credential,omitempty"`
}

func (s *Server) writePoll(w http.ResponseWriter, jobs []api.Job, lease time.Duration, newCred string, hostile map[Flag]bool) {
	resp := wirePoll{LeaseSeconds: int(lease / time.Second), NewCredential: newCred}
	for i, j := range jobs {
		wj := wireJob{Job: j}
		if hostile[IPInJob] {
			wj.PrinterAddress = "203.0.113.7:9100"
		}
		if hostile[RawBytes] {
			wj.Raw = "G0AbYQFIQUNLRUQKHVZB" // ESC @, ESC a 1, "HACKED", cut
		}
		if hostile[HugePayload] && i == 0 {
			wj.Padding = strings.Repeat("A", 50<<20)
		}
		resp.Jobs = append(resp.Jobs, wj)
		if hostile[RepeatID] {
			resp.Jobs = append(resp.Jobs, wj)
		}
	}
	body, err := json.Marshal(resp)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}

	if hostile[DropMidway] {
		// ResponseController follows Unwrap, so this works behind middleware.
		conn, buf, err := http.NewResponseController(w).Hijack()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "cannot hijack connection: "+err.Error())
			return
		}
		fmt.Fprintf(buf, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n", len(body))
		buf.Write(body[:len(body)/2])
		buf.Flush()
		conn.Close()
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(body)
}

func (s *Server) handleOutcome(w http.ResponseWriter, r *http.Request) {
	var req api.OutcomeRequest
	if !decode(w, r, &req) {
		return
	}
	o := job.Outcome{Kind: job.Kind(req.Kind), PrinterID: req.PrinterID, Reason: req.Reason}
	if err := o.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "bad_outcome", err.Error())
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.authLocked(w, r)
	if !ok {
		return
	}
	j, ok := s.jobs[r.PathValue("id")]
	if !ok || j.Restaurant != d.Restaurant {
		// Another restaurant's job looks exactly like a missing one.
		writeError(w, http.StatusNotFound, "not_found", "no such job")
		return
	}
	j.Reports++
	if j.State == done {
		w.WriteHeader(http.StatusNoContent) // repeated report: no-op
		return
	}
	j.State = done
	j.Outcome = &req
	w.WriteHeader(http.StatusNoContent)
}

// authLocked finds the device for the request's bearer credential. A pending
// (rotated) credential is promoted on first use, which retires the old one.
func (s *Server) authLocked(w http.ResponseWriter, r *http.Request) (*device, bool) {
	cred, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || cred == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing bearer credential")
		return nil, false
	}
	d, ok := s.byCred[cred]
	if !ok {
		for _, dev := range s.devices {
			if slices.Contains(dev.credHistory, cred) {
				writeError(w, http.StatusUnauthorized, "repair_needed", "credential was replaced; re-pair this till")
				return nil, false
			}
		}
		writeError(w, http.StatusUnauthorized, "unauthorized", "unknown credential")
		return nil, false
	}
	if d.Revoked {
		writeError(w, http.StatusUnauthorized, "repair_needed", "credential revoked; re-pair this till")
		return nil, false
	}
	if cred == d.Pending {
		delete(s.byCred, d.Credential)
		d.credHistory = append(d.credHistory, d.Credential)
		d.Credential = d.Pending
		d.Pending = ""
	}
	return d, true
}

// --- mock control endpoints ----------------------------------------------------

func (s *Server) handleNewCode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RestaurantID string `json:"restaurant_id"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.RestaurantID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "restaurant_id is required")
		return
	}
	code, exp := s.NewPairingCode(req.RestaurantID)
	writeJSON(w, http.StatusOK, map[string]any{"code": code, "expires_at": exp.UTC()})
}

func (s *Server) handleNewJobs(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RestaurantID string `json:"restaurant_id"`
		Jobs         []struct {
			PrinterID string   `json:"printer_id"`
			Slip      api.Slip `json:"slip"`
		} `json:"jobs"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.RestaurantID == "" || len(req.Jobs) == 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "restaurant_id and at least one job are required")
		return
	}
	var ids []string
	for _, j := range req.Jobs {
		if j.Slip.PlacedAt.IsZero() {
			j.Slip.PlacedAt = time.Now().UTC()
		}
		ids = append(ids, s.NewJob(req.RestaurantID, j.PrinterID, j.Slip))
	}
	writeJSON(w, http.StatusOK, map[string]any{"ids": ids})
}

func (s *Server) handleHostile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Flags []string `json:"flags"`
	}
	if !decode(w, r, &req) {
		return
	}
	flags, err := ParseFlags(strings.Join(req.Flags, ","))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_flag", err.Error())
		return
	}
	s.SetHostile(flags...)
	writeJSON(w, http.StatusOK, map[string]any{"flags": flags})
}

func (s *Server) handleRevoke(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.devices[r.PathValue("id")]
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "no such device")
		return
	}
	d.Revoked = true
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRotate(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.devices[r.PathValue("id")]
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "no such device")
		return
	}
	if d.Pending != "" {
		delete(s.byCred, d.Pending)
	}
	d.Pending = randHex(32)
	s.byCred[d.Pending] = d
	w.WriteHeader(http.StatusNoContent)
}

// State is a snapshot for tests and for people watching the mock.
type State struct {
	Devices []DeviceState `json:"devices"`
	Jobs    []JobState    `json:"jobs"`
	Hostile []Flag        `json:"hostile"`
}

type DeviceState struct {
	ID         string        `json:"id"`
	Restaurant string        `json:"restaurant"`
	Name       string        `json:"name"`
	Revoked    bool          `json:"revoked"`
	Rotating   bool          `json:"rotating"`
	Printers   []api.Printer `json:"printers"`
	Health     api.Health    `json:"health"`
	LastPoll   time.Time     `json:"last_poll"`
}

type JobState struct {
	ID         string              `json:"id"`
	Restaurant string              `json:"restaurant"`
	PrinterID  string              `json:"printer_id"`
	State      string              `json:"state"`
	LeasedTo   string              `json:"leased_to,omitempty"`
	Deliveries int                 `json:"deliveries"`
	Reports    int                 `json:"reports"`
	Outcome    *api.OutcomeRequest `json:"outcome,omitempty"`
}

func (s *Server) Snapshot() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := State{Devices: []DeviceState{}, Jobs: []JobState{}, Hostile: []Flag{}}
	for _, d := range s.devices {
		st.Devices = append(st.Devices, DeviceState{
			ID: d.ID, Restaurant: d.Restaurant, Name: d.Name, Revoked: d.Revoked,
			Rotating: d.Pending != "", Printers: d.Printers, Health: d.Health, LastPoll: d.LastPoll,
		})
	}
	sort.Slice(st.Devices, func(i, k int) bool { return st.Devices[i].ID < st.Devices[k].ID })
	for _, id := range s.jobOrder {
		j := s.jobs[id]
		st.Jobs = append(st.Jobs, JobState{
			ID: id, Restaurant: j.Restaurant, PrinterID: j.Job.PrinterID, State: string(j.State),
			LeasedTo: j.LeasedTo, Deliveries: j.Deliveries, Reports: j.Reports, Outcome: j.Outcome,
		})
	}
	for _, f := range Flags {
		if s.hostile[f] {
			st.Hostile = append(st.Hostile, f)
		}
	}
	return st
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Snapshot())
}

// --- helpers -------------------------------------------------------------------

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeError(w, http.StatusRequestEntityTooLarge, "too_large", "request body too large")
			return false
		}
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, api.Error{Code: code, Message: msg})
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand failing means the machine is broken
	}
	return hex.EncodeToString(b)
}

func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
