package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/config"
)

func TestSuggestID(t *testing.T) {
	for name, want := range map[string]string{"Tandoor": "p-tandoor", "Front Bill": "p-front-bill", "ಬಿಲ್": "p-3", "": "p-3"} {
		if got := suggestID(name, 3); got != want {
			t.Errorf("suggestID(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestRealPrintersHidesPretendOnes(t *testing.T) {
	got := realPrinters([]string{"OneNote (Desktop)", "Microsoft XPS Document Writer", "Microsoft Print to PDF", "Fax", "EPSON TM-T82 Receipt", "POS-80"})
	if strings.Join(got, ",") != "EPSON TM-T82 Receipt,POS-80" {
		t.Fatalf("got %v", got)
	}
}

func TestMakePrinter(t *testing.T) {
	raw := &config.Config{Server: "https://palate.example", Printers: []config.Printer{
		{ID: "p-tandoor", Name: "Tandoor", Kind: "network", Address: "192.168.1.50", Width: 80}}}
	p, err := makePrinter(raw, printerInput{Name: "Pass", ID: "p-pass", Where: "192.168.1.51", Width: 58, Backup: "p-tandoor"}, "")
	if err != nil || p.Address != "192.168.1.51:9100" || p.Kind != "network" {
		t.Fatalf("network printer: %+v, %v", p, err)
	}
	if p, err := makePrinter(raw, printerInput{Name: "Bill", ID: "p-bill", USB: true, Where: "EPSON TM-T82", Width: 80}, ""); err != nil || p.Kind != "usb" {
		t.Fatalf("usb printer: %+v, %v", p, err)
	}
	// Editing a printer keeps its own id without clashing.
	if _, err := makePrinter(raw, printerInput{Name: "Tandoor 2", ID: "p-tandoor", Where: "192.168.1.60", Width: 80}, "p-tandoor"); err != nil {
		t.Fatalf("editing: %v", err)
	}
	for name, in := range map[string]printerInput{
		"no name":      {ID: "p-x", Where: "192.168.1.9", Width: 80},
		"bad id":       {Name: "X", ID: "1x", Where: "192.168.1.9", Width: 80},
		"duplicate id": {Name: "X", ID: "p-tandoor", Where: "192.168.1.9", Width: 80},
		"no address":   {Name: "X", ID: "p-x", Width: 80},
		"public IP":    {Name: "X", ID: "p-x", Where: "8.8.8.8", Width: 80},
		"hostname":     {Name: "X", ID: "p-x", Where: "printer.local", Width: 80},
		"bad width":    {Name: "X", ID: "p-x", Where: "192.168.1.9", Width: 70},
		"self backup":  {Name: "X", ID: "p-x", Where: "192.168.1.9", Width: 80, Backup: "p-x"},
		"no USB":       {Name: "X", ID: "p-x", USB: true, Width: 80},
	} {
		if _, err := makePrinter(raw, in, ""); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestInstallSteps(t *testing.T) {
	f := &fakeSetup{}
	env, _ := newSetup(t, "", f, false)
	env.cfgPath = filepath.Join(t.TempDir(), "PalatePrintAgent", "agent.json")
	raw := &config.Config{Server: "https://palate.example", Printers: []config.Printer{
		{ID: "p-tandoor", Name: "Tandoor", Kind: "network", Address: "192.168.1.50", Width: 80}}}
	var said []string
	paired, err := installSteps(env, raw, " k7q2-m9xd ", func(s string) { said = append(said, s) })
	if err != nil || !paired || !f.started || len(f.installed) != 1 || f.codes[0] != "K7Q2-M9XD" {
		t.Fatalf("paired %v, err %v, started %v, installed %v, codes %v", paired, err, f.started, f.installed, f.codes)
	}
	if _, err := config.Load(env.cfgPath); err != nil {
		t.Fatalf("config not saved: %v", err)
	}
	if !strings.Contains(strings.Join(said, "\n"), "Running") {
		t.Fatalf("progress = %v", said)
	}

	// No code: installed, not started.
	f2 := &fakeSetup{}
	env2, _ := newSetup(t, "", f2, false)
	if paired, err := installSteps(env2, raw, "", func(string) {}); err != nil || paired || f2.started {
		t.Fatalf("without a code: paired %v, err %v, started %v", paired, err, f2.started)
	}

	// A refused code is an error and nothing starts.
	f3 := &fakeSetup{}
	env3, _ := newSetup(t, "", f3, false)
	if _, err := installSteps(env3, raw, "WRONG", func(string) {}); err == nil || f3.started {
		t.Fatalf("a wrong code: err %v, started %v", err, f3.started)
	}
}

// A printer of another width keeps its dots, and reports the paper class
// Palate's server knows.
func TestMakePrinterWithDots(t *testing.T) {
	raw := &config.Config{Server: "https://palate.example"}
	p, err := makePrinter(raw, printerInput{Name: "Bill", ID: "p-bill", Where: "192.168.1.52", Width: 80, Dots: 512}, "")
	if err != nil || p.Dots != 512 || p.Width != 80 || p.Paper() != "80 mm, 512 dots" {
		t.Fatalf("p = %+v, %v", p, err)
	}
	if _, err := makePrinter(raw, printerInput{Name: "Bill", ID: "p-bill", Where: "192.168.1.52", Width: 80, Dots: 500}, ""); err == nil {
		t.Fatal("500 dots (not a multiple of 8) accepted")
	}
}

// printer set -dots picks the paper class from the dots unless -width says.
func TestApplyFlagsDots(t *testing.T) {
	p := config.Printer{Width: 58}
	applyFlags(&p, "", "", "", 0, 832, "")
	if p.Dots != 832 || p.Width != 80 {
		t.Fatalf("after -dots 832: %+v", p)
	}
	applyFlags(&p, "", "", "", 0, 0, "")
	if p.Dots != 0 || p.Width != 80 {
		t.Fatalf("after -dots 0: %+v", p)
	}
	applyFlags(&p, "", "", "", 0, -1, "")
	if p.Dots != 0 {
		t.Fatalf("no -dots changed it: %+v", p)
	}
}
