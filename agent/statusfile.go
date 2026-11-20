package agent

import (
	"bytes"
	"encoding/json"
	"time"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/status"
)

// statusTick is how often the agent looks for a change to write.
const statusTick = 2 * time.Second

// Status is what the agent would write to its status file now.
func (a *Agent) Status() status.File {
	a.mu.Lock()
	conn := a.conn
	started := a.started
	a.mu.Unlock()
	f := status.File{UpdatedAt: time.Now(), StartedAt: started, AgentVersion: Version, Till: a.Till,
		Server: conn, QueueDepth: a.Queue.Depth()}
	states := map[string]int{}
	qs := a.Queue.States()
	for i, s := range qs {
		states[s.ID] = i
	}
	for _, p := range a.cfgs {
		sp := status.Printer{ID: p.ID, Name: p.Name, Kind: p.Kind, Where: p.Address, Width: p.Width, Backup: p.Backup, State: "unknown"}
		if p.Kind == "usb" {
			sp.Where = p.PrinterName
		}
		if i, ok := states[p.ID]; ok {
			s := qs[i]
			sp.State, sp.LastError, sp.Since, sp.Checked = s.State, s.LastError, s.Since, s.Checked
		}
		f.Printers = append(f.Printers, sp)
	}
	return f
}

// startStatus writes the status file every few seconds while the agent runs:
// at once when something changes, and every status.Every regardless, so a
// reader can tell a running agent from one that has stopped. The returned
// function stops it and writes a last copy saying the agent stopped, and why.
func (a *Agent) startStatus() func(err error) {
	now := time.Now()
	a.mu.Lock()
	a.started = now
	if a.conn.Since.IsZero() {
		a.conn.Since = now
	}
	a.mu.Unlock()
	if a.StatusFile == "" {
		return func(error) {}
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	var last []byte
	var lastWrite time.Time
	failing := false
	write := func(f status.File) {
		if err := status.Write(a.StatusFile, f); err != nil {
			if !failing {
				a.Log.Warn("couldn't write the status file; the status window won't be up to date", "path", a.StatusFile, "err", err)
			}
			failing = true
			return
		}
		failing = false
		lastWrite = f.UpdatedAt
	}
	go func() {
		defer close(done)
		t := time.NewTicker(statusTick)
		defer t.Stop()
		for {
			f := a.Status()
			key := f
			key.UpdatedAt = time.Time{}
			k, _ := json.Marshal(key)
			if !bytes.Equal(k, last) || time.Since(lastWrite) >= status.Every || failing {
				write(f)
				last = k
			}
			select {
			case <-stop:
				return
			case <-t.C:
			}
		}
	}()
	return func(err error) {
		close(stop)
		<-done
		f := a.Status()
		f.Stopped = "stopped"
		if err != nil {
			f.Stopped = "stopped: " + err.Error()
		}
		write(f)
	}
}
