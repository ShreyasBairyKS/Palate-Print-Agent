// Command mockpalate runs a mock Palate server for the agent to talk to.
//
//	mockpalate                               # 127.0.0.1:8080, 25 s hold, 60 s lease
//	mockpalate -seed demo                    # also print a pairing code for restaurant "demo"
//	mockpalate -hostile repeat-id,ip-in-job  # misbehave from the start
//	mockpalate -printer-control p-tandoor=http://127.0.0.1:19190   # demo page controls that fake printer
//
// The demo page is at http://127.0.0.1:8080/ (see scripts/demo-live.sh).
//
// Hostile flags can also be changed while it runs: PUT /mock/hostile.
// Backups for the till-not-answering rule: PUT /mock/backups.
// See the README for the control endpoints.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/internal/mockserver"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "address to listen on")
	hold := flag.Duration("hold", 25*time.Second, "how long a poll is held with no jobs")
	lease := flag.Duration("lease", 60*time.Second, "how long a fetched job is leased")
	hostile := flag.String("hostile", "", "comma-separated hostile flags: "+flagList())
	seed := flag.String("seed", "", "print a pairing code for this restaurant id at startup")
	notAnswering := flag.Duration("not-answering", 0, "no poll for this long and a till's jobs move to backups (default 2 x hold + 10s)")
	maxDeliveries := flag.Int("max-deliveries", 5, "deliveries without an outcome before the server records a job as failed")
	printerControl := flag.String("printer-control", "", "demo page: comma-separated id=url pairs for fake printer control APIs (loopback only)")
	flag.Parse()

	flags, err := mockserver.ParseFlags(*hostile)
	if err != nil {
		log.Fatal(err)
	}
	controls, err := parseControls(*printerControl)
	if err != nil {
		log.Fatal(err)
	}
	srv := mockserver.New(mockserver.Config{Hold: *hold, Lease: *lease, NotAnswering: *notAnswering, MaxDeliveries: *maxDeliveries, PrinterControl: controls})
	defer srv.Close()
	srv.SetHostile(flags...)

	if *seed != "" {
		code, exp := srv.NewPairingCode(*seed)
		log.Printf("pairing code for restaurant %q: %s (expires %s)", *seed, code, exp.Format(time.Kitchen))
	}

	log.Printf("mock Palate server on http://%s (hold %s, lease %s, hostile %v)", *addr, *hold, *lease, flags)
	log.Printf("demo page: http://%s/", *addr)
	hs := &http.Server{
		Addr:              *addr,
		Handler:           logRequests(srv),
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Fatal(hs.ListenAndServe())
}

// parseControls reads "id=url,id=url". Only loopback http URLs are accepted:
// the mock server forwards to them, and it must never reach anything else.
func parseControls(s string) (map[string]string, error) {
	out := map[string]string{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, raw, ok := strings.Cut(part, "=")
		u, err := url.Parse(raw)
		if !ok || id == "" || err != nil || u.Scheme != "http" {
			return nil, fmt.Errorf("-printer-control: %q is not id=http://127.0.0.1:port", part)
		}
		if ip, err := netip.ParseAddr(u.Hostname()); err != nil || !ip.IsLoopback() {
			return nil, fmt.Errorf("-printer-control: %q must point at a loopback address", part)
		}
		out[id] = raw
	}
	return out, nil
}

func flagList() string {
	var names []string
	for _, f := range mockserver.Flags {
		names = append(names, string(f))
	}
	return strings.Join(names, ", ")
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Hijack passes through so the drop-midway flag still works behind the logger.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func logRequests(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		h.ServeHTTP(rec, r)
		log.Printf("%s %s -> %d (%s)", r.Method, r.URL.Path, rec.status, time.Since(start).Round(time.Millisecond))
	})
}
