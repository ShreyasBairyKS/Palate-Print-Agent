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
		if b.Dy() < 100 || b.Dy() > 600 {
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
	// A 12-dot black bar pinned to the right edge, and a line of text that
	// is only visible on 58 mm paper (via the paper-58 class).
	doc := `<!doctype html><html><head><style>
body{margin:0;background:#fff;font:20px Arial}
p{margin:0}
.edge{float:right;width:12px;height:40px;background:#000}
.narrow{display:none} html.paper-58 .narrow{display:block;height:30px;background:#000}
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
		if !Ink(img, b.Min.X+width-4, b.Min.Y+10) || Ink(img, b.Min.X+width-20, b.Min.Y+10) {
			t.Fatalf("%d: the right-edge bar is not at the paper's right edge; the page was laid out at another width", width)
		}
		narrowShown := inkRows(img, 44, b.Dy()) > 5 // below the 40-dot bar
		if narrowShown != (width == 384) {
			t.Fatalf("%d: paper-58 rule applied = %v", width, narrowShown)
		}
	}
}
