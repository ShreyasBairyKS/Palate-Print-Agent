package config

import (
	"strings"
	"testing"
)

func dotsConfig(p Printer) *Config {
	p.ID, p.Name, p.Kind, p.Address = "p-bill", "Bill", "network", "192.168.1.50"
	return &Config{Server: "https://palate.example", Printers: []Printer{p}}
}

func TestPrinterDots(t *testing.T) {
	cases := []struct {
		p     Printer
		dots  int
		paper string
	}{
		{Printer{Width: 80}, 576, "80 mm"},
		{Printer{Width: 58}, 384, "58 mm"},
		{Printer{Width: 80, Dots: 512}, 512, "80 mm, 512 dots"},
		{Printer{Width: 80, Dots: 832}, 832, "80 mm, 832 dots"},
		{Printer{Width: 58, Dots: 360}, 360, "58 mm, 360 dots"},
	}
	for _, c := range cases {
		checked, err := dotsConfig(c.p).Check()
		if err != nil {
			t.Fatalf("%+v: %v", c.p, err)
		}
		p := checked.Printers[0]
		if p.PrintDots() != c.dots || p.Paper() != c.paper {
			t.Errorf("%+v: PrintDots %d, Paper %q; want %d, %q", c.p, p.PrintDots(), p.Paper(), c.dots, c.paper)
		}
	}
}

// Dots that are just the usual for the width are dropped, so the config
// says nothing it doesn't need to.
func TestUsualDotsAreDropped(t *testing.T) {
	checked, err := dotsConfig(Printer{Width: 80, Dots: 576}).Check()
	if err != nil || checked.Printers[0].Dots != 0 {
		t.Fatalf("Dots = %d, %v; want 0", checked.Printers[0].Dots, err)
	}
}

func TestBadDotsAreRefused(t *testing.T) {
	for _, d := range []int{100, 250, 513, 840, -8} {
		_, err := dotsConfig(Printer{Width: 80, Dots: d}).Check()
		if err == nil || !strings.Contains(err.Error(), "dots") {
			t.Errorf("dots %d: err = %v", d, err)
		}
	}
}
