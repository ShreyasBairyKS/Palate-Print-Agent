// Command mockpalate runs a mock Palate server for the agent to talk to.
//
//	mockpalate                               # 127.0.0.1:8080, 25 s hold, 60 s lease
//	mockpalate -seed demo                    # also print a pairing code for restaurant "demo"
//	mockpalate -hostile repeat-id,ip-in-job  # misbehave from the start
//
// Hostile flags can also be changed while it runs: PUT /mock/hostile.
// Backups for the till-not-answering rule: PUT /mock/backups.
// See the README for the control endpoints.
package main

import (
	"flag"
	"log"
	"net/http"
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
	flag.Parse()

	flags, err := mockserver.ParseFlags(*hostile)
	if err != nil {
		log.Fatal(err)
	}
	srv := mockserver.New(mockserver.Config{Hold: *hold, Lease: *lease, NotAnswering: *notAnswering, MaxDeliveries: *maxDeliveries})
	defer srv.Close()
	srv.SetHostile(flags...)

	if *seed != "" {
		code, exp := srv.NewPairingCode(*seed)
		log.Printf("pairing code for restaurant %q: %s (expires %s)", *seed, code, exp.Format(time.Kitchen))
	}

	log.Printf("mock Palate server on http://%s (hold %s, lease %s, hostile %v)", *addr, *hold, *lease, flags)
	hs := &http.Server{
		Addr:              *addr,
		Handler:           logRequests(srv),
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Fatal(hs.ListenAndServe())
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
