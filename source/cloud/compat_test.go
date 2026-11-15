package cloud

// Forward compatibility: a newer server adds fields; an agent installed today
// must keep printing. Fields that try to steer printing are still refused.

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/api"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/internal/mockserver"
)

func TestNewerServerFieldsAreIgnored(t *testing.T) {
	f := setup(t, time.Second)
	s := slip()
	s.Items[0].Notes = "less spicy"
	f.srv.NewJob("r1", "p1", s)
	f.srv.SetHostile(mockserver.NewerServer)
	jobs, err := f.client.Fetch(context.Background())
	if err != nil {
		t.Fatalf("a newer server's poll response failed: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("got %d jobs, want 1", len(jobs))
	}
	j := jobs[0]
	if j.Slip.Station != "Tandoor" || j.Slip.OrderNumber != "1042" || len(j.Slip.Items) != 1 ||
		j.Slip.Items[0].Name != "Paneer Tikka" || j.Slip.Items[0].Notes != "less spicy" {
		t.Fatalf("slip damaged by unknown fields: %+v", j.Slip)
	}
	if o := f.srv.Snapshot().Jobs[0].Outcome; o != nil {
		t.Fatalf("job with unknown fields was reported %+v; it should be printed, not refused", o)
	}
}

func TestUnknownFieldsAreListed(t *testing.T) {
	raw := `{"id":"j1","printer_id":"p1","created_at":"2026-11-11T00:00:00Z","priority":"high",
		"channel":{"kind":"captain-app"},
		"slip":{"station":"Tandoor","order_number":"1","course_number":2,
			"items":[{"quantity":1,"name":"Naan","allergens":["nuts"]},{"quantity":1,"name":"Dal","allergens":[]}],
			"extras":[{"label":"Course","value":"2","colour":"red"}]}}`
	j, unknown, err := parseJob([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(unknown)
	unknown = slices.Compact(unknown)
	want := []string{"channel", "priority", "slip.course_number", "slip.extras.colour", "slip.items.allergens"}
	if !slices.Equal(unknown, want) {
		t.Fatalf("unknown = %v, want %v", unknown, want)
	}
	if len(j.Slip.Items) != 2 || len(j.Slip.Extras) != 1 || j.Slip.Extras[0].Label != "Course" {
		t.Fatalf("known fields lost: %+v", j.Slip)
	}
}

func TestForbiddenFieldsAnywhereAreRefused(t *testing.T) {
	base := func(inject string) string {
		return strings.Replace(`{"id":"j1","printer_id":"p1","slip":{"station":"T","order_number":"1",
			"items":[{"quantity":1,"name":"Naan"}]}INJECT}`, "INJECT", inject, 1)
	}
	cases := map[string]string{
		"top level":           base(`,"printer_address":"203.0.113.7:9100"`),
		"any case":            base(`,"Printer_Address":"203.0.113.7:9100"`),
		"raw bytes":           base(`,"raw":"G0A="`),
		"in the slip":         strings.Replace(base(""), `"station":"T"`, `"station":"T","escpos":"G0A="`, 1),
		"in an item":          strings.Replace(base(""), `"name":"Naan"`, `"name":"Naan","host":"evil.example"`, 1),
		"inside unknown data": base(`,"routing":{"targets":[{"ip":"10.0.0.9"}]}`),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := parseJob([]byte(raw)); err == nil {
				t.Fatalf("accepted a job that tries to steer printing:\n%s", raw)
			}
		})
	}
}

func TestExtrasAreCarriedAndCapped(t *testing.T) {
	f := setup(t, time.Second)
	s := slip()
	for i := range api.MaxExtras + 3 {
		s.Extras = append(s.Extras, api.Extra{Label: fmt.Sprintf("L%d", i), Value: "v"})
	}
	f.srv.NewJob("r1", "p1", s)
	jobs, err := f.client.Fetch(context.Background())
	if err != nil || len(jobs) != 1 {
		t.Fatalf("jobs = %d, err = %v", len(jobs), err)
	}
	if got := len(jobs[0].Slip.Extras); got != api.MaxExtras {
		t.Fatalf("%d extras reached the queue, want %d", got, api.MaxExtras)
	}
}
