// Package logfile is the service's log: a file that rotates by size, so a
// till left alone for months never fills its disk.
//
// agent.log is the live file. When a write would take it past MaxBytes it is
// renamed agent.log.1 (agent.log.1 becomes .2, and so on), the oldest past
// Keep is deleted, and a fresh agent.log is started.
package logfile

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Writer is safe for concurrent use.
type Writer struct {
	Path     string
	MaxBytes int64 // default 5 MB
	Keep     int   // rotated files kept besides the live one; default 3

	mu   sync.Mutex
	f    *os.File
	size int64
}

func (w *Writer) max() int64 {
	if w.MaxBytes > 0 {
		return w.MaxBytes
	}
	return 5 << 20
}

func (w *Writer) keep() int {
	if w.Keep > 0 {
		return w.Keep
	}
	return 3
}

func (w *Writer) open() error {
	if err := os.MkdirAll(filepath.Dir(w.Path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(w.Path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	w.f, w.size = f, fi.Size()
	return nil
}

func (w *Writer) rotate() error {
	if w.f != nil {
		w.f.Close()
		w.f = nil
	}
	os.Remove(fmt.Sprintf("%s.%d", w.Path, w.keep()))
	for i := w.keep() - 1; i >= 1; i-- {
		os.Rename(fmt.Sprintf("%s.%d", w.Path, i), fmt.Sprintf("%s.%d", w.Path, i+1))
	}
	if err := os.Rename(w.Path, w.Path+".1"); err != nil && !os.IsNotExist(err) {
		return err
	}
	return w.open()
}

// Write appends p, rotating first if p would take the file past MaxBytes.
// A single write larger than MaxBytes still goes in whole.
func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		if err := w.open(); err != nil {
			return 0, err
		}
	}
	if w.size > 0 && w.size+int64(len(p)) > w.max() {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

// Close closes the live file.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}
