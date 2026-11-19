package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"golang.org/x/sys/windows/svc"
)

// Execute is driven here the way Windows drives it, through its channels,
// so the service's life can be tested without installing anything.

type run struct {
	req   chan svc.ChangeRequest
	st    chan svc.Status
	exit  chan uint32
	stops chan time.Time // when the agent saw its context cancelled
}

func start(t *testing.T, fn func(ctx context.Context) error, budget time.Duration) *run {
	t.Helper()
	r := &run{req: make(chan svc.ChangeRequest), st: make(chan svc.Status, 16), exit: make(chan uint32, 1)}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	go func() {
		_, code := execute(fn, r.req, r.st, log, budget)
		r.exit <- code
	}()
	r.expect(t, svc.StartPending)
	r.expect(t, svc.Running)
	return r
}

func (r *run) expect(t *testing.T, want svc.State) svc.Status {
	t.Helper()
	select {
	case s := <-r.st:
		if s.State != want {
			t.Fatalf("service reported %v, want %v", s.State, want)
		}
		return s
	case <-time.After(5 * time.Second):
		t.Fatalf("service never reported %v", want)
		return svc.Status{}
	}
}

func TestStopFinishesTheAgentCleanly(t *testing.T) {
	finished := make(chan struct{})
	r := start(t, func(ctx context.Context) error {
		<-ctx.Done()
		time.Sleep(300 * time.Millisecond) // finishing the ticket in hand
		close(finished)
		return nil
	}, 5*time.Second)
	r.req <- svc.ChangeRequest{Cmd: svc.Stop}
	if s := r.expect(t, svc.StopPending); s.WaitHint == 0 {
		t.Fatal("stop pending without a wait hint; Windows may give up on the service")
	}
	select {
	case code := <-r.exit:
		if code != 0 {
			t.Fatalf("exit code %d after a clean stop, want 0", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("service did not stop")
	}
	select {
	case <-finished:
	default:
		t.Fatal("the service exited before the agent finished")
	}
}

func TestShutdownIsAStopToo(t *testing.T) {
	r := start(t, func(ctx context.Context) error { <-ctx.Done(); return nil }, 5*time.Second)
	r.req <- svc.ChangeRequest{Cmd: svc.Shutdown}
	r.expect(t, svc.StopPending)
	if code := <-r.exit; code != 0 {
		t.Fatalf("exit code %d, want 0", code)
	}
}

func TestInterrogateAnswersWithCurrentStatus(t *testing.T) {
	r := start(t, func(ctx context.Context) error { <-ctx.Done(); return nil }, 5*time.Second)
	cur := svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	r.req <- svc.ChangeRequest{Cmd: svc.Interrogate, CurrentStatus: cur}
	r.expect(t, svc.Running)
	r.req <- svc.ChangeRequest{Cmd: svc.Stop}
	r.expect(t, svc.StopPending)
	<-r.exit
}

func TestAgentErrorIsAFailureSoWindowsRestartsIt(t *testing.T) {
	r := start(t, func(context.Context) error { return errors.New("re-pair needed") }, 5*time.Second)
	r.expect(t, svc.StopPending)
	if code := <-r.exit; code == 0 {
		t.Fatal("an agent error must exit nonzero so the restart-on-failure settings apply")
	}
}

func TestStuckAgentDoesNotHoldUpWindows(t *testing.T) {
	r := start(t, func(context.Context) error { select {} }, 300*time.Millisecond)
	r.req <- svc.ChangeRequest{Cmd: svc.Stop}
	r.expect(t, svc.StopPending)
	select {
	case <-r.exit:
	case <-time.After(5 * time.Second):
		t.Fatal("service waited forever for a stuck agent")
	}
}
