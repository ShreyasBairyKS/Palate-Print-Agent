package render

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/internal/escpos"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/job"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata/")

var ist = time.FixedZone("IST", 5*3600+1800)

func baseSlip() job.Slip {
	return job.Slip{
		Station:     "Tandoor",
		Table:       "T4",
		OrderNumber: "1042",
		PlacedAt:    time.Date(2026, 11, 11, 14, 5, 0, 0, ist),
		Items: []job.Item{
			{Quantity: 2, Name: "Paneer Tikka", Notes: "less spicy"},
			{Quantity: 1, Name: "Butter Naan"},
		},
	}
}

var goldenCases = map[string]func() job.Slip{
	"basic": baseSlip,
	"long-names": func() job.Slip {
		s := baseSlip()
		s.Items = []job.Item{
			{Quantity: 12, Name: "Tandoori Chicken Half Plate with Mint Chutney and Onion Rings",
				Notes: "no onions, extra chutney on the side, customer is allergic to cashew"},
			{Quantity: 1, Name: "Supercalifragilisticexpialidociousmasalakulcha"},
		}
		s.Notes = "Serve the starters together with the drinks please"
		return s
	},
	"takeaway-banners": func() job.Slip {
		s := baseSlip()
		s.Table = ""
		s.Duplicate = true
		s.DuplicateReason = "crash-recovery"
		s.MovedFrom = "Wok"
		s.Delayed = true
		return s
	},
	"latin-accents": func() job.Slip {
		s := baseSlip()
		s.Station = "Pâtisserie"
		s.Items = []job.Item{
			{Quantity: 1, Name: "Café crème brûlée", Notes: "jalapeño – “extra”"},
			{Quantity: 3, Name: "Crêpe Suzette €"},
		}
		return s
	},
}

func TestGolden(t *testing.T) {
	for name, mk := range goldenCases {
		for _, w := range []job.Width{job.Width58, job.Width80} {
			t.Run(name+"-"+w.String(), func(t *testing.T) {
				got, err := Render(mk(), w)
				if err != nil {
					t.Fatal(err)
				}
				base := filepath.Join("testdata", name+"-"+w.String())
				dump := escpos.Format(escpos.Decode(got))
				if *update {
					os.MkdirAll("testdata", 0o755)
					os.WriteFile(base+".bin", got, 0o644)
					os.WriteFile(base+".txt", []byte(dump), 0o644)
					return
				}
				want, err := os.ReadFile(base + ".bin")
				if err != nil {
					t.Fatalf("%v (run `go test ./render -update` to create it)", err)
				}
				if !bytes.Equal(got, want) {
					t.Errorf("bytes differ from %s.bin.\n--- want\n%s\n--- got\n%s",
						base, escpos.Format(escpos.Decode(want)), dump)
				}
			})
		}
	}
}

func TestDeterministic(t *testing.T) {
	for name, mk := range goldenCases {
		a, _ := Render(mk(), job.Width80)
		b, _ := Render(mk(), job.Width80)
		if !bytes.Equal(a, b) {
			t.Errorf("%s: two renders of the same slip differ", name)
		}
	}
}

func TestLinesFitThePaper(t *testing.T) {
	for name, mk := range goldenCases {
		for _, w := range []job.Width{job.Width58, job.Width80} {
			out, _ := Render(mk(), w)
			for _, c := range escpos.Decode(out) {
				if c.Name != "text" {
					continue
				}
				limit := columns(w)
				// The station line is double width, so half as many characters fit.
				if strings.EqualFold(string(c.Raw), strings.ToUpper(mk().Station)) {
					limit /= 2
				}
				if len(c.Raw) > limit {
					t.Errorf("%s %s: %q is %d columns, paper fits %d", name, w, c.Raw, len(c.Raw), limit)
				}
			}
		}
	}
}

func TestNonLatinNeedsRaster(t *testing.T) {
	s := baseSlip()
	s.Items[1].Name = "ಮಸಾಲೆ ದೋಸೆ" // masale dose, Kannada
	_, err := Render(s, job.Width80)
	if !errors.Is(err, ErrNeedsRaster) {
		t.Fatalf("err = %v, want ErrNeedsRaster", err)
	}
	var ure *UnsupportedRuneError
	if !errors.As(err, &ure) || ure.Field != "items[1].name" {
		t.Fatalf("err = %v, want it to name items[1].name", err)
	}
}

func TestControlCharactersCannotInjectCommands(t *testing.T) {
	s := baseSlip()
	s.Items[0].Name = "Naan\x1bp\x00\x19" // ESC p = open the cash drawer
	s.Notes = "line\x1d" + "V\x00one"     // GS V 0 = cut
	out, err := Render(s, job.Width80)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range escpos.Decode(out) {
		if c.Name == "ESC p" {
			t.Fatal("slip text produced a cash drawer command")
		}
	}
	cuts := 0
	for _, c := range escpos.Decode(out) {
		if c.Name == "GS V" {
			cuts++
		}
	}
	if cuts != 1 {
		t.Fatalf("got %d cuts, want exactly the one at the end", cuts)
	}
}

func TestRejectsBadSlips(t *testing.T) {
	cases := map[string]func(*job.Slip){
		"no items":      func(s *job.Slip) { s.Items = nil },
		"no station":    func(s *job.Slip) { s.Station = "" },
		"no order":      func(s *job.Slip) { s.OrderNumber = "" },
		"zero quantity": func(s *job.Slip) { s.Items[0].Quantity = 0 },
		"invalid utf-8": func(s *job.Slip) { s.Notes = "\xff\xfe" },
	}
	for name, mutate := range cases {
		s := baseSlip()
		mutate(&s)
		if _, err := Render(s, job.Width80); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if _, err := Render(baseSlip(), job.Width(72)); err == nil {
		t.Error("width 72: want an error")
	}
}

func TestWrap(t *testing.T) {
	cases := []struct {
		text       string
		cols, hang int
		want       []string
	}{
		{"short", 10, 0, []string{"short"}},
		{"", 10, 0, []string{""}},
		{"two words", 5, 0, []string{"two", "words"}},
		{"2 x Paneer Tikka Masala", 12, 4, []string{"2 x Paneer", "    Tikka", "    Masala"}},
		{"abcdefghijkl", 5, 0, []string{"abcde", "fghij", "kl"}},
		// A word too long for any line starts on the current one.
		{"x abcdefghij", 6, 2, []string{"x abcd", "  efgh", "  ij"}},
		{"1 x Supercalifragilistic", 10, 4, []string{"1 x Superc", "    alifra", "    gilist", "    ic"}},
		{"tab\tand\nnewline", 20, 0, []string{"tab and newline"}},
		// A hang as wide as the line is ignored rather than looping forever.
		{"hang wider than line", 3, 5, []string{"han", "g w", "ide", "r t", "han", "lin", "e"}},
	}
	for _, tc := range cases {
		got := wrap(tc.text, tc.cols, tc.hang)
		if strings.Join(got, "|") != strings.Join(tc.want, "|") {
			t.Errorf("wrap(%q, %d, %d) = %q, want %q", tc.text, tc.cols, tc.hang, got, tc.want)
		}
	}
}

func FuzzRender(f *testing.F) {
	f.Add("Tandoor", "Paneer Tikka", "less spicy", 2)
	f.Fuzz(func(t *testing.T, station, name, notes string, qty int) {
		s := baseSlip()
		s.Station, s.Items[0].Name, s.Items[0].Notes, s.Items[0].Quantity = station, name, notes, qty
		out, err := Render(s, job.Width58)
		if err != nil {
			return
		}
		for _, c := range escpos.Decode(out) {
			switch c.Name {
			case "unknown", "truncated", "ESC p":
				t.Fatalf("output contains %s: %q", c.Name, c.Raw)
			case "text":
				if len(c.Raw) > 32 {
					t.Fatalf("line %q is longer than 32 columns", c.Raw)
				}
			}
		}
	})
}
