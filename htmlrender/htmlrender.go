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
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
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

	mu sync.Mutex // one document at a time: tills are often old and short of memory
}

// ErrNoEdge means Microsoft Edge wasn't found on this till.
var ErrNoEdge = errors.New("htmlrender: Microsoft Edge not found; set edge_path in the config")

// Render draws doc widthDots wide and returns the picture, trimmed below the
// last ink so no paper is wasted. A document taller than MaxHeight is refused
// rather than printed with its end (usually the total) cut off.
func (e *Edge) Render(ctx context.Context, doc string, widthDots int) (image.Image, error) {
	if widthDots <= 0 || widthDots%8 != 0 {
		return nil, fmt.Errorf("htmlrender: width %d dots is not a positive multiple of 8", widthDots)
	}
	page, err := Lockdown(doc, widthDots)
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

	pagePath := filepath.Join(dir, "page.html")
	shotPath := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(pagePath, []byte(page), 0o600); err != nil {
		return nil, fmt.Errorf("htmlrender: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, Args(widthDots+windowSlack, maxH, shotPath, filepath.Join(dir, "profile"), fileURL(pagePath))...)
	var stderr tailBuffer
	cmd.Stderr = &stderr
	cmd.WaitDelay = 2 * time.Second
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("htmlrender: Edge took longer than %s", timeout)
	}

	data, err := os.ReadFile(shotPath)
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
	b := img.Bounds()
	if b.Dx() < widthDots {
		return nil, fmt.Errorf("htmlrender: Edge drew %d dots wide, less than the paper's %d (display scaling?)", b.Dx(), widthDots)
	}
	// The page itself is pinned to the paper's width; the window is wider
	// (see windowSlack), so keep just the paper.
	return Trim(subImage(img, image.Rect(b.Min.X, b.Min.Y, b.Min.X+widthDots, b.Max.Y)), maxH)
}

// windowSlack is how much wider than the paper Edge's window is. Edge won't
// make a window narrower than about 520 px, and loses some width to the
// window's frame, so a 384-dot window lays the page out about 490 px wide.
// Instead the window is made comfortably wider, the page's <html> is pinned
// to the paper's width (see Lockdown), and the picture is cropped to it.
const windowSlack = 240

// Args are the command-line arguments Edge runs with.
func Args(widthDots, heightDots int, shotPath, profileDir, url string) []string {
	return []string{
		"--headless=new",
		"--disable-gpu",
		"--hide-scrollbars",
		"--force-device-scale-factor=1",
		fmt.Sprintf("--window-size=%d,%d", widthDots, heightDots),
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
// pixels, and gets the class "paper-80" (576 dots) or "paper-58" (384 dots)
// and a data-paper-dots attribute, so a design can adapt to the roll with
// rules like "html.paper-58 body { font-size: 15px }". Media queries on
// width don't see the paper, because Edge's window is wider than it.
func Lockdown(doc string, widthDots int) (string, error) {
	root, err := html.Parse(strings.NewReader(strings.TrimPrefix(doc, "\uFEFF")))
	if err != nil {
		return "", fmt.Errorf("htmlrender: parsing the document: %w", err)
	}
	strip(root)
	head := find(root, atom.Head)
	if head == nil { // html.Parse always makes one; be safe anyway
		return "", errors.New("htmlrender: the document has no <head>")
	}
	csp := &html.Node{Type: html.ElementNode, DataAtom: atom.Meta, Data: "meta", Attr: []html.Attribute{
		{Key: "http-equiv", Val: "Content-Security-Policy"}, {Key: "content", Val: Policy}}}
	charset := &html.Node{Type: html.ElementNode, DataAtom: atom.Meta, Data: "meta", Attr: []html.Attribute{
		{Key: "charset", Val: "utf-8"}}}
	fit := &html.Node{Type: html.ElementNode, DataAtom: atom.Style, Data: "style"}
	fit.AppendChild(&html.Node{Type: html.TextNode, Data: fmt.Sprintf(
		"html{width:%dpx!important;min-width:0!important;max-width:none!important;overflow:hidden!important}", widthDots)})
	head.InsertBefore(fit, head.FirstChild)
	head.InsertBefore(csp, head.FirstChild)
	head.InsertBefore(charset, head.FirstChild)
	if root := find(root, atom.Html); root != nil {
		class := fmt.Sprintf("paper-%ddots", widthDots)
		switch widthDots {
		case 576:
			class = "paper-80"
		case 384:
			class = "paper-58"
		}
		setAttr(root, "data-paper-dots", fmt.Sprint(widthDots))
		addClass(root, class)
	}

	var out bytes.Buffer
	if err := html.Render(&out, root); err != nil {
		return "", fmt.Errorf("htmlrender: %w", err)
	}
	return out.String(), nil
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
