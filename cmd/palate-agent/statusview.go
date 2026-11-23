package main

// What the status window and "palate-agent status" say about the till, in
// words for restaurant staff. Built from the status file the running agent
// writes (package status) and the Windows service's state.

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/status"
)

type statusView struct {
	Bad      bool   // something needs someone's attention
	Headline string // one line: "All 3 printers working", "Pass isn't working"
	Detail   string // the connection to Palate, and tickets waiting
	Lines    []statusLine
	Advice   string // what to do about it, if anything
	Down     []string
}

type statusLine struct {
	ID   string
	Text string
	Bad  bool
}

// describeStatus turns what the agent last wrote into words. svc is the
// service's state ("running", "stopped", ...), "" when it isn't installed,
// or "unknown" when it can't be asked (then the file alone decides);
// readErr is from reading the status file.
func describeStatus(f status.File, readErr error, svc string, now time.Time) statusView {
	fresh := readErr == nil && f.Stopped == "" && !f.Stale(now)
	switch {
	case svc == "" && fresh:
		// No service, but an agent is running in a terminal: show it.
	case svc == "":
		return statusView{Bad: true, Headline: "The print agent isn't installed on this PC",
			Advice: "Nothing prints from Palate on this till. Ask your manager to set it up: double-click palate-agent.exe and choose Yes when Windows asks."}
	case svc == "starting":
		return statusView{Headline: "The print agent is starting...", Detail: "This takes a few seconds."}
	case svc != "running" && svc != "unknown":
		v := statusView{Bad: true, Headline: "The print agent isn't running",
			Advice: "Nothing prints from Palate on this till until it runs again. Restarting the till starts it; if it stops again, call your manager or Palate support."}
		if readErr == nil && strings.HasPrefix(f.Stopped, "stopped: ") {
			v.Detail = "It stopped " + when(f.UpdatedAt, now) + ": " + strings.TrimPrefix(f.Stopped, "stopped: ")
			if strings.Contains(f.Stopped, "re-pair") || strings.Contains(f.Stopped, "unauthorized") || strings.Contains(f.Stopped, "not paired") {
				v.Advice = "This till needs pairing with Palate again. Ask your manager: open setup, and type a new pairing code from the Palate portal."
			}
		}
		return v
	case errors.Is(readErr, os.ErrNotExist):
		return statusView{Headline: "Waiting for the print agent to report...", Detail: "It writes its status a few seconds after it starts."}
	case errors.Is(readErr, os.ErrPermission):
		return statusView{Bad: true, Headline: "This Windows account can't read the printer status",
			Advice: "The print agent is running. To see its printers, ask your manager to reinstall it with this version, which lets every account read the status."}
	case readErr != nil:
		return statusView{Bad: true, Headline: "The printer status can't be read", Detail: readErr.Error()}
	case f.Stopped != "" || f.Stale(now):
		return statusView{Bad: true, Headline: "The print agent isn't responding",
			Detail: "It last reported " + when(f.UpdatedAt, now) + ".",
			Advice: "Tickets may not print. Restart the till; if this comes back, call your manager or Palate support."}
	}

	v := statusView{}
	names := map[string]string{}
	for _, p := range f.Printers {
		names[p.ID] = p.Name
	}
	var advice []string
	up := 0
	for _, p := range f.Printers {
		l := statusLine{ID: p.ID}
		switch p.State {
		case "up":
			up++
			l.Text = "✓   " + p.Name + "   —   working"
			if !p.Checked.IsZero() {
				l.Text += " (checked " + ago(p.Checked, now) + ")"
			}
		case "down":
			l.Bad = true
			v.Down = append(v.Down, p.Name)
			why := p.LastError
			l.Text = "✗   " + p.Name + "   —   NOT WORKING: " + plainWhy(why) + ", since " + clock(p.Since, now)
			if b := names[p.Backup]; b != "" {
				l.Text += ". Its tickets go to " + b
			}
			if why != "" {
				l.Text += "   (error code: " + why + ")"
			}
			advice = append(advice, fix(p.Name, why))
		default:
			l.Text = "•   " + p.Name + "   —   "
			if p.Kind == "usb" {
				l.Text += "not checked yet; USB printers show their state once they print"
			} else {
				l.Text += "checking..."
			}
		}
		v.Lines = append(v.Lines, l)
	}

	switch {
	case f.Server.Connected:
		v.Detail = "Connected to Palate."
	case f.Server.Error == "":
		v.Detail = "Connecting to Palate..."
	default:
		v.Bad = true
		v.Detail = "Can't reach Palate since " + clock(f.Server.Since, now) + ". New orders print once the connection is back."
		advice = append(advice, explainText(f.Server.Error).String())
	}
	switch n := f.QueueDepth; {
	case n == 1:
		v.Detail += " 1 ticket waiting to print."
	case n > 1:
		v.Detail += fmt.Sprintf(" %d tickets waiting to print.", n)
	}

	switch {
	case len(v.Down) == 1:
		v.Bad = true
		v.Headline = v.Down[0] + " isn't working"
	case len(v.Down) > 1:
		v.Bad = true
		v.Headline = fmt.Sprintf("%d printers aren't working", len(v.Down))
	case !f.Server.Connected && f.Server.Error != "":
		v.Headline = "Not connected to Palate"
	case up == len(f.Printers) && up == 1:
		v.Headline = "The printer is working"
	case up == len(f.Printers):
		v.Headline = fmt.Sprintf("All %d printers working", up)
	default:
		v.Headline = "No problems found"
	}
	if len(v.Down) > 0 {
		advice = append(advice, "A printer starts printing again by itself within a minute of being fixed.")
	}
	v.Advice = strings.Join(advice, " ")
	return v
}

// plainWhy says why a printer is down in words for staff, from the error
// class the agent records.
func plainWhy(why string) string {
	switch why {
	case "paper out":
		return "out of paper"
	case "refused":
		return "can't connect to it"
	case "hang":
		return "not answering"
	case "partial write":
		return "stopped in the middle of a ticket"
	case "":
		return "not answering"
	}
	return why
}

// fix says what to check on a down printer, by why it's down.
func fix(name, why string) string {
	switch why {
	case "paper out":
		return "Put a new paper roll in " + name + "."
	case "partial write":
		return "Check " + name + ": a ticket may have been cut off, and it isn't answering."
	}
	return "Check " + name + " is switched on and its network cable is in."
}

// clock is a time of day, with the date when it isn't today.
func clock(t, now time.Time) string {
	t, now = t.Local(), now.Local()
	s := strings.ToLower(t.Format("3:04 PM"))
	if y, m, d := t.Date(); y != now.Year() || m != now.Month() || d != now.Day() {
		s = t.Format("2 Jan") + " " + s
	}
	return s
}

// ago is how long ago, roughly.
func ago(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < 5*time.Second:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%d s ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	}
	return "at " + clock(t, now)
}

// when is "3 min ago" for recent times and a clock time for older ones.
func when(t, now time.Time) string {
	if now.Sub(t) < time.Hour {
		return ago(t, now)
	}
	return "at " + clock(t, now)
}

// printStatusView writes the view for the console.
func printStatusView(v statusView) {
	mark := "OK"
	if v.Bad {
		mark = "PROBLEM"
	}
	fmt.Printf("\n%s: %s\n", mark, v.Headline)
	if v.Detail != "" {
		fmt.Println("  " + v.Detail)
	}
	for _, l := range v.Lines {
		fmt.Println("  " + l.Text)
	}
	if v.Advice != "" {
		fmt.Println("  " + v.Advice)
	}
}
