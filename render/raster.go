package render

// The raster path: text the printer's code page can't print (Kannada,
// Devanagari, Tamil) is shaped with HarfBuzz (go-text/typesetting) using the
// bundled Noto fonts, rasterised in pure Go, and sent as a 1-bit image with
// GS v 0.
//
// Everything here is deterministic: the fonts are embedded, nothing reads
// the system's fonts, and the same slip gives the same bytes on every till.

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"math"
	"strings"
	"sync"

	"github.com/go-text/typesetting/di"
	"github.com/go-text/typesetting/font"
	"github.com/go-text/typesetting/font/opentype"
	"github.com/go-text/typesetting/language"
	"github.com/go-text/typesetting/shaping"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"
)

//go:embed fonts/*.ttf
var fontFiles embed.FS

// Raster sizes in printer dots (203 dpi). Font A text is 12 dots per column.
const (
	colDots  = 12
	emDots   = 26 // em size of a normal raster line
	padDots  = 2  // blank rows above and below each raster line
	inkLevel = 0x80
)

// family is one script's regular and bold font.
type family struct {
	script   language.Script
	lang     language.Language
	regular  *font.Font
	bold     *font.Font
	fileStem string
}

var (
	fontsOnce sync.Once
	families  []*family // Latin first: the fallback for common characters
	fontsErr  error
)

func loadFonts() {
	defs := []struct {
		stem   string
		script language.Script
		lang   string
	}{
		{"NotoSans", language.Latin, "en"},
		{"NotoSansKannada", language.Kannada, "kn"},
		{"NotoSansDevanagari", language.Devanagari, "hi"},
		{"NotoSansTamil", language.Tamil, "ta"},
	}
	for _, d := range defs {
		f := &family{script: d.script, lang: language.NewLanguage(d.lang), fileStem: d.stem}
		for _, weight := range []string{"Regular", "Bold"} {
			data, err := fontFiles.ReadFile("fonts/" + d.stem + "-" + weight + ".ttf")
			if err != nil {
				fontsErr = err
				return
			}
			face, err := font.ParseTTF(bytes.NewReader(data))
			if err != nil {
				fontsErr = fmt.Errorf("render: parsing %s-%s: %w", d.stem, weight, err)
				return
			}
			if weight == "Regular" {
				f.regular = face.Font
			} else {
				f.bold = face.Font
			}
		}
		families = append(families, f)
	}
}

// ErrUnsupportedScript wraps errors for characters no bundled font covers.
var ErrUnsupportedScript = errors.New("render: no bundled font covers this text")

// run is a stretch of text in one font.
type run struct {
	start, end int // rune indices
	fam        *family
}

// itemise splits text into runs by font. Characters of a supported script
// use that script's font; common characters (spaces, digits, punctuation)
// and combining marks stay in the current run if its font has them, and
// otherwise fall back to Latin. Anything no font covers is an error.
func itemise(text []rune) ([]run, error) {
	fontsOnce.Do(loadFonts)
	if fontsErr != nil {
		return nil, fontsErr
	}
	latin := families[0]
	var runs []run
	var cur *family
	for i, r := range text {
		var want *family
		sc := language.LookupScript(r)
		for _, f := range families {
			if f.script == sc {
				want = f
				break
			}
		}
		if want == nil {
			// Common or inherited: stay in the current run when possible.
			switch {
			case cur != nil && has(cur, r):
				want = cur
			case has(latin, r):
				want = latin
			case cur == nil:
				// A leading mark or symbol; look ahead for the script it belongs to.
				for _, f := range families[1:] {
					if has(f, r) {
						want = f
						break
					}
				}
			}
		}
		if want == nil || !has(want, r) {
			return nil, fmt.Errorf("%w: %q (U+%04X)", ErrUnsupportedScript, r, r)
		}
		if want != cur {
			runs = append(runs, run{start: i, fam: want})
			cur = want
		}
		runs[len(runs)-1].end = i + 1
	}
	return runs, nil
}

func has(f *family, r rune) bool {
	if r == ' ' || r == 0x200C || r == 0x200D { // space, ZWNJ, ZWJ: shaping handles them
		return true
	}
	_, ok := f.regular.NominalGlyph(r)
	return ok
}

// cluster is the smallest piece a line can break between: one shaped glyph
// cluster.
type cluster struct {
	runeStart int
	space     bool
	advance   fixed.Int26_6
	glyphs    []placed
}

type placed struct {
	face    *font.Face
	scale   float32 // font units to dots at the size it was shaped
	gid     font.GID
	xOff    fixed.Int26_6
	yOff    fixed.Int26_6
	advance fixed.Int26_6
}

// shaped is one paragraph shaped as a single line, ready to break.
type shaped struct {
	clusters []cluster
	ascent   float64 // in dots
	descent  float64 // in dots, positive
}

func shape(text string, bold bool, em int) (*shaped, error) {
	runes := []rune(sanitise(text))
	runs, err := itemise(runes)
	if err != nil {
		return nil, err
	}
	var hb shaping.HarfbuzzShaper
	out := &shaped{}
	for _, rn := range runs {
		f := rn.fam.regular
		if bold {
			f = rn.fam.bold
		}
		face := font.NewFace(f)
		if ext, ok := face.FontHExtents(); ok {
			scale := float64(em) / float64(face.Upem())
			out.ascent = math.Max(out.ascent, float64(ext.Ascender)*scale)
			out.descent = math.Max(out.descent, -float64(ext.Descender)*scale)
		}
		res := hb.Shape(shaping.Input{
			Text: runes, RunStart: rn.start, RunEnd: rn.end,
			Direction: di.DirectionLTR, Face: face, Size: fixed.I(em),
			Script: rn.fam.script, Language: rn.fam.lang,
		})
		// Glyphs sharing a TextIndex form one cluster.
		for _, g := range res.Glyphs {
			p := placed{face: face, scale: float32(em) / float32(face.Upem()), gid: g.GlyphID, xOff: g.XOffset, yOff: g.YOffset, advance: g.Advance}
			n := len(out.clusters)
			if n > 0 && out.clusters[n-1].runeStart == g.TextIndex() && out.clusters[n-1].glyphs[0].face == face {
				c := &out.clusters[n-1]
				c.glyphs = append(c.glyphs, p)
				c.advance += g.Advance
				continue
			}
			out.clusters = append(out.clusters, cluster{
				runeStart: g.TextIndex(),
				space:     runes[g.TextIndex()] == ' ',
				advance:   g.Advance,
				glyphs:    []placed{p},
			})
		}
	}
	return out, nil
}

// rasterLine is one line of clusters to draw, starting x dots in.
type rasterLine struct {
	x        int
	clusters []cluster
}

func width(cs []cluster) int {
	var w fixed.Int26_6
	for _, c := range cs {
		w += c.advance
	}
	return w.Ceil()
}

// breakLines fills lines greedily, breaking at spaces, and between clusters
// when a word is wider than a whole line. first and rest are the line widths
// available in dots.
func breakLines(cs []cluster, first, rest int) [][]cluster {
	// Words: runs of non-space clusters.
	var words [][]cluster
	var w []cluster
	for _, c := range cs {
		if c.space {
			if len(w) > 0 {
				words = append(words, w)
				w = nil
			}
			continue
		}
		w = append(w, c)
	}
	if len(w) > 0 {
		words = append(words, w)
	}
	var space fixed.Int26_6
	for _, c := range cs {
		if c.space {
			space = c.advance
			break
		}
	}

	var lines [][]cluster
	var line []cluster
	room := first
	used := 0
	emit := func() {
		lines = append(lines, line)
		line, used, room = nil, 0, rest
	}
	for _, word := range words {
		for len(word) > 0 {
			ww := width(word)
			gap := 0
			if len(line) > 0 {
				gap = space.Ceil()
			}
			switch {
			case used+gap+ww <= room:
				if gap > 0 {
					line = append(line, cluster{space: true, advance: space})
				}
				line = append(line, word...)
				used += gap + ww
				word = nil
			case len(line) > 0 && ww <= rest:
				emit()
			default:
				// Wider than any line: take as many clusters as fit.
				if gap > 0 {
					line = append(line, cluster{space: true, advance: space})
					used += gap
				}
				n := 0
				for n < len(word) && used+width(word[:n+1]) <= room {
					n++
				}
				if n == 0 && len(line) == 0 {
					n = 1 // a single cluster wider than the paper: draw it clipped
				}
				line = append(line, word[:n]...)
				word = word[n:]
				emit()
			}
		}
	}
	if len(line) > 0 {
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		lines = [][]cluster{nil}
	}
	return lines
}

// bitmap is a 1-bit image as GS v 0 wants it: rows of bytes, MSB first.
type bitmap struct {
	w, h int // w is a multiple of 8
	bits []byte
}

func (b *bitmap) set(x, y int) {
	if x < 0 || y < 0 || x >= b.w || y >= b.h {
		return
	}
	b.bits[y*(b.w/8)+x/8] |= 0x80 >> (x % 8)
}

func (b *bitmap) invert(x0, x1 int) {
	for y := 0; y < b.h; y++ {
		for x := max(0, x0); x < min(b.w, x1); x++ {
			b.bits[y*(b.w/8)+x/8] ^= 0x80 >> (x % 8)
		}
	}
}

// draw rasterises one line of clusters into a bitmap paperDots wide.
func drawLine(ln rasterLine, paperDots int, sh *shaped) *bitmap {
	asc := int(math.Ceil(sh.ascent))
	h := asc + int(math.Ceil(sh.descent)) + 2*padDots
	bm := &bitmap{w: paperDots, h: h, bits: make([]byte, paperDots/8*h)}
	baseline := float32(padDots + asc)
	alpha := image.NewAlpha(image.Rect(0, 0, paperDots, h))
	var z vector.Rasterizer
	dot := fixed.I(ln.x)
	for _, c := range ln.clusters {
		for _, g := range c.glyphs {
			outline, ok := g.face.GlyphData(g.gid).(font.GlyphOutline)
			if ok && len(outline.Segments) > 0 {
				scale := g.scale
				ox := float32(dot+g.xOff) / 64
				oy := baseline - float32(g.yOff)/64
				z.Reset(paperDots, h)
				z.DrawOp = draw.Over
				open := false
				for _, s := range outline.Segments {
					a := s.Args
					px := func(i int) float32 { return ox + a[i].X*scale }
					py := func(i int) float32 { return oy - a[i].Y*scale }
					switch s.Op {
					case opentype.SegmentOpMoveTo:
						if open {
							z.ClosePath()
						}
						z.MoveTo(px(0), py(0))
						open = true
					case opentype.SegmentOpLineTo:
						z.LineTo(px(0), py(0))
					case opentype.SegmentOpQuadTo:
						z.QuadTo(px(0), py(0), px(1), py(1))
					case opentype.SegmentOpCubeTo:
						z.CubeTo(px(0), py(0), px(1), py(1), px(2), py(2))
					}
				}
				if open {
					z.ClosePath()
				}
				z.Draw(alpha, alpha.Bounds(), image.Opaque, image.Point{})
			}
			dot += g.advance
		}
		if len(c.glyphs) == 0 { // a synthetic space between words
			dot += c.advance
		}
	}
	for y := 0; y < h; y++ {
		row := alpha.Pix[y*alpha.Stride : y*alpha.Stride+paperDots]
		for x, a := range row {
			if a >= inkLevel {
				bm.set(x, y)
			}
		}
	}
	return bm
}

// writeRaster appends GS v 0 for one bitmap.
func writeRaster(b *bytes.Buffer, bm *bitmap) {
	x := bm.w / 8
	b.Write([]byte{0x1D, 'v', '0', 0, byte(x), byte(x >> 8), byte(bm.h), byte(bm.h >> 8)})
	b.Write(bm.bits)
}

// rasterBlock lays out text as raster lines: first-line text starts firstX
// dots in, continuation lines restX dots in; centre centres each line;
// reverse prints white on black behind the text.
func rasterBlock(b *bytes.Buffer, text string, paperDots, firstX, restX int, bold, double, centre, reverse bool) error {
	em := emDots
	if double {
		em *= 2
	}
	sh, err := shape(text, bold, em)
	if err != nil {
		return err
	}
	for i, cs := range breakLines(sh.clusters, paperDots-firstX, paperDots-restX) {
		x := restX
		if i == 0 {
			x = firstX
		}
		lw := width(cs)
		if centre {
			x = max(0, (paperDots-lw)/2)
		}
		bm := drawLine(rasterLine{x: x, clusters: cs}, paperDots, sh)
		if reverse {
			bm.invert(x-colDots, x+lw+colDots)
		}
		writeRaster(b, bm)
	}
	return nil
}

// rasterSpread draws left flush left and right flush right on one line, or on
// two lines if they don't fit together.
func rasterSpread(b *bytes.Buffer, left, right string, paperDots int) error {
	sl, err := shape(left, false, emDots)
	if err != nil {
		return err
	}
	sr, err := shape(right, false, emDots)
	if err != nil {
		return err
	}
	lw, rw := width(sl.clusters), width(sr.clusters)
	merged := &shaped{ascent: math.Max(sl.ascent, sr.ascent), descent: math.Max(sl.descent, sr.descent)}
	if lw+colDots+rw <= paperDots {
		gap := cluster{space: true, advance: fixed.I(paperDots - lw - rw)}
		merged.clusters = append(append(append(merged.clusters, sl.clusters...), gap), sr.clusters...)
		writeRaster(b, drawLine(rasterLine{x: 0, clusters: merged.clusters}, paperDots, merged))
		return nil
	}
	if err := rasterBlock(b, left, paperDots, 0, 0, false, false, false, false); err != nil {
		return err
	}
	writeRaster(b, drawLine(rasterLine{x: max(0, paperDots-rw), clusters: sr.clusters}, paperDots, sr))
	return nil
}

// needsRaster reports whether text has characters outside WPC1252.
func needsRaster(text string) bool {
	return strings.IndexFunc(text, func(r rune) bool {
		if r < 0x20 || r == 0x7F {
			return false // control characters become spaces
		}
		_, ok := to1252(r)
		return !ok
	}) >= 0
}
