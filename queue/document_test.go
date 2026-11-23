package queue

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/job"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/printer"
)

// fakeHTML draws every document as a white picture with one black bar, and
// records what it was asked to draw.
type fakeHTML struct {
	mu     sync.Mutex
	widths []int
	docs   []string
	err    error
}

func (f *fakeHTML) Render(_ context.Context, doc string, widthDots int) (image.Image, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.widths = append(f.widths, widthDots)
	f.docs = append(f.docs, doc)
	if f.err != nil {
		return nil, f.err
	}
	img := image.NewGray(image.Rect(0, 0, widthDots, 40))
	for i := range img.Pix {
		img.Pix[i] = 0xFF
	}
	for y := 10; y < 20; y++ {
		for x := 0; x < widthDots; x++ {
			img.SetGray(x, y, color.Gray{})
		}
	}
	return img, nil
}

func docJob(id, printerID, html string) job.Job {
	return job.Job{ID: id, PrinterID: printerID, CreatedAt: time.Now(), Document: &job.Document{HTML: html}}
}

// gsv0 is the start of a GS v 0 raster command.
var gsv0 = []byte{0x1D, 'v', '0', 0}

func TestDocumentPrintsAsAPictureAtThePrintersWidth(t *testing.T) {
	w80, w58 := &scriptWriter{confirm: true}, &scriptWriter{confirm: true}
	html := &fakeHTML{}
	rec := newRecorder()
	q := start(t, []Printer{
		{ID: "bill", Name: "Bill", Width: job.Width80, Writer: w80},
		{ID: "small", Name: "Small", Width: job.Width58, Writer: w58},
	}, rec, Config{HTML: html})
	q.Submit(context.Background(), docJob("d1", "bill", "<p>Total 798.00</p>"))
	q.Submit(context.Background(), docJob("d2", "small", "<p>Total 40.00</p>"))
	got := map[string]job.Kind{}
	for i := 0; i < 2; i++ {
		r := rec.next(t)
		got[r.id] = r.o.Kind
	}
	if got["d1"] != job.Printed || got["d2"] != job.Printed {
		t.Fatalf("outcomes = %v; want both printed", got)
	}
	html.mu.Lock()
	widths := map[int]bool{}
	for _, wd := range html.widths {
		widths[wd] = true
	}
	html.mu.Unlock()
	if !widths[576] || !widths[384] {
		t.Fatalf("drawn at widths %v; want 576 for 80 mm and 384 for 58 mm", widths)
	}
	for name, w := range map[string]*scriptWriter{"80mm": w80, "58mm": w58} {
		data := w.calls[0]
		if !bytes.Contains(data, gsv0) {
			t.Errorf("%s: no raster image in what was printed", name)
		}
		if !bytes.HasSuffix(data, []byte{0x1D, 'V', 66, 0}) {
			t.Errorf("%s: the document was not cut", name)
		}
	}
}

func TestDocumentWithoutARendererFails(t *testing.T) {
	w := &scriptWriter{}
	rec := newRecorder()
	q := start(t, []Printer{{ID: "p1", Name: "Bill", Width: job.Width80, Writer: w}}, rec, Config{})
	q.Submit(context.Background(), docJob("d1", "p1", "<p>x</p>"))
	if r := rec.next(t); r.o.Kind != job.Failed || !strings.Contains(r.o.Reason, "can't print documents") {
		t.Fatalf("outcome = %+v", r.o)
	}
	if w.n() != 0 {
		t.Fatal("nothing should be written")
	}
}

func TestDocumentDrawFailureIsRecorded(t *testing.T) {
	w := &scriptWriter{}
	rec := newRecorder()
	q := start(t, []Printer{{ID: "p1", Name: "Bill", Width: job.Width80, Writer: w}}, rec,
		Config{HTML: &fakeHTML{err: errors.New("htmlrender: Edge drew nothing")}})
	q.Submit(context.Background(), docJob("d1", "p1", "<p>x</p>"))
	if r := rec.next(t); r.o.Kind != job.Failed || !strings.Contains(r.o.Reason, "Edge drew nothing") {
		t.Fatalf("outcome = %+v", r.o)
	}
	if w.n() != 0 {
		t.Fatal("nothing should be written when drawing fails")
	}
}

func TestDocumentBannersFromServerAndBackup(t *testing.T) {
	primary := &scriptWriter{results: []error{&printer.Error{Kind: printer.Hang, Err: errors.New("no answer")}}}
	backup := &scriptWriter{}
	rec := newRecorder()
	q := start(t, []Printer{
		{ID: "bill", Name: "Bill", Width: job.Width80, Writer: primary, Backup: "pass"},
		{ID: "pass", Name: "Pass", Width: job.Width80, Writer: backup},
	}, rec, Config{HTML: &fakeHTML{}})
	j := docJob("d1", "bill", "<p>x</p>")
	j.Document.Duplicate = true
	j.Document.MovedFrom = "Front bill"
	q.Submit(context.Background(), j)
	if r := rec.next(t); r.o.Kind != job.Fallback {
		t.Fatalf("outcome = %+v; want fallback", r.o)
	}
	out := string(backup.calls[0])
	for _, want := range []string{"DUPLICATE", "MOVED FROM FRONT BILL", "BACKUP FOR BILL"} {
		if !strings.Contains(out, want) {
			t.Errorf("backup copy has no %q banner", want)
		}
	}
}

func TestCrashMidWriteReprintsDocumentAsDuplicate(t *testing.T) {
	led := openLedger(t, "")
	if err := led.Begin("d1"); err != nil {
		t.Fatal(err)
	}
	w := &scriptWriter{}
	rec := newRecorder()
	q := start(t, []Printer{{ID: "p1", Name: "Bill", Width: job.Width80, Writer: w}}, rec,
		Config{Ledger: led, HTML: &fakeHTML{}})
	q.Submit(context.Background(), docJob("d1", "p1", "<p>x</p>"))
	r := rec.next(t)
	if !strings.Contains(r.o.Reason, CrashRecovery) || !strings.Contains(string(w.calls[0]), "DUPLICATE") {
		t.Fatalf("outcome = %+v; want a DUPLICATE reprint marked %s", r.o, CrashRecovery)
	}
}

// A printer set to other dots (an Epson TM-T88's 512) gets its bills drawn
// and printed exactly that wide, and its kitchen tickets too.
func TestDocumentAtAPrintersOwnDots(t *testing.T) {
	w := &scriptWriter{confirm: true}
	html := &fakeHTML{}
	rec := newRecorder()
	q := start(t, []Printer{{ID: "bill", Name: "Bill", Width: job.Width80, Dots: 512, Writer: w}}, rec, Config{HTML: html})
	q.Submit(context.Background(), docJob("d1", "bill", "<p>Total 798.00</p>"))
	if r := rec.next(t); r.o.Kind != job.Printed {
		t.Fatalf("outcome = %+v", r.o)
	}
	html.mu.Lock()
	widths := append([]int(nil), html.widths...)
	html.mu.Unlock()
	if len(widths) != 1 || widths[0] != 512 {
		t.Fatalf("drawn at %v; want 512", widths)
	}
	data := w.calls[0]
	i := bytes.Index(data, gsv0)
	if i < 0 || (int(data[i+4])|int(data[i+5])<<8)*8 != 512 {
		t.Fatalf("raster isn't 512 dots wide")
	}
}
