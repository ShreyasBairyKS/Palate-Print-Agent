package job

import "testing"

func TestOutcomeValidate(t *testing.T) {
	cases := []struct {
		o  Outcome
		ok bool
	}{
		{Outcome{Kind: Printed, PrinterID: "p1"}, true},
		{Outcome{Kind: Written, PrinterID: "p1"}, true},
		{Outcome{Kind: ScreenOnly}, true},
		{Outcome{Kind: Fallback, PrinterID: "p2", Reason: "primary down"}, true},
		{Outcome{Kind: Fallback, PrinterID: "p2"}, false}, // must say why
		{Outcome{Kind: Failed}, false},                    // must say why
		{Outcome{Kind: "lost"}, false},                    // not a kind
	}
	for _, tc := range cases {
		if err := tc.o.Validate(); (err == nil) != tc.ok {
			t.Errorf("%+v: Validate() = %v, want ok=%v", tc.o, err, tc.ok)
		}
	}
}

func TestWidth(t *testing.T) {
	if !Width58.Valid() || !Width80.Valid() || Width(72).Valid() {
		t.Fatal("only 58 and 80 mm are valid widths")
	}
}
