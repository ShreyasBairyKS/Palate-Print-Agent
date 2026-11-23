// Package status is the till's status file: what the running agent last saw
// of its printers and of Palate's server. The status window and the status
// command read it, so restaurant staff can see that a printer is down
// without opening the Palate portal or the log.
//
// It holds no secrets and no ticket contents: printer names, addresses and
// states, the server connection, and how many tickets are waiting. Every
// local user may read it (and only read it), so the status window needs no
// administrator rights; everything else in the data folder stays closed.
//
// Nothing here may use the network (arch_test.go checks).
package status

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/internal/fileacl"
)

// FileName is the status file's name. It sits beside agent.json.
const FileName = "status.json"

// PathFor is the status file for a config file.
func PathFor(cfgPath string) string { return filepath.Join(filepath.Dir(cfgPath), FileName) }

// Every is how often a running agent rewrites the file even when nothing has
// changed. StaleAfter is how old a file may be before readers treat the agent
// as not running (or stuck): well over Every, so a slow disk isn't an alarm.
const (
	Every      = 10 * time.Second
	StaleAfter = 45 * time.Second
)

// maxSize caps what Read accepts; a real file is a few kilobytes.
const maxSize = 1 << 20

// File is the whole status file.
type File struct {
	UpdatedAt    time.Time `json:"updated_at"`
	StartedAt    time.Time `json:"started_at"`
	AgentVersion string    `json:"agent_version"`
	Till         string    `json:"till"`
	ServerURL    string    `json:"server_url,omitempty"` // Palate's server address (no secret)
	Server       Server    `json:"server"`
	QueueDepth   int       `json:"queue_depth"`
	Printers     []Printer `json:"printers"`
	// Stopped is set when the agent stops: "stopped", or why it had to.
	Stopped string `json:"stopped,omitempty"`
}

// Server is the connection to Palate's server.
type Server struct {
	Connected bool      `json:"connected"`        // the last call to the server worked
	Since     time.Time `json:"since"`            // when Connected last changed (the start, at first)
	LastOK    time.Time `json:"last_ok,omitzero"` // the last call that worked
	Error     string    `json:"error,omitempty"`  // why the last call failed, while not connected
}

// Printer is one printer on this till.
type Printer struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`  // "network" or "usb"
	Where     string    `json:"where"` // its address, or its Windows printer name
	Width     int       `json:"width"`
	Dots      int       `json:"dots,omitempty"`   // dots across, when not the paper's usual
	Backup    string    `json:"backup,omitempty"` // the backup printer's id
	State     string    `json:"state"`            // "up", "down" or "unknown"
	LastError string    `json:"last_error,omitempty"`
	Since     time.Time `json:"since"`            // when State last changed
	Checked   time.Time `json:"checked,omitzero"` // the last print or check that confirmed State
}

// Write replaces the file in one step, so a reader never sees half of it.
// The new file gets its access list (read-only for local users) before it
// takes the old one's place.
func Write(path string, f File) error {
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	os.Remove(tmp)
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("status: %w", err)
	}
	if err := fileacl.ReadableByLocalUsers(tmp); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	_, err = out.Write(append(data, '\n'))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
		return fmt.Errorf("status: %w", err)
	}
	return nil
}

// Read loads the file. A missing file is os.ErrNotExist (the agent hasn't
// run yet); a file closed to this user is os.ErrPermission.
func Read(path string) (File, error) {
	in, err := os.Open(path)
	if err != nil {
		return File{}, err
	}
	defer in.Close()
	data, err := io.ReadAll(io.LimitReader(in, maxSize+1))
	if err != nil {
		return File{}, err
	}
	if len(data) > maxSize {
		return File{}, errors.New("status: file too large")
	}
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return File{}, fmt.Errorf("status: %w", err)
	}
	return f, nil
}

// Stale reports whether the file is too old to describe a running agent.
func (f File) Stale(now time.Time) bool { return now.Sub(f.UpdatedAt) > StaleAfter }
