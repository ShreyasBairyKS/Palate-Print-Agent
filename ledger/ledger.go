// Package ledger is the printed-ID ledger: a small DPAPI-protected file on the
// till recording, for every job, whether the agent started writing it and
// what the outcome was. It is what stops a crash from printing twice or
// silently losing a ticket.
//
// The rule:
//
//   - Before writing to a printer, record the job as Attempting and flush.
//   - After the write, whatever the result, record it as Done with its
//     outcome and flush, before reporting the outcome.
//   - A Done job is never printed again; its stored outcome is reported again.
//   - An Attempting job (the agent died mid-write) is never assumed printed:
//     it prints again marked DUPLICATE, with the reason "crash-recovery".
//
// Entries hold job ids, states and outcomes only, never slip contents, and
// are kept for 7 days.
//
// On disk the ledger is an append-only file of length-prefixed records, each
// encrypted on its own, so an append interrupted by a crash damages only the
// last record. It is compacted on open.
package ledger

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/internal/dpapi"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/job"
)

// State is where a job got to.
type State string

const (
	Attempting State = "attempting" // write started; outcome unknown
	Done       State = "done"       // outcome recorded
)

// Entry is one job's record.
type Entry struct {
	ID      string      `json:"id"`
	State   State       `json:"state"`
	Outcome job.Outcome `json:"outcome"`
	At      time.Time   `json:"at"`
}

// Options configures a ledger. Zero values are the production defaults.
type Options struct {
	Retention time.Duration // default 7 days
	Now       func() time.Time
	// Protect and Unprotect default to DPAPI at machine scope.
	Protect   func([]byte) ([]byte, error)
	Unprotect func([]byte) ([]byte, error)
}

var entropy = []byte("palate-print-agent/ledger/v1")

const maxRecord = 64 << 10 // a record is a few hundred bytes; anything larger is damage

// Ledger is safe for use by several printer queues at once.
type Ledger struct {
	path string
	opt  Options

	mu      sync.Mutex
	f       *os.File
	entries map[string]Entry
	records int // records in the file, for deciding when to compact
}

// Open loads the ledger at path (creating it if needed), drops entries past
// retention, and rewrites the file compactly.
func Open(path string, opt Options) (*Ledger, error) {
	if opt.Retention == 0 {
		opt.Retention = 7 * 24 * time.Hour
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Protect == nil {
		opt.Protect = func(b []byte) ([]byte, error) { return dpapi.Protect(b, entropy) }
	}
	if opt.Unprotect == nil {
		opt.Unprotect = func(b []byte) ([]byte, error) { return dpapi.Unprotect(b, entropy) }
	}
	l := &Ledger{path: path, opt: opt, entries: map[string]Entry{}}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := l.load(); err != nil {
		return nil, err
	}
	if err := l.compact(); err != nil {
		return nil, err
	}
	return l, nil
}

// load reads every record. A damaged record is skipped; damage that makes the
// rest unreadable (a torn length prefix or a record cut short) ends the read.
func (l *Ledger) load() error {
	data, err := os.ReadFile(l.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for off := 0; off+4 <= len(data); {
		n := int(binary.BigEndian.Uint32(data[off:]))
		if n == 0 || n > maxRecord || off+4+n > len(data) {
			break // torn tail from a crash mid-append
		}
		blob := data[off+4 : off+4+n]
		off += 4 + n
		plain, err := l.opt.Unprotect(blob)
		if err != nil {
			continue
		}
		var e Entry
		if json.Unmarshal(plain, &e) != nil || e.ID == "" {
			continue
		}
		l.entries[e.ID] = e
	}
	return nil
}

// compact drops expired entries and rewrites the file with one record per
// job, atomically (temp file, then rename).
func (l *Ledger) compact() error {
	if l.f != nil {
		l.f.Close()
		l.f = nil
	}
	cutoff := l.opt.Now().Add(-l.opt.Retention)
	var buf bytes.Buffer
	l.records = 0
	for id, e := range l.entries {
		if e.At.Before(cutoff) {
			delete(l.entries, id)
			continue
		}
		rec, err := l.encode(e)
		if err != nil {
			return err
		}
		buf.Write(rec)
		l.records++
	}
	tmp, err := os.CreateTemp(filepath.Dir(l.path), ".ledger-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), l.path); err != nil {
		return err
	}
	l.f, err = os.OpenFile(l.path, os.O_WRONLY|os.O_APPEND, 0o600)
	return err
}

func (l *Ledger) encode(e Entry) ([]byte, error) {
	plain, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	blob, err := l.opt.Protect(plain)
	if err != nil {
		return nil, fmt.Errorf("ledger: encrypting: %w", err)
	}
	rec := make([]byte, 4+len(blob))
	binary.BigEndian.PutUint32(rec, uint32(len(blob)))
	copy(rec[4:], blob)
	return rec, nil
}

// append writes one record and flushes it to disk before returning.
func (l *Ledger) append(e Entry) error {
	rec, err := l.encode(e)
	if err != nil {
		return err
	}
	if l.f == nil {
		return errors.New("ledger: closed")
	}
	if _, err := l.f.Write(rec); err != nil {
		return fmt.Errorf("ledger: writing: %w", err)
	}
	if err := l.f.Sync(); err != nil {
		return fmt.Errorf("ledger: flushing: %w", err)
	}
	l.entries[e.ID] = e
	l.records++
	if l.records > 2*len(l.entries)+1000 {
		return l.compact()
	}
	return nil
}

// Get returns the job's entry, if the ledger has one.
func (l *Ledger) Get(id string) (Entry, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[id]
	return e, ok
}

// Begin records that a write is about to start. It returns only once the
// record is on disk.
func (l *Ledger) Begin(id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.append(Entry{ID: id, State: Attempting, At: l.opt.Now()})
}

// Finish records a job's outcome. It returns only once the record is on disk.
func (l *Ledger) Finish(id string, o job.Outcome) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.append(Entry{ID: id, State: Done, Outcome: o, At: l.opt.Now()})
}

// Len is the number of jobs in the ledger.
func (l *Ledger) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}

func (l *Ledger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}

var _ io.Closer = (*Ledger)(nil)
