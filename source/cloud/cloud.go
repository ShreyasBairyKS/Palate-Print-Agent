// Package cloud is the JobSource that talks to Palate's server: long polls
// for jobs, reports outcomes, registers printers and handles credential
// rotation.
//
// It is the only package that knows the wire format (from package api, which
// is provisional until backend-notes.md is available) and the only one below
// the agent loop allowed to use net/http.
package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/api"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/job"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/pair"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/source"
)

var _ source.JobSource = (*Client)(nil)

var (
	// ErrRepairNeeded means the credential was revoked or replaced. The agent
	// must stop polling and show "re-pair needed".
	ErrRepairNeeded = errors.New("cloud: credential revoked or replaced; re-pair this till")
	// ErrUnauthorized means the server doesn't recognise the credential.
	ErrUnauthorized = errors.New("cloud: credential not recognised; re-pair this till")
	// ErrTooLarge means a response passed api.MaxResponseBytes and was refused.
	ErrTooLarge = errors.New("cloud: response larger than the agent accepts")
	// ErrUnknownJob means the server has no such job for this restaurant.
	ErrUnknownJob = errors.New("cloud: unknown job")
)

// Client is a JobSource backed by the Palate cloud.
type Client struct {
	Server string
	Store  pair.Store
	HTTP   *http.Client // nil means a default client (no overall timeout; each call has its own)
	Health func() api.Health
	Log    *slog.Logger
	// Hold is the longest the server holds a poll. Fetch waits Hold+15s
	// before giving up on a response. Default 25s.
	Hold time.Duration

	mu   sync.Mutex
	cred string
}

func (c *Client) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c *Client) log() *slog.Logger {
	if c.Log != nil {
		return c.Log
	}
	return slog.Default()
}

func (c *Client) credential() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cred == "" {
		cred, err := c.Store.Load()
		if err != nil {
			return "", err
		}
		c.cred = cred
	}
	return c.cred, nil
}

// do sends one authenticated JSON request and returns the body of a 2xx
// response, read up to api.MaxResponseBytes.
func (c *Client) do(ctx context.Context, method, path string, in any) ([]byte, int, error) {
	cred, err := c.credential()
	if err != nil {
		return nil, 0, err
	}
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return nil, 0, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.Server, "/")+path, body)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+cred)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client().Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, api.MaxResponseBytes+1))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("cloud: reading %s response: %w", path, err)
	}
	if len(data) > api.MaxResponseBytes {
		return nil, resp.StatusCode, ErrTooLarge
	}
	if resp.StatusCode/100 == 2 {
		return data, resp.StatusCode, nil
	}
	var e api.Error
	json.Unmarshal(data, &e)
	switch {
	case resp.StatusCode == http.StatusUnauthorized && e.Code == "repair_needed":
		return nil, resp.StatusCode, ErrRepairNeeded
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, resp.StatusCode, ErrUnauthorized
	case resp.StatusCode == http.StatusNotFound:
		return nil, resp.StatusCode, ErrUnknownJob
	}
	return nil, resp.StatusCode, fmt.Errorf("cloud: %s %s: %d %s %s", method, path, resp.StatusCode, e.Code, e.Message)
}

// RegisterPrinters tells the server which printers this till owns. Addresses
// are never sent.
func (c *Client) RegisterPrinters(ctx context.Context, printers []api.Printer) error {
	_, _, err := c.do(ctx, http.MethodPut, api.PathPrinters, api.PrintersRequest{Printers: printers})
	return err
}

// wirePoll is the poll response as received: jobs stay raw so each can be
// checked on its own.
type wirePoll struct {
	Jobs          []json.RawMessage `json:"jobs"`
	LeaseSeconds  int               `json:"lease_seconds"`
	NewCredential string            `json:"new_credential"`
}

// Fetch long-polls for jobs. It returns as soon as the server has any, or
// with none when the server's hold time passes.
//
// Jobs that break the rules (an address, raw printer bytes, any unknown
// field, a printer id that is an address) are never returned: Fetch reports
// them as failed itself, so the floor sees the failure.
func (c *Client) Fetch(ctx context.Context) ([]job.Job, error) {
	hold := c.Hold
	if hold == 0 {
		hold = 25 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, hold+15*time.Second)
	defer cancel()

	var health api.Health
	if c.Health != nil {
		health = c.Health()
	}
	data, _, err := c.do(ctx, http.MethodPost, api.PathPoll, api.PollRequest{Health: health})
	if err != nil {
		return nil, err
	}
	var wp wirePoll
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&wp); err != nil {
		return nil, fmt.Errorf("cloud: bad poll response: %w", err)
	}

	if wp.NewCredential != "" {
		c.rotate(wp.NewCredential)
	}

	var jobs []job.Job
	seen := map[string]bool{}
	for _, raw := range wp.Jobs {
		j, err := parseJob(raw)
		if err != nil {
			c.reject(ctx, raw, err)
			continue
		}
		if seen[j.ID] {
			continue // the same job twice in one response prints once
		}
		seen[j.ID] = true
		jobs = append(jobs, j)
	}
	return jobs, nil
}

// rotate stores a new credential before using it. If storing fails, the old
// credential stays in use: the server keeps it valid until the new one is
// first used, so nothing is lost.
func (c *Client) rotate(newCred string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if newCred == c.cred {
		return
	}
	if err := c.Store.Save(newCred); err != nil {
		c.log().Error("credential rotation: could not store new credential; keeping the old one", "err", err)
		return
	}
	c.cred = newCred
	c.log().Info("credential rotated")
}

// parseJob decodes one job strictly and checks the rules.
func parseJob(raw json.RawMessage) (job.Job, error) {
	var aj api.Job
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&aj); err != nil {
		return job.Job{}, fmt.Errorf("rejected: %w", err)
	}
	if aj.ID == "" || aj.PrinterID == "" {
		return job.Job{}, errors.New("rejected: job without id or printer_id")
	}
	if looksLikeAddress(aj.PrinterID) {
		return job.Job{}, fmt.Errorf("rejected: printer_id %q is an address; jobs name printers by id", aj.PrinterID)
	}
	return toJob(aj), nil
}

func looksLikeAddress(s string) bool {
	if _, err := netip.ParseAddr(s); err == nil {
		return true
	}
	if host, _, err := net.SplitHostPort(s); err == nil {
		if _, err := netip.ParseAddr(host); err == nil {
			return true
		}
	}
	return false
}

// reject reports a job the agent refuses, if it has an id to report against.
func (c *Client) reject(ctx context.Context, raw json.RawMessage, reason error) {
	var idOnly struct {
		ID string `json:"id"`
	}
	json.Unmarshal(raw, &idOnly)
	c.log().Warn("refusing job", "job", idOnly.ID, "reason", reason)
	if idOnly.ID == "" {
		return
	}
	o := job.Outcome{Kind: job.Failed, Reason: "agent refused job: " + reason.Error()}
	if err := c.Report(ctx, idOnly.ID, o); err != nil {
		c.log().Error("could not report refused job", "job", idOnly.ID, "err", err)
	}
}

func toJob(a api.Job) job.Job {
	items := make([]job.Item, len(a.Slip.Items))
	for i, it := range a.Slip.Items {
		items[i] = job.Item{Quantity: it.Quantity, Name: it.Name, Notes: it.Notes}
	}
	return job.Job{
		ID:        a.ID,
		PrinterID: a.PrinterID,
		CreatedAt: a.CreatedAt,
		Slip: job.Slip{
			Station:     a.Slip.Station,
			Table:       a.Slip.Table,
			OrderNumber: a.Slip.OrderNumber,
			PlacedAt:    a.Slip.PlacedAt,
			Items:       items,
			Notes:       a.Slip.Notes,
			Duplicate:   a.Slip.Duplicate,
			MovedFrom:   a.Slip.MovedFrom,
		},
	}
}

// Report records a job's outcome. A repeated report is a no-op on the server.
func (c *Client) Report(ctx context.Context, jobID string, o job.Outcome) error {
	if err := o.Validate(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	path := strings.Replace(api.PathOutcome, "{id}", url.PathEscape(jobID), 1)
	_, _, err := c.do(ctx, http.MethodPost, path, api.OutcomeRequest{
		Kind: string(o.Kind), PrinterID: o.PrinterID, Reason: o.Reason,
	})
	return err
}
