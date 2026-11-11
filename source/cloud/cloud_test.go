package cloud

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/api"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/internal/mockserver"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/job"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/pair"
)

type fixture struct {
	srv    *mockserver.Server
	client *Client
	store  *pair.MemStore
	device string
}

func setup(t *testing.T, hold time.Duration) *fixture {
	t.Helper()
	srv := mockserver.New(mockserver.Config{Hold: hold})
	ts := httptest.NewServer(srv)
	t.Cleanup(func() { ts.Close(); srv.Close() })

	code, _ := srv.NewPairingCode("r1")
	store := &pair.MemStore{}
	dev, err := pair.Redeem(context.Background(), http.DefaultClient, ts.URL, code, "till-1", store)
	if err != nil {
		t.Fatal(err)
	}
	c := &Client{Server: ts.URL, Store: store, Hold: hold,
		Health: func() api.Health { return api.Health{AgentVersion: "test"} }}
	if err := c.RegisterPrinters(context.Background(), []api.Printer{{ID: "p1", Name: "Tandoor", Kind: "network", Width: 80}}); err != nil {
		t.Fatal(err)
	}
	return &fixture{srv: srv, client: c, store: store, device: dev}
}

func slip() api.Slip {
	return api.Slip{Station: "Tandoor", OrderNumber: "1042", Items: []api.Item{{Quantity: 2, Name: "Paneer Tikka"}}}
}

func TestFetchAndReport(t *testing.T) {
	f := setup(t, 5*time.Second)
	go func() {
		time.Sleep(200 * time.Millisecond)
		f.srv.NewJob("r1", "p1", slip())
	}()
	start := time.Now()
	jobs, err := f.client.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].Slip.Items[0].Name != "Paneer Tikka" {
		t.Fatalf("jobs = %+v", jobs)
	}
	if time.Since(start) > time.Second {
		t.Fatalf("Fetch took %v; the long poll should answer as soon as the job exists", time.Since(start))
	}
	if err := f.client.Report(context.Background(), jobs[0].ID, job.Outcome{Kind: job.Written, PrinterID: "p1"}); err != nil {
		t.Fatal(err)
	}
	st := f.srv.Snapshot()
	if st.Jobs[0].Outcome == nil || st.Jobs[0].Outcome.Kind != "written" {
		t.Fatalf("server recorded %+v", st.Jobs[0])
	}
	if st.Devices[0].Health.AgentVersion != "test" {
		t.Fatal("health did not reach the server")
	}
}

func TestFetchEmptyAfterHold(t *testing.T) {
	f := setup(t, 200*time.Millisecond)
	jobs, err := f.client.Fetch(context.Background())
	if err != nil || len(jobs) != 0 {
		t.Fatalf("jobs = %v, err = %v; want none, no error", jobs, err)
	}
}

func TestReportUnknownJob(t *testing.T) {
	f := setup(t, time.Second)
	err := f.client.Report(context.Background(), "../../mock/state", job.Outcome{Kind: job.Written})
	if !errors.Is(err, ErrUnknownJob) {
		t.Fatalf("err = %v, want ErrUnknownJob", err)
	}
}

func TestHostileJobsAreRefusedAndReported(t *testing.T) {
	for _, flag := range []mockserver.Flag{mockserver.IPInJob, mockserver.RawBytes} {
		t.Run(string(flag), func(t *testing.T) {
			f := setup(t, time.Second)
			f.srv.NewJob("r1", "p1", slip())
			f.srv.SetHostile(flag)
			jobs, err := f.client.Fetch(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(jobs) != 0 {
				t.Fatalf("got %d jobs; a job with %s must never reach the queue", len(jobs), flag)
			}
			j := f.srv.Snapshot().Jobs[0]
			if j.Outcome == nil || j.Outcome.Kind != "failed" {
				t.Fatalf("server recorded %+v; want the refusal reported as failed", j)
			}
		})
	}
}

func TestPrinterIDThatIsAnAddressIsRefused(t *testing.T) {
	// The mock only sends jobs for registered printer ids, so check parseJob
	// directly.
	_, err := parseJob([]byte(`{"id":"j1","printer_id":"192.168.1.50:9100","created_at":"2026-11-11T00:00:00Z","slip":{}}`))
	if err == nil {
		t.Fatal("a printer id that is an address was accepted")
	}
}

func TestHugePayloadIsRefused(t *testing.T) {
	f := setup(t, time.Second)
	f.srv.NewJob("r1", "p1", slip())
	f.srv.SetHostile(mockserver.HugePayload)
	if _, err := f.client.Fetch(context.Background()); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
}

func TestDropMidwayIsAnError(t *testing.T) {
	f := setup(t, time.Second)
	f.srv.NewJob("r1", "p1", slip())
	f.srv.SetHostile(mockserver.DropMidway)
	if jobs, err := f.client.Fetch(context.Background()); err == nil {
		t.Fatalf("got %d jobs and no error from half a response", len(jobs))
	}
}

func TestRepeatedIDPrintsOnce(t *testing.T) {
	f := setup(t, time.Second)
	f.srv.NewJob("r1", "p1", slip())
	f.srv.SetHostile(mockserver.RepeatID)
	jobs, err := f.client.Fetch(context.Background())
	if err != nil || len(jobs) != 1 {
		t.Fatalf("jobs = %d, err = %v; want 1", len(jobs), err)
	}
}

func TestRotationStoresThenUses(t *testing.T) {
	f := setup(t, 100*time.Millisecond)
	old, _ := f.store.Load()
	// Rotate through the mock's control endpoint.
	req, _ := http.NewRequest("POST", f.client.Server+"/mock/devices/"+f.device+"/rotate", nil)
	http.DefaultClient.Do(req)

	if _, err := f.client.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored, _ := f.store.Load()
	if stored == old {
		t.Fatal("new credential was not stored")
	}
	// The next poll uses the new credential, which retires the old one.
	if _, err := f.client.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	stale := &Client{Server: f.client.Server, Store: &pair.MemStore{}}
	stale.Store.Save(old)
	if _, err := stale.Fetch(context.Background()); !errors.Is(err, ErrRepairNeeded) {
		t.Fatalf("old credential after rotation: %v, want ErrRepairNeeded", err)
	}
}

func TestRevokedNeedsRepair(t *testing.T) {
	f := setup(t, time.Second)
	req, _ := http.NewRequest("POST", f.client.Server+"/mock/devices/"+f.device+"/revoke", nil)
	http.DefaultClient.Do(req)
	if _, err := f.client.Fetch(context.Background()); !errors.Is(err, ErrRepairNeeded) {
		t.Fatalf("err = %v, want ErrRepairNeeded", err)
	}
}

func TestNotPaired(t *testing.T) {
	c := &Client{Server: "http://127.0.0.1:1", Store: &pair.MemStore{}}
	if _, err := c.Fetch(context.Background()); !errors.Is(err, pair.ErrNotPaired) {
		t.Fatalf("err = %v, want ErrNotPaired", err)
	}
}
