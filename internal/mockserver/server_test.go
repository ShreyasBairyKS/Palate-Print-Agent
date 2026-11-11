package mockserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"palate-print-agent/api"
)

type harness struct {
	t   *testing.T
	srv *Server
	ts  *httptest.Server
}

func newHarness(t *testing.T, cfg Config) *harness {
	t.Helper()
	if cfg.Hold == 0 {
		cfg.Hold = 500 * time.Millisecond
	}
	srv := New(cfg)
	ts := httptest.NewServer(srv)
	t.Cleanup(func() { ts.Close(); srv.Close() })
	return &harness{t: t, srv: srv, ts: ts}
}

func (h *harness) do(method, path, cred string, body any) (*http.Response, []byte) {
	h.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, h.ts.URL+path, r)
	if cred != "" {
		req.Header.Set("Authorization", "Bearer "+cred)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 100<<20))
	return resp, data
}

// pair creates a code for the restaurant, redeems it, and registers one
// printer, returning the credential.
func (h *harness) pair(restaurant, printerID string) (cred, deviceID string) {
	h.t.Helper()
	code, _ := h.srv.NewPairingCode(restaurant)
	resp, body := h.do("POST", api.PathPair, "", api.PairRequest{Code: code, AgentName: "till-1"})
	if resp.StatusCode != 200 {
		h.t.Fatalf("pair: %d %s", resp.StatusCode, body)
	}
	var pr api.PairResponse
	json.Unmarshal(body, &pr)
	resp, body = h.do("PUT", api.PathPrinters, pr.Credential, api.PrintersRequest{Printers: []api.Printer{
		{ID: printerID, Name: "Tandoor", Kind: "network", Width: 80},
	}})
	if resp.StatusCode != 204 {
		h.t.Fatalf("printers: %d %s", resp.StatusCode, body)
	}
	return pr.Credential, pr.DeviceID
}

func (h *harness) poll(cred string) (api.PollResponse, *http.Response) {
	h.t.Helper()
	resp, body := h.do("POST", api.PathPoll, cred, api.PollRequest{Health: api.Health{AgentVersion: "test"}})
	var pr api.PollResponse
	if resp.StatusCode == 200 {
		if err := json.Unmarshal(body, &pr); err != nil {
			h.t.Fatalf("poll body: %v", err)
		}
	}
	return pr, resp
}

func (h *harness) outcome(cred, id string, o api.OutcomeRequest) int {
	h.t.Helper()
	resp, _ := h.do("POST", strings.Replace(api.PathOutcome, "{id}", id, 1), cred, o)
	return resp.StatusCode
}

func slip() api.Slip {
	return api.Slip{Station: "Tandoor", Table: "T4", OrderNumber: "1042",
		Items: []api.Item{{Quantity: 2, Name: "Paneer Tikka"}}}
}

func TestPairingCodeIsSingleUse(t *testing.T) {
	h := newHarness(t, Config{})
	code, _ := h.srv.NewPairingCode("r1")
	if resp, _ := h.do("POST", api.PathPair, "", api.PairRequest{Code: code}); resp.StatusCode != 200 {
		t.Fatalf("first use: %d", resp.StatusCode)
	}
	if resp, _ := h.do("POST", api.PathPair, "", api.PairRequest{Code: code}); resp.StatusCode != 401 {
		t.Fatalf("second use: %d, want 401", resp.StatusCode)
	}
}

func TestPairingIsRateLimited(t *testing.T) {
	h := newHarness(t, Config{})
	for i := 0; i < 5; i++ {
		h.do("POST", api.PathPair, "", api.PairRequest{Code: "WRONG"})
	}
	code, _ := h.srv.NewPairingCode("r1")
	if resp, _ := h.do("POST", api.PathPair, "", api.PairRequest{Code: code}); resp.StatusCode != 429 {
		t.Fatalf("after 5 failures: %d, want 429", resp.StatusCode)
	}
}

func TestPollReturnsAsSoonAsAJobExists(t *testing.T) {
	h := newHarness(t, Config{Hold: 10 * time.Second})
	cred, _ := h.pair("r1", "p1")

	go func() {
		time.Sleep(300 * time.Millisecond)
		h.srv.NewJob("r1", "p1", slip())
	}()
	start := time.Now()
	pr, _ := h.poll(cred)
	elapsed := time.Since(start)

	if len(pr.Jobs) != 1 {
		t.Fatalf("got %d jobs, want 1", len(pr.Jobs))
	}
	// The design target is a ticket within about 1 s of confirmation.
	if elapsed > time.Second {
		t.Fatalf("poll took %v; want it answered the moment the job was queued", elapsed)
	}
}

func TestPollTimesOutEmpty(t *testing.T) {
	h := newHarness(t, Config{Hold: 300 * time.Millisecond})
	cred, _ := h.pair("r1", "p1")
	start := time.Now()
	pr, resp := h.poll(cred)
	if resp.StatusCode != 200 || len(pr.Jobs) != 0 {
		t.Fatalf("status %d, %d jobs; want 200 with none", resp.StatusCode, len(pr.Jobs))
	}
	if time.Since(start) < 250*time.Millisecond {
		t.Fatal("poll returned before the hold time")
	}
}

func TestRestaurantComesFromCredential(t *testing.T) {
	h := newHarness(t, Config{Hold: 200 * time.Millisecond})
	credA, _ := h.pair("restaurant-a", "p1")
	credB, _ := h.pair("restaurant-b", "p1") // same printer id, different restaurant
	id := h.srv.NewJob("restaurant-a", "p1", slip())

	if pr, _ := h.poll(credB); len(pr.Jobs) != 0 {
		t.Fatalf("restaurant B received restaurant A's job")
	}
	if code := h.outcome(credB, id, api.OutcomeRequest{Kind: "printed"}); code != 404 {
		t.Fatalf("B reporting A's job: %d, want 404", code)
	}
	if pr, _ := h.poll(credA); len(pr.Jobs) != 1 {
		t.Fatalf("restaurant A got %d jobs, want 1", len(pr.Jobs))
	}
}

func TestLeaseExpiryRequeues(t *testing.T) {
	h := newHarness(t, Config{Hold: 3 * time.Second, Lease: 400 * time.Millisecond})
	cred, _ := h.pair("r1", "p1")
	id := h.srv.NewJob("r1", "p1", slip())

	if pr, _ := h.poll(cred); len(pr.Jobs) != 1 {
		t.Fatal("first poll: no job")
	}
	// Don't report. The next poll should get the same job once the lease ends.
	start := time.Now()
	pr, _ := h.poll(cred)
	if len(pr.Jobs) != 1 || pr.Jobs[0].ID != id {
		t.Fatalf("second poll: %+v, want job %s again", pr.Jobs, id)
	}
	if time.Since(start) > 1500*time.Millisecond {
		t.Fatalf("requeue took %v; want shortly after the 400 ms lease", time.Since(start))
	}
	if d := h.srv.Snapshot().Jobs[0].Deliveries; d != 2 {
		t.Fatalf("deliveries = %d, want 2", d)
	}
}

func TestRepeatedReportIsNoOp(t *testing.T) {
	h := newHarness(t, Config{})
	cred, _ := h.pair("r1", "p1")
	id := h.srv.NewJob("r1", "p1", slip())
	h.poll(cred)

	if code := h.outcome(cred, id, api.OutcomeRequest{Kind: "written", PrinterID: "p1"}); code != 204 {
		t.Fatalf("first report: %d", code)
	}
	if code := h.outcome(cred, id, api.OutcomeRequest{Kind: "failed", Reason: "late"}); code != 204 {
		t.Fatalf("repeat report: %d", code)
	}
	j := h.srv.Snapshot().Jobs[0]
	if j.Outcome.Kind != "written" || j.Reports != 2 || j.State != "done" {
		t.Fatalf("job = %+v; want first outcome kept, 2 reports", j)
	}
}

func TestOutcomeNeedsReason(t *testing.T) {
	h := newHarness(t, Config{})
	cred, _ := h.pair("r1", "p1")
	id := h.srv.NewJob("r1", "p1", slip())
	if code := h.outcome(cred, id, api.OutcomeRequest{Kind: "failed"}); code != 400 {
		t.Fatalf("failed without reason: %d, want 400", code)
	}
	if code := h.outcome(cred, id, api.OutcomeRequest{Kind: "nonsense"}); code != 400 {
		t.Fatalf("unknown kind: %d, want 400", code)
	}
}

func TestRevokedCredentialNeedsRepair(t *testing.T) {
	h := newHarness(t, Config{})
	cred, dev := h.pair("r1", "p1")
	h.do("POST", "/mock/devices/"+dev+"/revoke", "", nil)
	_, resp := h.poll(cred)
	if resp.StatusCode != 401 {
		t.Fatalf("poll after revoke: %d, want 401", resp.StatusCode)
	}
}

func TestRotationKeepsOldCredentialUntilNewIsUsed(t *testing.T) {
	h := newHarness(t, Config{Hold: 100 * time.Millisecond})
	old, dev := h.pair("r1", "p1")
	h.do("POST", "/mock/devices/"+dev+"/rotate", "", nil)

	pr, _ := h.poll(old)
	if pr.NewCredential == "" {
		t.Fatal("poll response carried no new credential")
	}
	// Agent "crashes" before storing it: the old credential must still work.
	if _, resp := h.poll(old); resp.StatusCode != 200 {
		t.Fatalf("old credential before first use of new: %d, want 200", resp.StatusCode)
	}
	if _, resp := h.poll(pr.NewCredential); resp.StatusCode != 200 {
		t.Fatalf("new credential: %d", resp.StatusCode)
	}
	_, resp := h.poll(old)
	if resp.StatusCode != 401 {
		t.Fatalf("old credential after new was used: %d, want 401", resp.StatusCode)
	}
}

func TestHealthIsRecorded(t *testing.T) {
	h := newHarness(t, Config{Hold: 50 * time.Millisecond})
	cred, _ := h.pair("r1", "p1")
	h.do("POST", api.PathPoll, cred, api.PollRequest{Health: api.Health{
		AgentVersion: "0.1.0", QueueDepth: 3,
		Printers: []api.PrinterHealth{{ID: "p1", State: "down", LastError: "timeout"}},
	}})
	d := h.srv.Snapshot().Devices[0]
	if d.Health.AgentVersion != "0.1.0" || d.Health.QueueDepth != 3 || d.Health.Printers[0].State != "down" {
		t.Fatalf("health = %+v", d.Health)
	}
}

func TestUnknownFieldsInRequestsAreRejected(t *testing.T) {
	h := newHarness(t, Config{})
	cred, _ := h.pair("r1", "p1")
	// An agent must never send a restaurant id; the server must not accept one.
	resp, _ := h.do("POST", api.PathPoll, cred, map[string]any{"restaurant_id": "someone-else", "health": map[string]any{}})
	if resp.StatusCode != 400 {
		t.Fatalf("poll with restaurant_id: %d, want 400", resp.StatusCode)
	}
}

func TestHostileFlags(t *testing.T) {
	cases := []struct {
		flag  Flag
		check func(t *testing.T, resp *http.Response, body []byte, err error)
	}{
		{IPInJob, func(t *testing.T, _ *http.Response, body []byte, _ error) {
			if !bytes.Contains(body, []byte(`"printer_address":"203.0.113.7:9100"`)) {
				t.Fatalf("no address in job: %s", body)
			}
		}},
		{RawBytes, func(t *testing.T, _ *http.Response, body []byte, _ error) {
			if !bytes.Contains(body, []byte(`"raw":`)) {
				t.Fatalf("no raw bytes in job: %s", body)
			}
		}},
		{HugePayload, func(t *testing.T, _ *http.Response, body []byte, _ error) {
			if len(body) < 50<<20 {
				t.Fatalf("body is %d bytes, want at least 50 MB", len(body))
			}
		}},
		{RepeatID, func(t *testing.T, _ *http.Response, body []byte, _ error) {
			var pr api.PollResponse
			json.Unmarshal(body, &pr)
			if len(pr.Jobs) != 2 || pr.Jobs[0].ID != pr.Jobs[1].ID {
				t.Fatalf("jobs = %+v, want the same job twice", pr.Jobs)
			}
		}},
		{ShortLease, func(t *testing.T, _ *http.Response, body []byte, _ error) {
			var pr api.PollResponse
			json.Unmarshal(body, &pr)
			if pr.LeaseSeconds != 2 {
				t.Fatalf("lease_seconds = %d, want 2", pr.LeaseSeconds)
			}
		}},
		{DropMidway, func(t *testing.T, _ *http.Response, body []byte, err error) {
			if err == nil {
				t.Fatalf("read the whole body (%d bytes); want an unexpected EOF", len(body))
			}
		}},
	}
	for _, tc := range cases {
		t.Run(string(tc.flag), func(t *testing.T) {
			h := newHarness(t, Config{})
			cred, _ := h.pair("r1", "p1")
			h.srv.NewJob("r1", "p1", slip())
			h.srv.SetHostile(tc.flag)

			req, _ := http.NewRequest("POST", h.ts.URL+api.PathPoll, strings.NewReader(`{"health":{}}`))
			req.Header.Set("Authorization", "Bearer "+cred)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, readErr := io.ReadAll(resp.Body)
			tc.check(t, resp, body, readErr)
		})
	}
}

func TestControlEndpoints(t *testing.T) {
	h := newHarness(t, Config{})
	resp, body := h.do("POST", "/mock/pairing-codes", "", map[string]string{"restaurant_id": "r1"})
	var code struct{ Code string }
	json.Unmarshal(body, &code)
	if resp.StatusCode != 200 || len(code.Code) != 9 {
		t.Fatalf("pairing code: %d %s", resp.StatusCode, body)
	}
	resp, body = h.do("POST", "/mock/jobs", "", map[string]any{
		"restaurant_id": "r1",
		"jobs":          []map[string]any{{"printer_id": "p1", "slip": slip()}},
	})
	if resp.StatusCode != 200 || !bytes.Contains(body, []byte("job-000001")) {
		t.Fatalf("new jobs: %d %s", resp.StatusCode, body)
	}
	resp, body = h.do("PUT", "/mock/hostile", "", map[string]any{"flags": []string{"repeat-id", "bogus"}})
	if resp.StatusCode != 400 {
		t.Fatalf("bogus flag: %d %s", resp.StatusCode, body)
	}
	resp, body = h.do("GET", "/mock/state", "", nil)
	if resp.StatusCode != 200 || !bytes.Contains(body, []byte(`"state":"queued"`)) {
		t.Fatalf("state: %d %s", resp.StatusCode, body)
	}
}

func TestParseFlags(t *testing.T) {
	got, err := ParseFlags(" ip-in-job, repeat-id ,")
	if err != nil || fmt.Sprint(got) != "[ip-in-job repeat-id]" {
		t.Fatalf("ParseFlags = %v, %v", got, err)
	}
	if _, err := ParseFlags("nope"); err == nil {
		t.Fatal("want an error for an unknown flag")
	}
}
