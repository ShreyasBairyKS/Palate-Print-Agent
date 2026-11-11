package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCheckPrinterAddress(t *testing.T) {
	ok := map[string]string{
		"192.168.1.50":        "192.168.1.50:9100",
		"192.168.1.50:9101":   "192.168.1.50:9101",
		"10.0.0.7":            "10.0.0.7:9100",
		"172.16.4.4":          "172.16.4.4:9100",
		"172.31.255.255:9100": "172.31.255.255:9100",
		"127.0.0.1:19100":     "127.0.0.1:19100", // the rig
	}
	for in, want := range ok {
		got, err := CheckPrinterAddress(in)
		if err != nil || got != want {
			t.Errorf("%q -> %q, %v; want %q", in, got, err, want)
		}
	}
	bad := []string{
		"8.8.8.8",           // public
		"203.0.113.7:9100",  // public (the mock's hostile address)
		"172.32.0.1",        // just outside 172.16/12
		"printer.local",     // hostname
		"evil.example.com",  // hostname
		"192.168.1.50:0",    // bad port
		"192.168.1.50:http", // bad port
		"[fd00::1]:9100",    // IPv6 not supported on printers yet
		"",
	}
	for _, in := range bad {
		if got, err := CheckPrinterAddress(in); err == nil {
			t.Errorf("%q accepted as %q; want an error", in, got)
		}
	}
}

func TestCheckServer(t *testing.T) {
	for _, s := range []string{"https://api.palate.example", "http://127.0.0.1:8080"} {
		if err := checkServer(s); err != nil {
			t.Errorf("%s: %v", s, err)
		}
	}
	for _, s := range []string{"http://api.palate.example", "http://192.168.1.2:8080", "ftp://x", "not a url"} {
		if err := checkServer(s); err == nil {
			t.Errorf("%s accepted; want an error", s)
		}
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.json")
	os.WriteFile(path, []byte(`{
		"server": "http://127.0.0.1:8080",
		"agent_name": "till-1",
		"printers": [{"id":"p1","name":"Tandoor","kind":"network","address":"192.168.1.50","width":80},
		             {"id":"p2","name":"Pass","kind":"usb","printer_name":"EPSON TM-T82 Receipt","width":58}],
		"timings": {"stall_timeout": "4s"}
	}`), 0o644)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Printers[0].Address != "192.168.1.50:9100" {
		t.Errorf("address = %q, want the port added", c.Printers[0].Address)
	}
	if c.Timings.StallTimeout.D() != 4*time.Second || c.Timings.ConnectTimeout.D() != 3*time.Second || c.Timings.FastRetries != 2 {
		t.Errorf("timings = %+v; want stall 4s from the file and the rest defaulted", c.Timings)
	}
	if c.CredentialFile != filepath.Join(dir, "credential.bin") {
		t.Errorf("credential file = %q", c.CredentialFile)
	}
}

func TestLoadRejects(t *testing.T) {
	cases := map[string]string{
		"public printer": `{"server":"https://x","printers":[{"id":"p1","name":"A","kind":"network","address":"8.8.8.8","width":80}]}`,
		"duplicate id":   `{"server":"https://x","printers":[{"id":"p1","name":"A","kind":"network","address":"10.0.0.1","width":80},{"id":"p1","name":"B","kind":"network","address":"10.0.0.2","width":80}]}`,
		"bad width":      `{"server":"https://x","printers":[{"id":"p1","name":"A","kind":"network","address":"10.0.0.1","width":72}]}`,
		"usb no name":    `{"server":"https://x","printers":[{"id":"p1","name":"A","kind":"usb","width":80}]}`,
		"usb with addr":  `{"server":"https://x","printers":[{"id":"p1","name":"A","kind":"usb","printer_name":"EPSON","address":"10.0.0.1","width":80}]}`,
		"net with name":  `{"server":"https://x","printers":[{"id":"p1","name":"A","kind":"network","printer_name":"EPSON","address":"10.0.0.1","width":80}]}`,
		"no printers":    `{"server":"https://x","printers":[]}`,
		"unknown field":  `{"server":"https://x","credential":"plaintext!","printers":[{"id":"p1","name":"A","kind":"network","address":"10.0.0.1","width":80}]}`,
		"bad duration":   `{"server":"https://x","printers":[{"id":"p1","name":"A","kind":"network","address":"10.0.0.1","width":80}],"timings":{"retry_gap":5}}`,
	}
	for name, body := range cases {
		path := filepath.Join(t.TempDir(), "agent.json")
		os.WriteFile(path, []byte(body), 0o644)
		if _, err := Load(path); err == nil {
			t.Errorf("%s: loaded; want an error", name)
		} else if !strings.Contains(err.Error(), "agent.json") {
			t.Errorf("%s: error %q does not name the file", name, err)
		}
	}
}
