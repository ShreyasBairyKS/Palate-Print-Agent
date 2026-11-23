// Package htmlrender draws a finished HTML document, such as a customer bill
// from Palate, as a picture exactly as wide as the printer's paper.
//
// It drives Microsoft Edge, which every Windows 10 and 11 till already has,
// in headless mode as a short-lived child process. Nothing listens on a port:
// Edge is given the page as a local file and writes a screenshot back.
//
// The page comes from the cloud, so it is treated as hostile:
//
//   - Lockdown parses the HTML and removes everything that can run code,
//     load another page or navigate: scripts, frames, objects, embeds,
//     <base>, <link>, every <meta http-equiv> (refresh included) and every
//     on* attribute. It then puts its own Content-Security-Policy first in
//     <head>: no scripts, no network, no files; only inline CSS and data:
//     images and fonts.
//   - Edge runs with a proxy that goes nowhere, so even a load the policy
//     missed can't leave the till; with no extensions, sync or background
//     networking; and with a fresh, throwaway profile for every document.
//
// Rendering is not deterministic across Edge versions, which is why it lives
// here and not in package render, whose output is byte-for-byte golden.
package htmlrender

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"math"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Policy is the Content-Security-Policy every document is drawn under.
const Policy = "default-src 'none'; style-src 'unsafe-inline' data:; img-src data:; font-src data:; " +
	"base-uri 'none'; form-action 'none'"

// Edge draws HTML with headless Microsoft Edge. The zero value works: it finds
// Edge in its usual places and works in the system temp folder.
type Edge struct {
	Path      string        // msedge.exe; "" looks in the usual install folders
	WorkDir   string        // each document gets a throwaway folder in here; "" means the system temp folder
	Timeout   time.Duration // the longest one document may take; default 30 s
	MaxHeight int           // the longest document in dots; default 12000 (150 cm at 8 dots/mm)
	// PxPerInch is how many CSS pixels make an inch on paper. The default,
	// 96, is the web's own: a page prints at the size a browser prints it,
	// so 72mm fills an 80 mm roll's print width and 12px text is 12px text.
	// PrinterDPI (203.2) draws one CSS pixel per printer dot instead, the
	// agent's behaviour before, which prints a page made for browsers at
	// 47% of its size.
	PxPerInch float64

	mu sync.Mutex // one document at a time: tills are often old and short of memory
}

// PrinterDPI is a receipt printer's resolution: 8 dots per mm.
const PrinterDPI = 203.2

// WebPxPerInch is CSS's own pixel: 96 to the inch.
const WebPxPerInch = 96.0

// ErrNoEdge means Microsoft Edge wasn't found on this till.
var ErrNoEdge = errors.New("htmlrender: Microsoft Edge not found; set edge_path in the config")

// Render draws doc widthDots wide and returns the picture, trimmed below the
// last ink so no paper is wasted. A document taller than MaxHeight is refused
// rather than printed with its end (usually the total) cut off.
//
// It prints the page as a browser prints it with "Fit to printable area":
// at the web's own size (PxPerInch), with the page's print styles, laid out
// exactly as wide as the printer's print width, and shrunk to fit if
// anything on it is wider than that. Nothing is cut off at the side unless
// it would need shrinking below MinZoom.
func (e *Edge) Render(ctx context.Context, doc string, widthDots int) (image.Image, error) {
	if widthDots <= 0 || widthDots%8 != 0 {
		return nil, fmt.Errorf("htmlrender: width %d dots is not a positive multiple of 8", widthDots)
	}
	ppi := e.PxPerInch
	if ppi == 0 {
		ppi = WebPxPerInch
	}
	if ppi < 72 || ppi > PrinterDPI {
		return nil, fmt.Errorf("htmlrender: %g CSS pixels per inch is out of range (72 to %g)", ppi, PrinterDPI)
	}
	// Each CSS pixel is scale printer dots: about 2.12 at the web's 96 to
	// the inch. Edge lays the page out in CSS pixels and draws it scale
	// times bigger, so the picture comes out in printer dots.
	scale := PrinterDPI / ppi
	cssWidth := float64(widthDots) / scale // the print width in CSS pixels: 72 mm on 80 mm paper
	page, err := lockdown(doc, widthDots, cssWidth, 1)
	if err != nil {
		return nil, err
	}
	exe, err := e.edgePath()
	if err != nil {
		return nil, err
	}
	timeout, maxH := e.Timeout, e.MaxHeight
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	if maxH == 0 {
		maxH = 12000
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	if e.WorkDir != "" {
		if err := os.MkdirAll(e.WorkDir, 0o700); err != nil {
			return nil, fmt.Errorf("htmlrender: work folder: %w", err)
		}
	}
	dir, err := os.MkdirTemp(e.WorkDir, "doc-*")
	if err != nil {
		return nil, fmt.Errorf("htmlrender: work folder: %w", err)
	}
	defer removeAll(dir)

	pass := 0
	window, windowH := int(math.Ceil(cssWidth))+windowSlack, int(math.Ceil(float64(maxH)/scale))
	shoot := func(page string, windowW int) (image.Image, error) {
		pass++
		return e.shoot(ctx, exe, dir, pass, page, scale, windowW, windowH, timeout)
	}
	img, err := shoot(page, window)
	if err != nil {
		return nil, err
	}
	b := img.Bounds()
	if b.Dx() < widthDots {
		return nil, fmt.Errorf("htmlrender: Edge drew %d dots wide, less than the paper's %d (display scaling?)", b.Dx(), widthDots)
	}
	// Fit to the printable area, as a browser's "Fit to printable area"
	// does: a page with anything wider than the paper is drawn again,
	// shrunk just enough that all of it fits. A page that fits is never
	// enlarged or otherwise changed.
	if right := RightmostInk(img); right >= widthDots {
		if right >= b.Dx()-1 {
			// It reaches the window's edge and may go further: measure it
			// in a window wide enough to see it all.
			wide, err := shoot(page, int(math.Ceil(cssWidth))*3+windowSlack)
			if err != nil {
				return nil, err
			}
			right = RightmostInk(wide)
		}
		zoom := float64(widthDots) / float64(right+1+fitMargin)
		if zoom < MinZoom {
			zoom = MinZoom // smaller is unreadable: print at this size, and what's still too wide is cut off
		}
		fitted, err := lockdown(doc, widthDots, cssWidth/zoom, zoom)
		if err != nil {
			return nil, err
		}
		if img, err = shoot(fitted, window); err != nil {
			return nil, err
		}
		b = img.Bounds()
	}
	// The window is wider than the paper (see windowSlack); keep just the paper.
	return Trim(subImage(img, image.Rect(b.Min.X, b.Min.Y, b.Min.X+widthDots, b.Max.Y)), maxH)
}

// MinZoom is the most a page is shrunk to fit the paper: below about half
// size a bill's text is too small to read.
const MinZoom = 0.5

// fitMargin is the room, in dots, left at the right of a page shrunk to fit.
const fitMargin = 8

// RightmostInk is the x of the rightmost dark pixel, relative to the
// picture's left edge, or -1 if there is none.
func RightmostInk(img image.Image) int {
	b := img.Bounds()
	for x := b.Max.X - 1; x >= b.Min.X; x-- {
		for y := b.Min.Y; y < b.Max.Y; y++ {
			if Ink(img, x, y) {
				return x - b.Min.X
			}
		}
	}
	return -1
}

// shoot has Edge draw page in a window windowW by windowH CSS pixels, scale
// dots to the pixel, and returns the picture (in dots). Each pass gets its
// own files and profile, so a pass never meets the last one's Edge closing.
func (e *Edge) shoot(ctx context.Context, exe, dir string, pass int, page string, scale float64, windowW, windowH int, timeout time.Duration) (image.Image, error) {
	pagePath := filepath.Join(dir, fmt.Sprintf("page-%d.html", pass))
	shotPath := filepath.Join(dir, fmt.Sprintf("shot-%d.png", pass))
	if err := os.WriteFile(pagePath, []byte(page), 0o600); err != nil {
		return nil, fmt.Errorf("htmlrender: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, Args(windowW, windowH, scale, shotPath, filepath.Join(dir, fmt.Sprintf("profile-%d", pass)), fileURL(pagePath))...)
	var stderr tailBuffer
	cmd.Stderr = &stderr
	cmd.WaitDelay = 2 * time.Second
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("htmlrender: Edge took longer than %s", timeout)
	}

	// Since Edge 158 the msedge.exe we start hands the work to another
	// process and exits before the picture is written, so wait for a whole
	// picture to appear (within the same time limit) instead of reading the
	// file the moment it exits.
	data, err := waitForShot(ctx, shotPath)
	if err != nil {
		// Edge exits 0 even when it draws nothing, so the missing file is
		// the real signal. Its last log line usually says why.
		why := stderr.lastLine()
		if runErr != nil {
			why = runErr.Error() + "; " + why
		}
		return nil, fmt.Errorf("htmlrender: Edge drew nothing (%s)", strings.TrimSpace(why))
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("htmlrender: reading Edge's picture: %w", err)
	}
	return img, nil
}

// windowSlack is how much wider than the paper Edge's window is. Edge won't
// make a window narrower than about 520 px, and loses some width to the
// window's frame, so a 384-dot window lays the page out about 490 px wide.
// Instead the window is made comfortably wider, the page's <html> is pinned
// to the paper's width (see Lockdown), and the picture is cropped to it.
const windowSlack = 240

// Args are the command-line arguments Edge runs with: a window width by
// height CSS pixels, drawn scale dots to the pixel.
func Args(width, height int, scale float64, shotPath, profileDir, url string) []string {
	return []string{
		"--headless=new",
		"--disable-gpu",
		"--hide-scrollbars",
		fmt.Sprintf("--force-device-scale-factor=%.6f", scale),
		fmt.Sprintf("--window-size=%d,%d", width, height),
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-extensions",
		"--disable-sync",
		"--disable-background-networking",
		"--disable-component-update",
		"--mute-audio",
		// Every network request goes to a proxy that isn't there, loopback
		// included ("<-loopback>" removes the built-in loopback exception).
		"--proxy-server=http://127.0.0.1:9",
		"--proxy-bypass-list=<-loopback>",
		"--user-data-dir=" + profileDir,
		"--screenshot=" + shotPath,
		url,
	}
}

// Trim cuts the picture just below its last dark row, keeping a few dots of
// margin. A picture whose ink reaches the bottom row was cut off by the
// window, so it is refused.
func Trim(img image.Image, maxH int) (image.Image, error) {
	b := img.Bounds()
	last := -1
	for y := b.Max.Y - 1; y >= b.Min.Y && last < 0; y-- {
		for x := b.Min.X; x < b.Max.X; x++ {
			if Ink(img, x, y) {
				last = y
				break
			}
		}
	}
	if last < 0 {
		return nil, errors.New("htmlrender: the document drew nothing but white")
	}
	if last == b.Max.Y-1 {
		return nil, fmt.Errorf("htmlrender: the document is longer than %d dots (%.0f cm) or has a dark background to the end",
			maxH, float64(maxH)/80)
	}
	end := min(last+1+16, b.Max.Y)
	return subImage(img, image.Rect(b.Min.X, b.Min.Y, b.Max.X, end)), nil
}

// Ink reports whether a pixel prints as a black dot: darker than mid-grey.
func Ink(img image.Image, x, y int) bool {
	r, g, bl, _ := img.At(x, y).RGBA()
	// Rec. 601 luma on 16-bit channels.
	lum := (299*r + 587*g + 114*bl) / 1000
	return lum < 0x8000
}

func subImage(img image.Image, r image.Rectangle) image.Image {
	if s, ok := img.(interface {
		SubImage(image.Rectangle) image.Image
	}); ok {
		return s.SubImage(r)
	}
	return img
}

// Lockdown parses doc and returns it with everything that can run code,
// load anything or navigate removed, and the drawing policy first in <head>.
//
// It also fits the page to the paper: <html> is pinned to widthDots CSS
// pixels, and gets the class "paper-80" (above 432 dots) or "paper-58" (up to
// 432), plus "paper-512dots" and the like on a printer that isn't 576 or 384
// dots across, and a data-paper-dots attribute, so a design can adapt to the roll with
// rules like "html.paper-58 body { font-size: 15px }". Media queries on
// width don't see the paper, because Edge's window is wider than it.
func Lockdown(doc string, widthDots int) (string, error) {
	return lockdown(doc, widthDots, float64(widthDots), 1)
}

// lockdown is Lockdown with <html> pinned to cssWidth CSS pixels (the
// paper's print width at the drawing scale: 272.1 px for 576 dots at the
// web's 96 pixels to the inch, which is exactly 72 mm) and zoomed by zoom,
// for a page shrunk to fit (cssWidth * zoom is then the print width).
func lockdown(doc string, widthDots int, cssWidth, zoom float64) (string, error) {
	root, err := html.Parse(strings.NewReader(strings.TrimPrefix(doc, "\uFEFF")))
	if err != nil {
		return "", fmt.Errorf("htmlrender: parsing the document: %w", err)
	}
	strip(root)
	printMedia(root)
	head := find(root, atom.Head)
	if head == nil { // html.Parse always makes one; be safe anyway
		return "", errors.New("htmlrender: the document has no <head>")
	}
	csp := &html.Node{Type: html.ElementNode, DataAtom: atom.Meta, Data: "meta", Attr: []html.Attribute{
		{Key: "http-equiv", Val: "Content-Security-Policy"}, {Key: "content", Val: Policy}}}
	charset := &html.Node{Type: html.ElementNode, DataAtom: atom.Meta, Data: "meta", Attr: []html.Attribute{
		{Key: "charset", Val: "utf-8"}}}
	fit := &html.Node{Type: html.ElementNode, DataAtom: atom.Style, Data: "style"}
	// Nothing is hidden at the side, so Render can see what is wider than
	// the paper and shrink the page to fit; the picture is cropped to the
	// paper afterwards. A white ground keeps a coloured body from reading
	// as ink across the whole window.
	css := fmt.Sprintf("html{width:%spx!important;min-width:0!important;max-width:none!important;"+
		"overflow:visible!important;background:#fff!important",
		strconv.FormatFloat(math.Round(cssWidth*1000)/1000, 'f', -1, 64))
	if zoom != 1 {
		css += fmt.Sprintf(";zoom:%.5f!important", zoom)
	}
	fit.AppendChild(&html.Node{Type: html.TextNode, Data: css + "}"})
	head.InsertBefore(fit, head.FirstChild)
	head.InsertBefore(csp, head.FirstChild)
	head.InsertBefore(charset, head.FirstChild)
	if root := find(root, atom.Html); root != nil {
		// The roll's class first, which designs are written for; a printer
		// of another width (512 dots on an Epson TM-T88, 832 on 112 mm)
		// also gets its exact width, as paper-512dots.
		class := "paper-80"
		if widthDots <= 432 {
			class = "paper-58"
		}
		setAttr(root, "data-paper-dots", fmt.Sprint(widthDots))
		addClass(root, class)
		if widthDots != 384 && widthDots != 576 {
			addClass(root, fmt.Sprintf("paper-%ddots", widthDots))
		}
	}

	var out bytes.Buffer
	if err := html.Render(&out, root); err != nil {
		return "", fmt.Errorf("htmlrender: %w", err)
	}
	return out.String(), nil
}

// mediaPrint and mediaScreen find the media queries a browser applies when
// printing and when showing a page.
var (
	mediaPrint  = regexp.MustCompile(`(?i)@media\s+(only\s+)?print\b`)
	mediaScreen = regexp.MustCompile(`(?i)@media\s+(only\s+)?screen\b`)
)

// printMedia makes the page's print styles apply, as when a browser prints
// it: rules for print are switched on and rules for the screen off, in
// <style> elements and their media attributes. Edge draws the page as a
// screen would, so without this a design's print rules would be ignored.
func printMedia(n *html.Node) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && strings.EqualFold(c.Data, "style") {
			for i, a := range c.Attr {
				if strings.EqualFold(a.Key, "media") {
					m := strings.ToLower(strings.TrimSpace(a.Val))
					switch {
					case strings.Contains(m, "print"):
						c.Attr[i].Val = "all"
					case strings.Contains(m, "screen"):
						c.Attr[i].Val = "speech"
					}
				}
			}
			for t := c.FirstChild; t != nil; t = t.NextSibling {
				if t.Type == html.TextNode {
					t.Data = mediaPrint.ReplaceAllString(t.Data, "@media all")
					// "speech" matches nothing here; "not all" would turn
					// "screen and (min-width: 900px)" into a rule that applies.
					t.Data = mediaScreen.ReplaceAllString(t.Data, "@media speech")
				}
			}
		}
		printMedia(c)
	}
}

// removed are elements dropped with everything inside them. Matched by name
// in any namespace, so <svg><script> goes too.
var removed = map[string]bool{
	"script": true, "iframe": true, "frame": true, "frameset": true, "object": true,
	"embed": true, "applet": true, "base": true, "link": true, "portal": true,
	"fencedframe": true,
}

func strip(n *html.Node) {
	for c := n.FirstChild; c != nil; {
		next := c.NextSibling
		if c.Type == html.ElementNode {
			name := strings.ToLower(c.Data)
			if removed[name] || (name == "meta" && hasAttr(c, "http-equiv")) || (name == "meta" && hasAttr(c, "charset")) {
				n.RemoveChild(c)
				c = next
				continue
			}
			attrs := c.Attr[:0]
			for _, a := range c.Attr {
				if !strings.HasPrefix(strings.ToLower(a.Key), "on") {
					attrs = append(attrs, a)
				}
			}
			c.Attr = attrs
		}
		strip(c)
		c = next
	}
}

func setAttr(n *html.Node, key, val string) {
	for i, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			n.Attr[i].Val = val
			return
		}
	}
	n.Attr = append(n.Attr, html.Attribute{Key: key, Val: val})
}

func addClass(n *html.Node, class string) {
	for i, a := range n.Attr {
		if strings.EqualFold(a.Key, "class") {
			n.Attr[i].Val = strings.TrimSpace(a.Val + " " + class)
			return
		}
	}
	n.Attr = append(n.Attr, html.Attribute{Key: "class", Val: class})
}

func hasAttr(n *html.Node, key string) bool {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return true
		}
	}
	return false
}

func find(n *html.Node, a atom.Atom) *html.Node {
	if n.Type == html.ElementNode && n.DataAtom == a {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if f := find(c, a); f != nil {
			return f
		}
	}
	return nil
}

// Find returns where Edge is, or ErrNoEdge.
func (e *Edge) Find() (string, error) { return e.edgePath() }

func (e *Edge) edgePath() (string, error) {
	if e.Path != "" {
		if _, err := os.Stat(e.Path); err != nil {
			return "", fmt.Errorf("htmlrender: edge_path %s: %w", e.Path, err)
		}
		return e.Path, nil
	}
	var dirs []string
	for _, v := range []string{"ProgramFiles(x86)", "ProgramFiles", "LocalAppData"} {
		if d := os.Getenv(v); d != "" {
			dirs = append(dirs, d)
		}
	}
	for _, d := range dirs {
		p := filepath.Join(d, "Microsoft", "Edge", "Application", "msedge.exe")
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", ErrNoEdge
}

func fileURL(path string) string {
	return (&url.URL{Scheme: "file", Path: "/" + filepath.ToSlash(path)}).String()
}

// removeAll deletes a work folder. Edge's helper processes can hold files for
// a moment after the main process exits, so it tries a few times.
// shotPoll is how often waitForShot looks for Edge's picture.
const shotPoll = 50 * time.Millisecond

// waitForShot returns the picture at path once it is complete: a file that
// decodes as a PNG and has stopped growing. It gives up when ctx ends and
// returns the last read error (os.ErrNotExist if nothing ever appeared).
func waitForShot(ctx context.Context, path string) ([]byte, error) {
	var last []byte
	for {
		data, err := os.ReadFile(path)
		if err == nil && len(data) > 0 && bytes.Equal(data, last) {
			if _, derr := png.Decode(bytes.NewReader(data)); derr == nil {
				return data, nil
			}
		}
		if err == nil {
			last = data
		} else {
			last = nil
		}
		select {
		case <-ctx.Done():
			if err == nil {
				err = errors.New("the picture was never complete")
			}
			return nil, err
		case <-time.After(shotPoll):
		}
	}
}

func removeAll(dir string) {
	for i := 0; i < 10; i++ {
		if os.RemoveAll(dir) == nil {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// tailBuffer keeps the last 4 KiB written to it: enough for Edge's final log
// lines without holding everything it prints.
type tailBuffer struct{ b []byte }

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > 4096 {
		t.b = t.b[len(t.b)-4096:]
	}
	return len(p), nil
}

func (t *tailBuffer) lastLine() string {
	lines := strings.Split(strings.TrimSpace(string(t.b)), "\n")
	if len(lines) == 0 || lines[len(lines)-1] == "" {
		return "no log output"
	}
	return lines[len(lines)-1]
}
