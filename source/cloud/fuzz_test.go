package cloud

import (
	"encoding/json"
	"strings"
	"testing"
)

// FuzzParseJob throws arbitrary bytes at the code that reads each job from
// the server. It must never panic, and anything it accepts must obey the
// rules: an id, a printer id that isn't an address, no field that tries to
// steer printing anywhere in it, and no known field reported as unknown.
func FuzzParseJob(f *testing.F) {
	seeds := []string{
		`{"id":"j1","printer_id":"p1","created_at":"2026-11-11T00:00:00Z","slip":{"station":"Tandoor","order_number":"1","items":[{"quantity":1,"name":"Naan"}]}}`,
		`{"id":"j1","printer_id":"p1","priority":"high","slip":{"station":"T","order_number":"1","course_number":2,"items":[{"quantity":1,"name":"Dal","allergens":["nuts"]}],"extras":[{"label":"Course","value":"2"}]}}`,
		`{"id":"j1","printer_id":"p1","printer_address":"203.0.113.7:9100","slip":{}}`,
		`{"id":"j1","printer_id":"p1","routing":{"targets":[{"ip":"10.0.0.9"}]},"slip":{}}`,
		`{"id":"j1","printer_id":"192.168.1.50:9100","slip":{}}`,
		`{"id":"j1","printer_id":"p1","slip":{"items":[{"quantity":1,"name":"ಮಸಾಲೆ ದೋಸೆ","notes":"\u001b@"}]}}`,
		`[]`, `{}`, `null`, `{"id":1}`, `{"slip":[[[[[]]]]]}`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		j, unknown, err := parseJob(raw)
		if err != nil {
			return
		}
		if j.ID == "" || j.PrinterID == "" {
			t.Fatalf("accepted a job without id or printer id: %q", raw)
		}
		if looksLikeAddress(j.PrinterID) {
			t.Fatalf("accepted a printer id that is an address: %q", raw)
		}
		if hasForbidden(raw) {
			t.Fatalf("accepted a job with a field that steers printing: %q", raw)
		}
		for _, p := range unknown {
			if _, known := jobShape[strings.ToLower(p)]; known {
				t.Fatalf("reported known field %q as unknown for %q", p, raw)
			}
		}
	})
}

// hasForbidden walks the JSON independently of inspect and reports whether
// any object key, at any depth, is on the forbidden list.
func hasForbidden(raw []byte) bool {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return false
	}
	var walk func(any) bool
	walk = func(v any) bool {
		switch x := v.(type) {
		case map[string]any:
			for k, sub := range x {
				if forbidden[strings.ToLower(k)] || walk(sub) {
					return true
				}
			}
		case []any:
			for _, sub := range x {
				if walk(sub) {
					return true
				}
			}
		}
		return false
	}
	return walk(v)
}
