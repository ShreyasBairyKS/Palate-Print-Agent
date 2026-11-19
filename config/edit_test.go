package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const twoPrinters = `{
  "server": "https://palate.example",
  "printers": [
    {"id": "p-tandoor", "name": "Tandoor", "kind": "network", "address": "192.168.1.50", "width": 80, "backup": "p-pass"},
    {"id": "p-pass", "name": "Pass", "kind": "network", "address": "192.168.1.51", "width": 58}
  ]
}`

func writeFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSaveWritesOnlyWhatWasWrittenAndKeepsThePrevious(t *testing.T) {
	path := writeFile(t, twoPrinters)
	c, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	c.Find("p-tandoor").Address = "192.168.1.60"
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	saved, _ := os.ReadFile(path)
	for _, unwanted := range []string{"timings", "credential_file", "ledger_file", "html_work_dir", "agent_name", "connect_timeout", ":9100"} {
		if strings.Contains(string(saved), unwanted) {
			t.Errorf("saved config gained a default it never had: %q\n%s", unwanted, saved)
		}
	}
	if !strings.Contains(string(saved), "192.168.1.60") {
		t.Fatal("the change wasn't saved")
	}
	prev, err := os.ReadFile(path + ".previous")
	if err != nil || string(prev) != twoPrinters {
		t.Fatalf("the previous file wasn't kept as it was: %v", err)
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("the saved config doesn't load: %v", err)
	}
}

func TestSaveRefusesABrokenConfig(t *testing.T) {
	path := writeFile(t, twoPrinters)
	for name, change := range map[string]func(*Config){
		"public IP":       func(c *Config) { c.Find("p-pass").Address = "8.8.8.8" },
		"hostname":        func(c *Config) { c.Find("p-pass").Address = "printer.local" },
		"bad width":       func(c *Config) { c.Find("p-pass").Width = 70 },
		"missing backup":  func(c *Config) { c.Find("p-pass").Backup = "p-nowhere" },
		"backup itself":   func(c *Config) { c.Find("p-pass").Backup = "p-pass" },
		"duplicate id":    func(c *Config) { c.Printers = append(c.Printers, c.Printers[0]) },
		"http server":     func(c *Config) { c.Server = "http://palate.example" },
		"usb and address": func(c *Config) { c.Find("p-pass").PrinterName = "EPSON" },
	} {
		c, _ := Read(path)
		change(c)
		if err := Save(path, c); err == nil {
			t.Errorf("%s: saved", name)
		}
	}
	if got, _ := os.ReadFile(path); string(got) != twoPrinters {
		t.Fatal("a refused change touched the file")
	}
}

func TestRemoveRules(t *testing.T) {
	path := writeFile(t, twoPrinters)
	c, _ := Read(path)
	if err := c.Remove("p-pass"); err == nil || !strings.Contains(err.Error(), "p-tandoor use it as their backup") {
		t.Fatalf("removing a printer that is someone's backup: %v", err)
	}
	if err := c.Remove("p-nowhere"); err == nil {
		t.Fatal("removed a printer that doesn't exist")
	}
	if err := c.Remove("p-tandoor"); err != nil {
		t.Fatal(err)
	}
	if err := c.Remove("p-pass"); err == nil || !strings.Contains(err.Error(), "only printer") {
		t.Fatalf("removing the last printer: %v", err)
	}
}

func TestCheckLeavesTheOriginalAlone(t *testing.T) {
	path := writeFile(t, twoPrinters)
	c, _ := Read(path)
	v, err := c.Check()
	if err != nil {
		t.Fatal(err)
	}
	if v.Find("p-pass").Address != "192.168.1.51:9100" || c.Find("p-pass").Address != "192.168.1.51" {
		t.Fatal("Check must normalise a copy, not the config being edited")
	}
}
