package logfile

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRotatesAndKeepsALimitedNumber(t *testing.T) {
	dir := t.TempDir()
	w := &Writer{Path: filepath.Join(dir, "agent.log"), MaxBytes: 100, Keep: 2}
	defer w.Close()
	for i := 0; i < 20; i++ {
		fmt.Fprintf(w, "line %02d: %s\n", i, strings.Repeat("x", 20)) // 30 bytes each
	}
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != 3 {
		t.Fatalf("files = %v; want agent.log plus 2 rotated", names)
	}
	for _, n := range names {
		fi, _ := os.Stat(filepath.Join(dir, n))
		if fi.Size() > 100 {
			t.Fatalf("%s is %d bytes, over the 100-byte limit", n, fi.Size())
		}
	}
	live, _ := os.ReadFile(filepath.Join(dir, "agent.log"))
	if !strings.Contains(string(live), "line 19") {
		t.Fatalf("the newest line isn't in the live file:\n%s", live)
	}
	old, _ := os.ReadFile(filepath.Join(dir, "agent.log.2"))
	if strings.Contains(string(old), "line 00") {
		t.Fatal("the oldest lines should have been dropped")
	}
}

func TestContinuesAnExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.log")
	os.WriteFile(path, []byte("before restart\n"), 0o644)
	w := &Writer{Path: path}
	fmt.Fprintln(w, "after restart")
	w.Close()
	got, _ := os.ReadFile(path)
	if string(got) != "before restart\nafter restart\n" {
		t.Fatalf("log = %q; a restart must append, not truncate", got)
	}
}
