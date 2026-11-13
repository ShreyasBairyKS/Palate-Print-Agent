package main_test

// Architecture rules, checked on every `go test ./...`.
//
//   - render opens no socket: it must not depend on net at all.
//   - Nothing below JobSource (queue, ladder, render, printer writers) may
//     depend on net/http. The printer writers need raw TCP, so they may use
//     net, but never HTTP.
//
// Packages that don't exist yet are skipped, so each rule starts biting the
// day its package is created.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const module = "github.com/ShreyasBairyKS/Palate-Print-Agent"

var rules = []struct {
	pkg       string
	forbidden []string
}{
	{"render", []string{"net", "net/http", "crypto/tls"}},
	{"job", []string{"net", "net/http"}},
	{"config", []string{"net/http"}},
	{"ledger", []string{"net", "net/http"}},
	{"internal/dpapi", []string{"net", "net/http"}},
	{"queue", []string{"net/http"}},
	{"ladder", []string{"net/http"}},
	{"printer/net", []string{"net/http"}},
	{"printer/usb", []string{"net/http"}},
}

func TestNothingBelowJobSourceSpeaksHTTP(t *testing.T) {
	for _, r := range rules {
		if _, err := os.Stat(filepath.FromSlash(r.pkg)); err != nil {
			t.Logf("%s: not created yet, skipped", r.pkg)
			continue
		}
		out, err := exec.Command("go", "list", "-deps", "./"+r.pkg).Output()
		if err != nil {
			t.Fatalf("go list %s: %v", r.pkg, err)
		}
		deps := map[string]bool{}
		for _, d := range strings.Fields(string(out)) {
			deps[d] = true
		}
		for _, f := range r.forbidden {
			if deps[f] {
				t.Errorf("%s/%s depends on %s; the architecture rules forbid it", module, r.pkg, f)
			}
		}
	}
}
