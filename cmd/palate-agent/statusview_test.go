package main

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/status"
)

func liveFile(now time.Time, printers ...status.Printer) status.File {
	return status.File{UpdatedAt: now.Add(-2 * time.Second), Till: "FRONT-TILL",
		Server: status.Server{Connected: true, Since: now.Add(-time.Hour), LastOK: now}, Printers: printers}
}

func TestStatusViewAllWorking(t *testing.T) {
	now := time.Now()
	f := liveFile(now,
		status.Printer{ID: "p1", Name: "Tandoor", Kind: "network", State: "up", Checked: now.Add(-20 * time.Second)},
		status.Printer{ID: "p2", Name: "Pass", Kind: "network", State: "up", Checked: now.Add(-3 * time.Second)})
	v := describeStatus(f, nil, "running", now)
	if v.Bad || v.Headline != "All 2 printers working" || v.Detail != "Connected to Palate." || v.Advice != "" {
		t.Fatalf("view = %+v", v)
	}
	if !strings.Contains(v.Lines[0].Text, "Tandoor") || !strings.Contains(v.Lines[0].Text, "checked 20 s ago") {
		t.Fatalf("line = %q", v.Lines[0].Text)
	}
}

func TestStatusViewPrinterDown(t *testing.T) {
	now := time.Now()
	since := now.Add(-5 * time.Minute)
	f := liveFile(now,
		status.Printer{ID: "p1", Name: "Tandoor", Kind: "network", State: "up"},
		status.Printer{ID: "p2", Name: "Pass", Kind: "network", State: "down", LastError: "paper out", Since: since, Backup: "p1"})
	f.QueueDepth = 2
	v := describeStatus(f, nil, "running", now)
	if !v.Bad || v.Headline != "Pass isn't working" || len(v.Down) != 1 {
		t.Fatalf("view = %+v", v)
	}
	l := v.Lines[1]
	if !l.Bad || !strings.Contains(l.Text, "NOT WORKING: out of paper") || !strings.Contains(l.Text, clock(since, now)) || !strings.Contains(l.Text, "go to Tandoor") {
		t.Fatalf("line = %q", l.Text)
	}
	if !strings.Contains(v.Advice, "new paper roll in Pass") || !strings.Contains(v.Detail, "2 tickets waiting") {
		t.Fatalf("advice = %q, detail = %q", v.Advice, v.Detail)
	}

	f.Printers[0] = status.Printer{ID: "p1", Name: "Tandoor", Kind: "network", State: "down", LastError: "hang"}
	v = describeStatus(f, nil, "running", now)
	if v.Headline != "2 printers aren't working" || !strings.Contains(v.Advice, "Tandoor is switched on") {
		t.Fatalf("view = %+v", v)
	}
}

func TestStatusViewNoInternet(t *testing.T) {
	now := time.Now()
	f := liveFile(now, status.Printer{ID: "p1", Name: "Tandoor", Kind: "network", State: "up"})
	f.Server = status.Server{Since: now.Add(-10 * time.Minute), Error: "dial tcp: no route to host"}
	v := describeStatus(f, nil, "running", now)
	if !v.Bad || v.Headline != "Not connected to Palate" || !strings.Contains(v.Detail, "Can't reach Palate since") || !strings.Contains(v.Advice, "internet") {
		t.Fatalf("view = %+v", v)
	}
	// Just started, nothing failed yet: not an alarm.
	f.Server = status.Server{Since: now}
	if v := describeStatus(f, nil, "running", now); v.Bad || v.Detail != "Connecting to Palate..." {
		t.Fatalf("view = %+v", v)
	}
}

func TestStatusViewUSBUnknownIsNotAProblem(t *testing.T) {
	now := time.Now()
	f := liveFile(now, status.Printer{ID: "p1", Name: "Bar", Kind: "usb", State: "unknown"})
	v := describeStatus(f, nil, "running", now)
	if v.Bad || v.Headline != "No problems found" || !strings.Contains(v.Lines[0].Text, "USB") {
		t.Fatalf("view = %+v", v)
	}
}

func TestStatusViewAgentNotRunning(t *testing.T) {
	now := time.Now()
	f := liveFile(now, status.Printer{ID: "p1", Name: "Tandoor", Kind: "network", State: "up"})
	cases := []struct {
		name     string
		f        status.File
		err      error
		svc      string
		headline string
	}{
		{"not installed", status.File{}, os.ErrNotExist, "", "The print agent isn't installed on this PC"},
		{"stopped", f, nil, "stopped", "The print agent isn't running"},
		{"stuck", status.File{UpdatedAt: now.Add(-5 * time.Minute)}, nil, "running", "The print agent isn't responding"},
		{"no file yet", status.File{}, os.ErrNotExist, "running", "Waiting for the print agent to report..."},
		{"old install", status.File{}, os.ErrPermission, "running", "This Windows account can't read the printer status"},
	}
	for _, c := range cases {
		v := describeStatus(c.f, c.err, c.svc, now)
		if v.Headline != c.headline {
			t.Errorf("%s: headline = %q, want %q", c.name, v.Headline, c.headline)
		}
	}
	// Stopped because the credential was revoked: say it needs pairing.
	f.Stopped = "stopped: cloud: credential rejected; re-pair needed"
	v := describeStatus(f, nil, "stopped", now)
	if !v.Bad || !strings.Contains(v.Advice, "pairing") || !strings.Contains(v.Detail, "re-pair needed") {
		t.Fatalf("view = %+v", v)
	}
	if v := describeStatus(status.File{}, errors.New("x"), "starting", now); v.Bad {
		t.Fatal("starting is not a problem")
	}
	// No service, but the agent runs in a terminal and reports: show it.
	f.Stopped = ""
	if v := describeStatus(f, nil, "", now); v.Bad || v.Headline != "The printer is working" {
		t.Fatalf("agent in a terminal: view = %+v", v)
	}
}
