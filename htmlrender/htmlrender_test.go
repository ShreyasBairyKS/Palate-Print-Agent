package htmlrender

import (
	"context"
	"errors"
	"image"
	"image/color"
	"strings"
	"testing"
	"time"
)

func TestLockdownRemovesEverythingThatRunsLoadsOrNavigates(t *testing.T) {
	doc := `<!doctype html><html><head>
<meta http-equiv="refresh" content="0;url=file:///C:/ProgramData/PalatePrintAgent/agent.json">
<meta http-equiv="&#114;efresh" content="0;url=file:///C:/secret">
<meta content="a>b" http-equiv="refresh">
<meta http-equiv="Content-Security-Policy" content="default-src *">
<meta charset="windows-1252">
<base href="https://evil.example/">
<link rel="stylesheet" href="https://evil.example/x.css">
<script>document.body.innerHTML = "pwned"</script>
</head><body onload="alert(1)">
<h1 onclick="x()">Palate Kitchen</h1>
<iframe src="file:///C:/Windows/win.ini"></iframe>
<object data="x.swf"></object><embed src="x"><frameset></frameset>
<svg><script>alert(1)</script><image href="file:///C:/x.png"/></svg>
<p>Total 798.00</p>
</body></html>`
	out, err := Lockdown(doc, 576)
	if err != nil {
		t.Fatal(err)
	}
	low := strings.ToLower(out)
	for _, bad := range []string{"refresh", "evil.example", "<script", "pwned", "onload", "onclick",
		"<iframe", "<object", "<embed", "<base", "<link", "default-src *", "windows-1252"} {
		if strings.Contains(low, bad) {
			t.Errorf("locked-down page still contains %q:\n%s", bad, out)
		}
	}
	for _, keep := range []string{"Palate Kitchen", "Total 798.00"} {
		if !strings.Contains(out, keep) {
			t.Errorf("locked-down page lost %q", keep)
		}
	}
	head := out[strings.Index(low, "<head>"):]
	if !strings.HasPrefix(head, `<head><meta charset="utf-8"/><meta http-equiv="Content-Security-Policy" content="`+
		strings.ReplaceAll(Policy, "'", "&#39;")+`"/>`) {
		t.Fatalf("the policy must come first in <head>; got:\n%.300s", head)
	}
}

func TestLockdownOfAFragmentStillGetsThePolicy(t *testing.T) {
	out, err := Lockdown("<p>Just a paragraph</p>", 384)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Content-Security-Policy") || !strings.Contains(out, "Just a paragraph") {
		t.Fatalf("got %s", out)
	}
	if !strings.Contains(out, `class="paper-58"`) || !strings.Contains(out, `data-paper-dots="384"`) ||
		!strings.Contains(out, "html{width:384px!important") {
		t.Fatalf("the page is not fitted to 58 mm paper: %s", out)
	}
}

func white(w, h int) *image.Gray {
	img := image.NewGray(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = 0xFF
	}
	return img
}

func TestTrimCutsBelowTheLastInk(t *testing.T) {
	img := white(64, 1000)
	img.SetGray(5, 100, color.Gray{})
	out, err := Trim(img, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if h := out.Bounds().Dy(); h != 101+16 {
		t.Fatalf("height %d; want the last ink row plus a 16-dot margin (117)", h)
	}
}

func TestTrimRefusesACutOffDocument(t *testing.T) {
	img := white(64, 100)
	img.SetGray(5, 99, color.Gray{})
	if _, err := Trim(img, 100); err == nil || !strings.Contains(err.Error(), "longer than") {
		t.Fatalf("err = %v; a document reaching the bottom must be refused, not printed without its end", err)
	}
	if _, err := Trim(white(64, 100), 100); err == nil {
		t.Fatal("an all-white document should be refused")
	}
}

func TestRenderRejectsBadWidth(t *testing.T) {
	e := &Edge{}
	if _, err := e.Render(context.Background(), "<p>x</p>", 577); err == nil {
		t.Fatal("a width that isn't a multiple of 8 was accepted")
	}
}

func TestMissingEdgeIsAClearError(t *testing.T) {
	e := &Edge{Path: `C:\nowhere\msedge.exe`}
	if _, err := e.Render(context.Background(), "<p>x</p>", 576); err == nil || !strings.Contains(err.Error(), "edge_path") {
		t.Fatalf("err = %v", err)
	}
}

// edge returns a renderer using the real Edge, or skips the test.
func edge(t *testing.T) *Edge {
	t.Helper()
	if testing.Short() {
		t.Skip("drives Microsoft Edge; skipped with -short")
	}
	e := &Edge{WorkDir: t.TempDir(), Timeout: 60 * time.Second}
	if _, err := e.edgePath(); errors.Is(err, ErrNoEdge) {
		t.Skip("Microsoft Edge is not installed here")
	}
	return e
}

// inkRows counts rows with any dark pixel between y0 and y1.
func inkRows(img image.Image, y0, y1 int) int {
	n := 0
	b := img.Bounds()
	for y := b.Min.Y + y0; y < b.Min.Y+y1 && y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if Ink(img, x, y) {
				n++
				break
			}
		}
	}
	return n
}

func TestEdgeDrawsABillAtThePapersWidth(t *testing.T) {
	e := edge(t)
	bill := `<!doctype html><html><head><style>
body{margin:0;padding:8px;font:22px Arial,sans-serif;color:#000;background:#fff}
.tot{font-weight:bold;font-size:28px;border-top:2px dashed #000}
</style></head><body>
<h1 style="text-align:center;margin:0">Palate Kitchen</h1>
<div>2 x Paneer Tikka ₹480.00</div><div>4 x ಬೆಣ್ಣೆ ನಾನ್ ₹240.00</div><div>1 x मसाला चाय ₹40.00</div>
<div class="tot">TOTAL ₹798.00</div></body></html>`
	for _, width := range []int{576, 384} {
		img, err := e.Render(context.Background(), bill, width)
		if err != nil {
			t.Fatal(err)
		}
		b := img.Bounds()
		if b.Dx() != width {
			t.Fatalf("drew %d dots wide; want %d", b.Dx(), width)
		}
		if b.Dy() < 200 || b.Dy() > 1400 {
			t.Fatalf("drew %d dots tall; want the bill trimmed to its content", b.Dy())
		}
		if inkRows(img, 0, b.Dy()) < 50 {
			t.Fatal("the bill drew almost no ink")
		}
	}
}

func TestEdgeRunsNoScripts(t *testing.T) {
	e := edge(t)
	// If the script ran, it would replace the page with a 2000-dot black
	// block, and the picture would be tall and dark.
	doc := `<!doctype html><html><body style="margin:0;background:#fff">
<p style="font:20px Arial">Bill</p>
<script>document.body.innerHTML='<div style="height:2000px;background:#000"></div>'</script>
<img src="x" onerror="document.body.style.background='#000'">
</body></html>`
	img, err := e.Render(context.Background(), doc, 576)
	if err != nil {
		t.Fatal(err)
	}
	if h := img.Bounds().Dy(); h > 200 {
		t.Fatalf("picture is %d dots tall; the script ran", h)
	}
}

func TestEdgeLaysThePageOutAtExactlyThePapersWidth(t *testing.T) {
	e := edge(t)
	// A 3 mm black bar pinned to the right edge, 5 mm tall, and a block that
	// is only visible on 58 mm paper (via the paper-58 class).
	doc := `<!doctype html><html><head><style>
body{margin:0;background:#fff;font:20px Arial}
p{margin:0}
.edge{float:right;width:3mm;height:5mm;background:#000}
.narrow{display:none} html.paper-58 .narrow{display:block;height:4mm;background:#000}
</style></head><body><div class="edge"></div><p>Bill</p><div class="narrow"></div></body></html>`
	for _, width := range []int{576, 384} {
		img, err := e.Render(context.Background(), doc, width)
		if err != nil {
			t.Fatal(err)
		}
		if img.Bounds().Dx() != width {
			t.Fatalf("%d: picture is %d wide", width, img.Bounds().Dx())
		}
		b := img.Bounds()
		// 3 mm is 24 dots: ink 4 dots from the edge, none 32 dots in.
		if !Ink(img, b.Min.X+width-4, b.Min.Y+10) || Ink(img, b.Min.X+width-32, b.Min.Y+10) {
			t.Fatalf("%d: the right-edge bar is not at the paper's right edge; the page was laid out at another width", width)
		}
		narrowShown := inkRows(img, 48, b.Dy()) > 5 // below the 5 mm (40-dot) bar
		if narrowShown != (width == 384) {
			t.Fatalf("%d: paper-58 rule applied = %v", width, narrowShown)
		}
	}
}

// A printer that isn't 384 or 576 dots across gets its roll's class, which
// designs are written for, and its exact width.
func TestLockdownTagsOtherWidths(t *testing.T) {
	for dots, want := range map[int][]string{
		576: {`class="paper-80"`, `data-paper-dots="576"`},
		512: {`class="paper-80 paper-512dots"`, `data-paper-dots="512"`, "html{width:512px!important"},
		832: {`class="paper-80 paper-832dots"`, `data-paper-dots="832"`},
		360: {`class="paper-58 paper-360dots"`, `data-paper-dots="360"`},
	} {
		out, err := Lockdown("<p>Bill</p>", dots)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range want {
			if !strings.Contains(out, w) {
				t.Errorf("%d dots: no %s in %s", dots, w, out)
			}
		}
	}
}

// inkInColumns reports whether any dark pixel lies in columns [x0, x1).
func inkInColumns(img image.Image, x0, x1 int) bool {
	b := img.Bounds()
	for x := b.Min.X + x0; x < b.Min.X+x1 && x < b.Max.X; x++ {
		for y := b.Min.Y; y < b.Max.Y; y++ {
			if Ink(img, x, y) {
				return true
			}
		}
	}
	return false
}

// A page designed wider than the paper's 72 mm print width (here an
// invoice laid out 80 mm wide, as Palate's are) is shrunk to fit, as a
// browser's "Fit to printable area" does: its right-hand column, with a
// mark at the far right, makes it onto the paper.
func TestEdgeFitsAWidePageToThePaper(t *testing.T) {
	e := edge(t)
	doc := `<!doctype html><html><head><style>body{margin:0;font:13px Arial,sans-serif}
.inv{width:80mm} td{padding:2px} .r{text-align:right}</style></head><body><div class="inv">
<table style="width:100%"><tr><th align=left>ITEM NAME</th><th>QT</th><th class=r>RATE</th><th class=r>AMOUNT</th></tr>
<tr><td>Glen Fiddich 12 Yrs</td><td>1</td><td class=r>599.00</td><td class=r>599.00<span style="display:inline-block;width:2mm;height:4mm;background:#000"></span></td></tr></table>
</div></body></html>`
	img, err := e.Render(context.Background(), doc, 576)
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != 576 {
		t.Fatalf("%d dots wide", img.Bounds().Dx())
	}
	if !inkInColumns(img, 576-40, 576) {
		t.Fatal("nothing printed near the right edge: the amount column was cut off")
	}
}

// A page that fits is printed exactly as it is: not shrunk.
func TestEdgeDoesNotShrinkAPageThatFits(t *testing.T) {
	e := edge(t)
	doc := `<!doctype html><html><body style="margin:0"><div style="width:72mm;height:10mm;background:#000"></div></body></html>`
	img, err := e.Render(context.Background(), doc, 576)
	if err != nil {
		t.Fatal(err)
	}
	if rows := inkRows(img, 0, img.Bounds().Dy()); rows < 78 || rows > 82 || !Ink(img, 572, 40) {
		t.Fatalf("a 72 x 10 mm bar printed %d rows tall; want 80, full width", rows)
	}
}

// A page far too wide prints at the smallest readable size, half, with the
// rest cut off, rather than not at all.
func TestEdgeFarTooWidePrintsAtHalfSize(t *testing.T) {
	e := edge(t)
	doc := `<!doctype html><html><body style="margin:0"><div style="width:300mm;height:10mm;background:#000"></div></body></html>`
	img, err := e.Render(context.Background(), doc, 576)
	if err != nil {
		t.Fatal(err)
	}
	if rows := inkRows(img, 0, img.Bounds().Dy()); rows < 38 || rows > 42 {
		t.Fatalf("bar printed %d rows tall; want 40 (half of 10 mm)", rows)
	}
}

// The page's print styles apply and its screen styles don't, as when a
// browser prints it.
func TestEdgeUsesPrintStyles(t *testing.T) {
	e := edge(t)
	doc := `<!doctype html><html><head><style>
body{margin:0} .p{display:none} .s{height:20mm;background:#000}
@media print{.p{display:block;height:10mm;background:#000}}
@media screen{.s{display:block}} @media only screen and (min-width:100mm){.s{height:30mm}}
</style><style media="screen">.s2{height:20mm;background:#000}</style></head>
<body><div class="p"></div><div class="s" style="display:none"></div><div class="s2"></div></body></html>`
	img, err := e.Render(context.Background(), doc, 576)
	if err != nil {
		t.Fatal(err)
	}
	if rows := inkRows(img, 0, img.Bounds().Dy()); rows < 78 || rows > 82 {
		t.Fatalf("%d rows of ink; want 80 (only the 10 mm print block)", rows)
	}
}

func TestLockdownTurnsPrintStylesOn(t *testing.T) {
	out, err := Lockdown(`<style>@media print{a{b:c}} @MEDIA ONLY SCREEN and (min-width:9px){d{e:f}}</style><style media="print">x{}</style><style media="screen">y{}</style>`, 576)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"@media all{a{b:c}}", "@media speech and (min-width:9px)", `media="all">x{}`, `media="speech">y{}`} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in %s", want, out)
		}
	}
}

// A page prints at its real size, as a browser prints it: 36 mm is 288
// dots and 10 mm is 80 (8 dots per mm), whatever the CSS unit.
func TestEdgePrintsAtRealSize(t *testing.T) {
	e := edge(t)
	for name, box := range map[string]string{
		"mm": "width:36mm;height:10mm",
		"px": "width:136.06px;height:37.8px", // 36 mm and 10 mm at 96 px to the inch
		"pt": "width:102.05pt;height:28.35pt",
	} {
		doc := `<!doctype html><html><body style="margin:0"><div style="` + box + `;background:#000"></div></body></html>`
		img, err := e.Render(context.Background(), doc, 576)
		if err != nil {
			t.Fatal(err)
		}
		rows := inkRows(img, 0, img.Bounds().Dy())
		if rows < 78 || rows > 82 {
			t.Errorf("%s: box is %d dots tall; want 80 (10 mm)", name, rows)
		}
		if !Ink(img, 284, 40) || Ink(img, 292, 40) {
			t.Errorf("%s: box doesn't end at 288 dots (36 mm)", name)
		}
	}
}

// PxPerInch PrinterDPI is the old drawing: one CSS pixel per printer dot.
func TestEdgeOnePixelPerDot(t *testing.T) {
	e := edge(t)
	e.PxPerInch = PrinterDPI
	doc := `<!doctype html><html><body style="margin:0"><div style="width:288px;height:80px;background:#000"></div></body></html>`
	img, err := e.Render(context.Background(), doc, 576)
	if err != nil {
		t.Fatal(err)
	}
	if rows := inkRows(img, 0, img.Bounds().Dy()); rows < 78 || rows > 82 || !Ink(img, 284, 40) || Ink(img, 292, 40) {
		t.Fatalf("288x80 px didn't print as 288x80 dots (rows %d)", rows)
	}
}

func TestPxPerInchOutOfRange(t *testing.T) {
	for _, ppi := range []float64{10, 300} {
		e := &Edge{PxPerInch: ppi, Path: `C:\nowhere\msedge.exe`}
		if _, err := e.Render(context.Background(), "<p>x</p>", 576); err == nil || !strings.Contains(err.Error(), "out of range") {
			t.Errorf("%g: err = %v", ppi, err)
		}
	}
}
