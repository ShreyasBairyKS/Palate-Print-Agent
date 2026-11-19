package main

import (
	"bufio"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/config"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/service"
)

type fakeSetup struct {
	installed []string
	codes     []string
	started   bool
	tests     []string
	failOnce  map[string]bool
}

func newSetup(t *testing.T, input string, f *fakeSetup, installed bool) (*setupEnv, *strings.Builder) {
	t.Helper()
	out := &strings.Builder{}
	env := &setupEnv{
		in:      bufio.NewReader(strings.NewReader(input)),
		out:     out,
		cfgPath: filepath.Join(t.TempDir(), "PalatePrintAgent", "agent.json"),
		status: func() (service.Info, error) {
			if installed {
				return service.Info{State: "running", Account: service.VirtualAccount}, nil
			}
			return service.Info{}, service.ErrNotInstalled
		},
		install: func(p string) error { f.installed = append(f.installed, p); return nil },
		pair: func(cfg *config.Config, code string) (string, error) {
			f.codes = append(f.codes, code)
			if code != "K7Q2-M9XD" {
				return "", errors.New("pairing refused (401 invalid_code)")
			}
			return "dev-0001", nil
		},
		start: func() error { f.started = true; return nil },
		testPrint: func(p config.Printer, _ config.Timings) error {
			f.tests = append(f.tests, p.ID)
			if f.failOnce[p.ID] {
				f.failOnce[p.ID] = false
				return errors.New("refused: connection refused")
			}
			return nil
		},
		usbList:  func() ([]string, error) { return []string{"EPSON TM-T82 Receipt"}, nil },
		edgeFind: func() (string, error) { return `C:\Edge\msedge.exe`, nil },
	}
	return env, out
}

func TestSetupWalksThroughEverything(t *testing.T) {
	input := strings.Join([]string{
		"http://palate.example", // refused: not https
		"https://palate.example",
		"Tandoor", "", "192.168.1.50", "", // default id p-tandoor, default width 80
		"r",                         // its first test print fails: retry
		"y",                         // another printer
		"Pass", "p-pass", "1", "58", // USB printer number 1
		"n",          // no more printers
		"y",          // set backups
		"p-pass", "", // Tandoor backs up to Pass; Pass has none
		"WRONG-CODE", "k7q2-m9xd", // a bad code, then a good one (typed in lower case)
	}, "\n") + "\n"
	f := &fakeSetup{failOnce: map[string]bool{"p-tandoor": true}}
	env, out := newSetup(t, input, f, false)
	if err := runSetup(env); err != nil {
		t.Fatalf("setup: %v\n%s", err, out)
	}

	cfg, err := config.Load(env.cfgPath)
	if err != nil {
		t.Fatalf("saved config: %v", err)
	}
	if cfg.Server != "https://palate.example" || len(cfg.Printers) != 2 {
		t.Fatalf("config = %+v", cfg)
	}
	tandoor, pass := cfg.Find("p-tandoor"), cfg.Find("p-pass")
	if tandoor == nil || tandoor.Kind != "network" || tandoor.Address != "192.168.1.50:9100" || tandoor.Width != 80 || tandoor.Backup != "p-pass" {
		t.Fatalf("Tandoor = %+v", tandoor)
	}
	if pass == nil || pass.Kind != "usb" || pass.PrinterName != "EPSON TM-T82 Receipt" || pass.Width != 58 || pass.Backup != "" {
		t.Fatalf("Pass = %+v", pass)
	}
	if strings.Join(f.tests, ",") != "p-tandoor,p-tandoor,p-pass" {
		t.Fatalf("test prints = %v; want Tandoor twice (a retry), then Pass", f.tests)
	}
	if len(f.installed) != 1 || f.installed[0] != env.cfgPath {
		t.Fatalf("installed = %v", f.installed)
	}
	if strings.Join(f.codes, ",") != "WRONG-CODE,K7Q2-M9XD" || !f.started {
		t.Fatalf("pairing codes = %v, started = %v", f.codes, f.started)
	}
	for _, want := range []string{"plain http is only allowed", "The test print failed", "1. EPSON TM-T82 Receipt", "That didn't work", "Done. The print agent is running"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output is missing %q\n%s", want, out)
		}
	}
}

func TestSetupCanPairLater(t *testing.T) {
	input := "https://palate.example\nGrill\n\n192.168.1.70\n80\nn\n\n"
	f := &fakeSetup{}
	env, out := newSetup(t, input, f, false)
	if err := runSetup(env); err != nil {
		t.Fatalf("setup: %v\n%s", err, out)
	}
	if len(f.installed) != 1 || f.started || len(f.codes) != 0 {
		t.Fatalf("installed %v, started %v, codes %v; want installed, not started, no pairing", f.installed, f.started, f.codes)
	}
	if !strings.Contains(out.String(), "palate-agent pair -code") {
		t.Fatalf("no instructions for pairing later:\n%s", out)
	}
}

func TestSetupOnAnInstalledPCOnlyShowsStatus(t *testing.T) {
	f := &fakeSetup{}
	env, out := newSetup(t, "", f, true)
	if err := runSetup(env); err != nil {
		t.Fatal(err)
	}
	if len(f.installed) != 0 || !strings.Contains(out.String(), "already installed") {
		t.Fatalf("installed %v\n%s", f.installed, out)
	}
}

func TestSetupCutShortSavesNothing(t *testing.T) {
	f := &fakeSetup{}
	env, _ := newSetup(t, "https://palate.example\nTandoor\n", f, false)
	if err := runSetup(env); err == nil || !strings.Contains(err.Error(), "ended before it finished") {
		t.Fatalf("err = %v", err)
	}
	if _, err := config.Read(env.cfgPath); err == nil || len(f.installed) != 0 {
		t.Fatal("a cut-short setup saved a config or installed the service")
	}
}
