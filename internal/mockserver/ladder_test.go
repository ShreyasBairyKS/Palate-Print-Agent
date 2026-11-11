package mockserver

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/api"
)

func jobOf(h *harness, id string) JobState {
	for _, j := range h.srv.Snapshot().Jobs {
		if j.ID == id {
			return j
		}
	}
	return JobState{}
}

// pollQuiet polls without touching the test, for background goroutines that
// may still be polling as the test server shuts down.
func (h *harness) pollQuiet(cred string) []api.Job {
	req, _ := http.NewRequest("POST", h.ts.URL+api.PathPoll, strings.NewReader(`{"health":{}}`))
	req.Header.Set("Authorization", "Bearer "+cred)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var pr api.PollResponse
	json.NewDecoder(resp.Body).Decode(&pr)
	return pr.Jobs
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestPoisonJobIsRecordedFailedByServer(t *testing.T) {
	h := newHarness(t, Config{Hold: 50 * time.Millisecond, Lease: 100 * time.Millisecond, MaxDeliveries: 3})
	cred, _ := h.pair("r1", "p1")
	id := h.srv.NewJob("r1", "p1", slip())
	// The agent fetches it three times and never reports.
	for i := 0; i < 3; i++ {
		waitFor(t, "the job to be deliverable", func() bool { return jobOf(h, id).State == "queued" })
		if pr, _ := h.poll(cred); len(pr.Jobs) != 1 {
			t.Fatalf("delivery %d: got %d jobs", i+1, len(pr.Jobs))
		}
	}
	waitFor(t, "the server to give up", func() bool { return jobOf(h, id).State == "done" })
	j := jobOf(h, id)
	if j.Outcome.Kind != "failed" || j.RecordedBy != "server" || !strings.Contains(j.Outcome.Reason, "3 times") {
		t.Fatalf("job = %+v; want failed, recorded by the server after 3 deliveries", j)
	}
	if len(h.srv.Snapshot().Alerts) == 0 {
		t.Fatal("no alert raised")
	}
}

// twoTills pairs two devices in one restaurant: till A owns p1, till B owns p2.
func twoTills(t *testing.T, cfg Config) (h *harness, credA, credB, devA, devB string) {
	t.Helper()
	h = newHarness(t, cfg)
	credA, devA = h.pair("r1", "p1")
	credB, devB = h.pair("r1", "p2")
	return
}

func TestSilentTillsJobsMoveToBackup(t *testing.T) {
	h, credA, credB, devA, devB := twoTills(t, Config{Hold: 50 * time.Millisecond, NotAnswering: 300 * time.Millisecond})
	h.srv.SetBackups(map[string]string{"p1": "p2"})
	h.poll(credA) // till A is seen once, then goes silent

	// Till B keeps polling and passes on what it receives; till A stops.
	received := make(chan api.Job, 8)
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				for _, pj := range h.pollQuiet(credB) {
					received <- pj
				}
			}
		}
	}()
	defer close(stop)

	time.Sleep(400 * time.Millisecond)
	id := h.srv.NewJob("r1", "p1", slip())
	select {
	case got := <-received:
		if got.ID != id || got.PrinterID != "p2" || got.Slip.MovedFrom != "Tandoor" {
			t.Fatalf("till B got %+v; want the job on p2 with MovedFrom = the original printer name", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("till B never received the moved job")
	}
	if j := jobOf(h, id); j.MovedFrom != "p1" || j.MovedTill != devA {
		t.Fatalf("job = %+v; want it recorded as moved from p1 on %s", j, devA)
	}
	_ = devB
}

func TestSilentTillWithoutBackupIsRecordedFailure(t *testing.T) {
	h, credA, _, _, _ := twoTills(t, Config{Hold: 50 * time.Millisecond, NotAnswering: 200 * time.Millisecond})
	h.poll(credA)
	id := h.srv.NewJob("r1", "p1", slip())
	waitFor(t, "the server to record a failure", func() bool { return jobOf(h, id).State == "done" })
	j := jobOf(h, id)
	if j.Outcome.Kind != "failed" || j.RecordedBy != "server" || !strings.Contains(j.Outcome.Reason, "not answering") {
		t.Fatalf("job = %+v", j)
	}
}

func TestLiveLeaseIsNeverMoved(t *testing.T) {
	h, credA, credB, _, _ := twoTills(t, Config{Hold: 50 * time.Millisecond, Lease: 2 * time.Second, NotAnswering: 200 * time.Millisecond})
	h.srv.SetBackups(map[string]string{"p1": "p2"})
	h.poll(credB)
	id := h.srv.NewJob("r1", "p1", slip())
	if pr, _ := h.poll(credA); len(pr.Jobs) != 1 { // A takes the lease, then goes silent
		t.Fatal("till A did not get the job")
	}
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				h.pollQuiet(credB)
			}
		}
	}()
	defer close(stop)
	time.Sleep(800 * time.Millisecond) // A is "not answering", but its lease is live
	if j := jobOf(h, id); j.MovedFrom != "" {
		t.Fatalf("job moved while its lease was live: %+v", j)
	}
	waitFor(t, "the move after the lease ends", func() bool { return jobOf(h, id).MovedFrom == "p1" })
}

func TestLateReportFromOriginalTillRaisesAlert(t *testing.T) {
	h, credA, credB, _, _ := twoTills(t, Config{Hold: 50 * time.Millisecond, Lease: 200 * time.Millisecond, NotAnswering: 200 * time.Millisecond})
	h.srv.SetBackups(map[string]string{"p1": "p2"})
	id := h.srv.NewJob("r1", "p1", slip())
	h.poll(credA) // A takes it, prints, and loses its connection before reporting
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				h.pollQuiet(credB)
			}
		}
	}()
	waitFor(t, "the move", func() bool { return jobOf(h, id).MovedFrom == "p1" })
	waitFor(t, "till B's delivery", func() bool { return jobOf(h, id).Deliveries >= 2 })
	close(stop)
	h.outcome(credB, id, api.OutcomeRequest{Kind: "printed", PrinterID: "p2"})
	// Till A comes back and reports what it printed before the move.
	h.outcome(credA, id, api.OutcomeRequest{Kind: "written", PrinterID: "p1"})

	j := jobOf(h, id)
	if j.Outcome.Kind != "printed" || j.Outcome.PrinterID != "p2" || j.Late == nil || j.Late.PrinterID != "p1" {
		t.Fatalf("job = %+v; want both outcomes recorded", j)
	}
	found := false
	for _, a := range h.srv.Snapshot().Alerts {
		if strings.Contains(a, "check for a duplicate") {
			found = true
		}
	}
	if !found {
		t.Fatalf("alerts = %q; want a duplicate warning", h.srv.Snapshot().Alerts)
	}
}
