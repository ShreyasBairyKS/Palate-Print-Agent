package main_test

// palate-agent printer, run as the real command against fake printers.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/config"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/internal/fakeprinter"
)

func runAgentCmd(t *testing.T, exe string, args ...string) (string, error) {
	t.Helper()
	out, err := exec.Command(exe, args...).CombinedOutput()
	return string(out), err
}

func TestPrinterCommandChangesPrintersLocally(t *testing.T) {
	exe := buildAgent(t)
	dir := t.TempDir()
	first := spyPrinter(t, fakeprinter.Config{})
	cfgPath := writeConfig(t, dir, "https://palate.example", first.addr, "200ms")

	// Move the printer to a new address: test-printed there, then saved.
	moved := spyPrinter(t, fakeprinter.Config{})
	out, err := runAgentCmd(t, exe, "printer", "set", "p0", "-address", moved.addr, "-name", "Grill", "-no-restart", "-config", cfgPath)
	if err != nil {
		t.Fatalf("set: %v\n%s", err, out)
	}
	select {
	case <-moved.accepted:
	default:
		t.Fatal("no test print reached the printer at its new address")
	}
	c, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if p := c.Find("p0"); p.Address != moved.addr || p.Name != "Grill" {
		t.Fatalf("saved printer = %+v", p)
	}
	if _, err := os.Stat(cfgPath + ".previous"); err != nil {
		t.Fatal("the old config wasn't kept")
	}
	log, _ := os.ReadFile(filepath.Join(dir, "logs", "config-changes.log"))
	if !strings.Contains(string(log), "p0 (Tandoor)") || !strings.Contains(string(log), "p0 (Grill)") {
		t.Fatalf("change log = %q", log)
	}

	// Add a second printer and make it the first one's backup.
	pass := spyPrinter(t, fakeprinter.Config{})
	if out, err := runAgentCmd(t, exe, "printer", "add", "p-pass", "-name", "Pass", "-address", pass.addr, "-width", "58", "-no-restart", "-config", cfgPath); err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	if out, err := runAgentCmd(t, exe, "printer", "set", "p0", "-backup", "p-pass", "-no-restart", "-config", cfgPath); err != nil {
		t.Fatalf("set backup: %v\n%s", err, out)
	}
	out, err = runAgentCmd(t, exe, "printer", "remove", "p-pass", "-no-restart", "-config", cfgPath)
	if err == nil || !strings.Contains(out, "backup") {
		t.Fatalf("removing someone's backup should be refused: %v\n%s", err, out)
	}
	if out, err := runAgentCmd(t, exe, "printer", "set", "p0", "-backup", "none", "-no-restart", "-config", cfgPath); err != nil {
		t.Fatalf("clear backup: %v\n%s", err, out)
	}
	if out, err := runAgentCmd(t, exe, "printer", "remove", "p-pass", "-no-restart", "-config", cfgPath); err != nil {
		t.Fatalf("remove: %v\n%s", err, out)
	}
	out, _ = runAgentCmd(t, exe, "printer", "list", "-config", cfgPath)
	if !strings.Contains(out, "p0 (Grill)") || strings.Contains(out, "p-pass") {
		t.Fatalf("list = %s", out)
	}
}

func TestPrinterCommandRefusesBadChanges(t *testing.T) {
	exe := buildAgent(t)
	dir := t.TempDir()
	fp := spyPrinter(t, fakeprinter.Config{})
	cfgPath := writeConfig(t, dir, "https://palate.example", fp.addr, "200ms")
	before, _ := os.ReadFile(cfgPath)

	for name, args := range map[string][]string{
		"public IP":       {"set", "p0", "-address", "8.8.8.8"},
		"hostname":        {"set", "p0", "-address", "printer.local"},
		"bad width":       {"set", "p0", "-width", "70"},
		"unknown printer": {"set", "p9", "-name", "X"},
		"nothing to do":   {"set", "p0"},
		"address-like id": {"add", "192.168.1.9", "-name", "X", "-address", fp.addr},
		"duplicate id":    {"add", "p0", "-name", "X", "-address", fp.addr},
		"last printer":    {"remove", "p0"},
		"both kinds":      {"set", "p0", "-address", fp.addr, "-usb", "EPSON"},
		// Nothing listens here, so the test print fails and nothing is saved.
		"dead printer": {"set", "p0", "-address", "127.0.0.1:1"},
	} {
		args = append(append([]string{"printer"}, args...), "-no-restart", "-config", cfgPath)
		if out, err := runAgentCmd(t, exe, args...); err == nil {
			t.Errorf("%s: accepted\n%s", name, out)
		}
	}
	if after, _ := os.ReadFile(cfgPath); string(after) != string(before) {
		t.Fatal("a refused change touched the config")
	}

	// -no-test saves a printer that's off right now.
	if out, err := runAgentCmd(t, exe, "printer", "set", "p0", "-address", "127.0.0.1:1", "-no-test", "-no-restart", "-config", cfgPath); err != nil {
		t.Fatalf("-no-test: %v\n%s", err, out)
	}
}

func TestInitCreatesTheConfig(t *testing.T) {
	exe := buildAgent(t)
	fp := spyPrinter(t, fakeprinter.Config{})
	cfgPath := filepath.Join(t.TempDir(), "PalatePrintAgent", "agent.json")

	if out, err := runAgentCmd(t, exe, "init", "p-tandoor", "-name", "Tandoor", "-address", fp.addr, "-config", cfgPath); err == nil {
		t.Fatalf("init without -server was accepted\n%s", out)
	}
	if out, err := runAgentCmd(t, exe, "init", "p-tandoor", "-server", "http://palate.example", "-name", "Tandoor", "-address", fp.addr, "-config", cfgPath); err == nil {
		t.Fatalf("init with a plain-http server was accepted\n%s", out)
	}
	out, err := runAgentCmd(t, exe, "init", "p-tandoor", "-server", "https://palate.example", "-name", "Tandoor", "-address", fp.addr, "-config", cfgPath)
	if err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	select {
	case <-fp.accepted:
	default:
		t.Fatal("init didn't test-print the first printer")
	}
	c, err := config.Load(cfgPath)
	if err != nil || c.Server != "https://palate.example" || c.Find("p-tandoor") == nil {
		t.Fatalf("created config: %+v, %v", c, err)
	}
	if out, err := runAgentCmd(t, exe, "init", "p-x", "-server", "https://palate.example", "-name", "X", "-address", fp.addr, "-config", cfgPath); err == nil {
		t.Fatalf("a second init overwrote the config\n%s", out)
	}
	if out, err := runAgentCmd(t, exe, "printer", "set", "p-tandoor", "-server", "https://evil.example", "-config", cfgPath); err == nil {
		t.Fatalf("printer set changed the server\n%s", out)
	}
}
