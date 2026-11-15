package main_test

// Internet outages: the restaurant's line goes quiet, twice in one evening,
// and comes back. The agent must reconnect on its own, print everything that
// was ordered meanwhile, and print nothing twice.
//
// By default the test runs at a reduced time scale (6 s outages, 1 s polls)
// so it fits in the normal suite. PALATE_REALTIME_OUTAGE=1 runs the real
// thing: two 90-second outages with production timings, about 5 minutes.

import (
	"context"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/internal/fakeprinter"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/internal/mockserver"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/source/cloud"
)

// line is the restaurant's internet connection, between the agent and the
// server. Cutting it makes it go quiet the way a dead line does: connections
// that were open stop carrying anything in either direction and are never
// closed (the agent has to notice on its own), and new connections fail, as
// they do when DNS or the route is gone.
type line struct {
	ln     net.Listener
	target string

	mu    sync.Mutex
	quiet bool
	gen   int // bumped on each cut; connections from an older generation are dead
	conns []net.Conn
}

func newLine(t *testing.T, target string) *line {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l := &line{ln: ln, target: target}
	go l.serve()
	t.Cleanup(l.close)
	return l
}

func (l *line) url() string { return "http://" + l.ln.Addr().String() }

func (l *line) serve() {
	for {
		c, err := l.ln.Accept()
		if err != nil {
			return
		}
		l.mu.Lock()
		if l.quiet {
			l.mu.Unlock()
			c.Close()
			continue
		}
		gen := l.gen
		l.mu.Unlock()
		up, err := net.Dial("tcp", l.target)
		if err != nil {
			c.Close()
			continue
		}
		l.mu.Lock()
		l.conns = append(l.conns, c, up)
		l.mu.Unlock()
		go l.pipe(c, up, gen)
		go l.pipe(up, c, gen)
	}
}

func (l *line) dead(gen int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return gen < l.gen
}

// pipe copies one direction. Once the connection's generation is dead, bytes
// are dropped and an end on one side is not passed on: the far side just
// hears nothing, as on a dead line.
func (l *line) pipe(src, dst net.Conn, gen int) {
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		if n > 0 && !l.dead(gen) {
			dst.Write(buf[:n])
		}
		if err != nil {
			if !l.dead(gen) {
				dst.Close()
			}
			return
		}
	}
}

func (l *line) cut() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.quiet = true
	l.gen++
}

func (l *line) restore() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.quiet = false
}

func (l *line) close() {
	l.ln.Close()
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, c := range l.conns {
		c.Close()
	}
}

type outagePlan struct {
	hold, grace, lease, retryMax time.Duration // agent and server timings
	outage, between              time.Duration
	recoverWithin                time.Duration // restore -> ticket queued meanwhile is printed
}

func TestInternetGoesQuietTwiceAndComesBack(t *testing.T) {
	plan := outagePlan{
		hold: time.Second, grace: time.Second, lease: 2 * time.Second, retryMax: 2 * time.Second,
		outage: 6 * time.Second, between: 3 * time.Second, recoverWithin: 5 * time.Second,
	}
	if os.Getenv("PALATE_REALTIME_OUTAGE") == "1" {
		// Production timings: 25 s polls, 15 s grace, 60 s leases, 10 s
		// retry cap (zero values take the agent's defaults).
		plan = outagePlan{
			hold: 25 * time.Second, lease: 60 * time.Second,
			outage: 90 * time.Second, between: 60 * time.Second, recoverWithin: 15 * time.Second,
		}
	} else if testing.Short() {
		t.Skip("takes about 30 s")
	}

	var cutOnAccept atomic.Pointer[line] // set to cut the line the moment the printer takes a ticket
	r := newRig(t, mockserver.Config{Hold: plan.hold, Lease: plan.lease},
		fakeprinter.Config{Mode: fakeprinter.Normal, Status: fakeprinter.StatusOK,
			OnAccept: func(int) {
				if l := cutOnAccept.Swap(nil); l != nil {
					l.cut()
				}
			}})
	wire := newLine(t, strings.TrimPrefix(r.cfg.Server, "http://"))
	r.cfg.Server = wire.url()

	a := r.agent(t)
	a.RetryMax = plan.retryMax
	cl := a.Cloud.(*cloud.Client)
	cl.Hold, cl.Grace = plan.hold, plan.grace

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { a.Run(ctx); close(stopped) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-stopped:
		case <-time.After(10 * time.Second):
			t.Error("agent did not stop within 10 s of cancel")
		}
	})

	done := func(id string, within time.Duration) {
		t.Helper()
		j := jobFinal(t, r.srv, id, within)
		if j.Outcome.Kind != "printed" {
			t.Fatalf("job %s: outcome %+v, want printed", id, j.Outcome)
		}
	}

	// A normal ticket first.
	done(r.srv.NewJob("demo", "p0", order("5101")), 10*time.Second)

	// Outage 1. The line dies the moment the printer takes ticket 5102, so
	// the ticket prints but its outcome can't reach the server. Ticket 5103
	// is ordered while the line is down (from a captain's phone on mobile
	// data, say) and waits on the server.
	cutOnAccept.Store(wire)
	cut1 := time.Now()
	lost := r.srv.NewJob("demo", "p0", order("5102"))
	time.Sleep(plan.outage / 3)
	during1 := r.srv.NewJob("demo", "p0", order("5103"))
	time.Sleep(time.Until(cut1.Add(plan.outage)))
	wire.restore()
	restored := time.Now()
	done(during1, plan.recoverWithin)
	t.Logf("outage 1 (%v): ticket ordered during it printed %v after the line came back",
		plan.outage, time.Since(restored).Round(10*time.Millisecond))
	// The ticket that printed as the line died: its outcome arrives late
	// (a retried report, or re-reported from the ledger when the server
	// sends it again), and it is not printed a second time.
	done(lost, plan.lease+plan.recoverWithin+20*time.Second)

	time.Sleep(plan.between)

	// Outage 2: a plain one.
	wire.cut()
	time.Sleep(plan.outage / 2)
	during2 := r.srv.NewJob("demo", "p0", order("5104"))
	time.Sleep(plan.outage - plan.outage/2)
	wire.restore()
	restored = time.Now()
	done(during2, plan.recoverWithin)
	t.Logf("outage 2 (%v): ticket ordered during it printed %v after the line came back",
		plan.outage, time.Since(restored).Round(10*time.Millisecond))
	after := time.Now()
	done(r.srv.NewJob("demo", "p0", order("5105")), 5*time.Second)
	t.Logf("first ticket after outage 2 printed in %v", time.Since(after).Round(time.Millisecond))

	select {
	case <-stopped:
		t.Fatal("the agent stopped during the outages; it must keep going on its own")
	default:
	}

	// Every ticket on paper exactly once.
	counts := map[string]int{}
	deadline := time.After(2 * time.Second)
drain:
	for {
		select {
		case c := <-r.conns["p0"]:
			text := ticketText(c.Data)
			for _, n := range []string{"5101", "5102", "5103", "5104", "5105"} {
				if strings.Contains(text, "#"+n) {
					counts[n]++
				}
			}
		case <-deadline:
			break drain
		}
	}
	for _, n := range []string{"5101", "5102", "5103", "5104", "5105"} {
		if counts[n] != 1 {
			t.Errorf("ticket #%s on paper %d times, want exactly 1 (all: %v)", n, counts[n], counts)
		}
	}

	// The agent's health clears once it's back.
	healthDeadline := time.Now().Add(plan.hold + 5*time.Second)
	for {
		h := r.srv.Snapshot().Devices[0].Health
		if h.LastError == "" {
			break
		}
		if time.Now().After(healthDeadline) {
			t.Fatalf("health still reports %q after recovery", h.LastError)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
