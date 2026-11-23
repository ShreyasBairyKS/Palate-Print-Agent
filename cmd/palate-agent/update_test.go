package main

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/agent"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/config"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/service"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/status"
)

func withBuildDate(t *testing.T, d string) {
	old := buildDate
	buildDate = d
	t.Cleanup(func() { buildDate = old })
}

func TestVersionLineRoundTrip(t *testing.T) {
	withBuildDate(t, "2026-11-22T08:35:00Z")
	ver, built := parseVersionLine(versionLine())
	if ver != agent.Version || built != buildDate {
		t.Fatalf("parse(%q) = %q, %q", versionLine(), ver, built)
	}
	if ver, built := parseVersionLine("0.3.0\n"); ver != "0.3.0" || built != "" {
		t.Fatalf("an older program's line = %q, %q", ver, built)
	}
	if l := labelFor("0.4.0", "2026-11-22T08:35:00Z"); !strings.HasPrefix(l, "version 0.4.0, built 22 Nov 2026, ") || strings.Contains(l, "PM") {
		t.Fatalf("label = %q", l)
	}
	if l := labelFor("0.4.0", ""); l != "version 0.4.0" {
		t.Fatalf("label without a date = %q", l)
	}
}

func TestCompareInstalled(t *testing.T) {
	withBuildDate(t, "2026-11-22T08:35:00Z")
	dir := t.TempDir()
	self, inst := filepath.Join(dir, "new.exe"), filepath.Join(dir, "PalatePrintAgent", "palate-agent.exe")
	os.WriteFile(self, []byte("new program"), 0o644)
	ran := ""
	version := func(exe string) (string, error) { ran = exe; return "0.4.0-m4 built 2026-11-21T10:24:00Z", nil }

	// Not installed in Program Files.
	if c, err := compareInstalled(self, inst, version); err != nil || !c.Different || !strings.HasPrefix(c.Installed, "missing") {
		t.Fatalf("missing: %+v, %v", c, err)
	}
	// The same program.
	os.MkdirAll(filepath.Dir(inst), 0o755)
	os.WriteFile(inst, []byte("new program"), 0o644)
	if c, _ := compareInstalled(self, inst, version); c.Different || ran != "" {
		t.Fatalf("same contents: %+v (ran %q)", c, ran)
	}
	// Itself.
	if c, _ := compareInstalled(inst, inst, version); c.Different {
		t.Fatalf("itself: %+v", c)
	}
	// An older build installed.
	os.WriteFile(inst, []byte("old program"), 0o644)
	c, err := compareInstalled(self, inst, version)
	if err != nil || !c.Different || c.Older || ran != inst || !strings.Contains(c.Installed, "21 Nov 2026") {
		t.Fatalf("older installed: %+v, %v", c, err)
	}
	if q := updateOffer(c); !strings.Contains(q, "21 Nov 2026") || !strings.Contains(q, "22 Nov 2026") || strings.Contains(q, "OLDER") {
		t.Fatalf("offer = %q", q)
	}
	// A newer build installed: this file is older.
	version = func(string) (string, error) { return "0.4.0-m4 built 2026-11-23T10:00:00Z", nil }
	if c, _ := compareInstalled(self, inst, version); !c.Older || !strings.Contains(updateOffer(c), "OLDER") {
		t.Fatalf("newer installed: %+v", c)
	}
	// An installed program too old to say when it was built.
	version = func(string) (string, error) { return "", errors.New("exit status 2") }
	if c, _ := compareInstalled(self, inst, version); !c.Different || c.Installed != "an earlier build" {
		t.Fatalf("silent installed: %+v", c)
	}
}

func TestSupportReport(t *testing.T) {
	withBuildDate(t, "2026-11-22T08:35:00Z")
	dir := filepath.Join(t.TempDir(), "PalatePrintAgent")
	os.MkdirAll(filepath.Join(dir, "logs"), 0o755)
	cfgPath := filepath.Join(dir, "agent.json")
	if err := config.Save(cfgPath, &config.Config{Server: config.DefaultServer, Printers: []config.Printer{
		{ID: "p-bar", Name: "Bar", Kind: "network", Address: "192.168.0.87:9100", Width: 80},
	}}); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "logs", "agent.log"), []byte("line one\nline two\n"), 0o644)
	now := time.Now()
	f := status.File{UpdatedAt: now, StartedAt: now, AgentVersion: agent.Version, Till: "FRONT-TILL",
		Server:   status.Server{Connected: false, Since: now.Add(-time.Minute), Error: "Post \"https://x\": dial tcp: lookup x: no such host"},
		Printers: []status.Printer{{ID: "p-bar", Name: "Bar", Where: "192.168.0.87:9100", Width: 80, State: "down", LastError: "paper out", Since: now}}}
	if err := status.Write(status.PathFor(cfgPath), f); err != nil {
		t.Fatal(err)
	}
	r := supportReport(reportEnv{
		now: now, cfgPath: cfgPath,
		svc: func() (service.Info, error) {
			return service.Info{State: "running", AutoStart: true, Account: "NT SERVICE\\PalatePrintAgent"}, nil
		},
		edgeFind:  func() (string, error) { return `C:\Edge\msedge.exe`, nil },
		edgeCheck: func(string) (int, error) { return 227, nil },
		self:      `C:\Users\x\Downloads\palate-agent.exe`, windows: "Windows 10.0.26200",
	})
	for _, want := range []string{
		"22 Nov 2026", "Windows 10.0.26200", "Service:       running",
		fmt.Sprintf("%-15sPROBLEM", browserShort+" check:"), "227 dots",
		"FRONT-TILL", "NOT connected", "error code: dns", "no such host",
		"Bar (p-bar) at 192.168.0.87:9100, 80 mm: down", "error code: paper out",
		"server: " + config.DefaultServer, "printer: p-bar (Bar)", "line two",
	} {
		if !strings.Contains(r, want) {
			t.Errorf("report lacks %q:\n%s", want, r)
		}
	}
	if strings.Contains(strings.ToLower(r), "credential:") {
		t.Error("report mentions the credential")
	}
	path, err := saveReport(t.TempDir(), r, now)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), "\r\n") || !strings.HasSuffix(path, ".txt") {
		t.Fatalf("saved report %s isn't a Windows text file", path)
	}
}

func TestSupportReportEdgeOK(t *testing.T) {
	r := supportReport(reportEnv{
		now: time.Now(), cfgPath: filepath.Join(t.TempDir(), "agent.json"),
		svc:       func() (service.Info, error) { return service.Info{}, service.ErrNotInstalled },
		edgeFind:  func() (string, error) { return `C:\Edge\msedge.exe`, nil },
		edgeCheck: func(string) (int, error) { return 481, nil },
	})
	for _, want := range []string{"Service:       not installed", fmt.Sprintf("%-15sOK", browserShort+" check:"), "Settings:      not included (there isn't one yet)"} {
		if !strings.Contains(r, want) {
			t.Errorf("report lacks %q:\n%s", want, r)
		}
	}
}

func TestWidestInkAndEdgeVersion(t *testing.T) {
	img := image.NewGray(image.Rect(0, 0, 100, 10))
	for i := range img.Pix {
		img.Pix[i] = 255
	}
	for x := 10; x < 70; x++ {
		img.Set(x, 4, color.Black)
	}
	if n := widestInk(img); n != 60 {
		t.Fatalf("widestInk = %d, want 60", n)
	}
	dir := t.TempDir()
	for _, d := range []string{"131.0.2903.112", "158.0.3100.9", "SetupMetrics"} {
		os.Mkdir(filepath.Join(dir, d), 0o755)
	}
	if v := edgeVersion(filepath.Join(dir, "msedge.exe")); v != "version 158.0.3100.9" {
		t.Fatalf("edgeVersion = %q", v)
	}
}
