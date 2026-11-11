// Package render is the paper track: it turns one station's slip into the
// exact ESC/POS bytes a printer will accept.
//
// Rules (enforced by arch_test.go at the repo root):
//   - no sockets: this package never imports net or net/http
//   - no config, no credentials: everything it needs is in its arguments
//   - deterministic: the same slip and width give byte-identical output on
//     every till, which is what the golden tests check. In particular it
//     never reads the clock, the time zone or the system's fonts: PlacedAt is
//     printed in whatever location the caller gave it, and the fonts for the
//     raster path are embedded.
//
// Each paragraph is decided on its own: if every character is in code page
// WPC1252 it goes as text; otherwise (Kannada, Devanagari, Tamil) it is
// shaped with the bundled Noto fonts and sent as a raster image. Text no
// bundled font covers makes Render return an error wrapping
// ErrUnsupportedScript.
package render

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/job"
)

// ESC/POS commands used by the layout.
var (
	cmdInit        = []byte{0x1B, '@'}
	cmdCodePage    = []byte{0x1B, 't', 16} // WPC1252
	cmdAlignLeft   = []byte{0x1B, 'a', 0}
	cmdAlignCentre = []byte{0x1B, 'a', 1}
	cmdBoldOn      = []byte{0x1B, 'E', 1}
	cmdBoldOff     = []byte{0x1B, 'E', 0}
	cmdReverseOn   = []byte{0x1D, 'B', 1}
	cmdReverseOff  = []byte{0x1D, 'B', 0}
	cmdSizeDouble  = []byte{0x1D, '!', 0x11} // 2x wide, 2x high
	cmdSizeNormal  = []byte{0x1D, '!', 0x00}
	cmdFeedLines   = []byte{0x1B, 'd', 3}
	cmdCut         = []byte{0x1D, 'V', 66, 0} // feed to cutter, partial cut
)

// columns is the font A line length for each paper width.
func columns(w job.Width) int {
	if w == job.Width58 {
		return 32
	}
	return 48
}

// renderer carries the output and the first error, so the layout reads
// straight down.
type renderer struct {
	b         bytes.Buffer
	cols      int
	paperDots int
	err       error
}

// Render turns one station's slip into the exact bytes a printer will accept.
// It opens no socket, reads no config, and never sees a credential.
func Render(slip job.Slip, width job.Width) ([]byte, error) {
	if !width.Valid() {
		return nil, errors.New("render: width must be 58 or 80 mm")
	}
	if err := checkSlip(slip); err != nil {
		return nil, err
	}
	cols := columns(width)
	r := &renderer{cols: cols, paperDots: cols * colDots}
	b := &r.b
	b.Write(cmdInit)
	b.Write(cmdCodePage)

	// Banners first, so nobody misses them.
	if slip.Duplicate {
		r.banner("DUPLICATE")
		if slip.DuplicateReason != "" {
			b.Write(cmdAlignCentre)
			r.block(slip.DuplicateReason, cols, 0, "", "", style{centre: true})
		}
	}
	if slip.MovedFrom != "" {
		r.banner("MOVED FROM " + strings.ToUpper(slip.MovedFrom))
		b.Write(cmdAlignCentre)
		r.text("check for duplicate")
	}
	if slip.Delayed {
		r.banner("DELAYED")
	}

	// Station, large and centred. Double width halves the line length.
	b.Write(cmdAlignCentre)
	b.Write(cmdSizeDouble)
	b.Write(cmdBoldOn)
	r.block(strings.ToUpper(slip.Station), cols/2, 0, "", "", style{bold: true, double: true, centre: true})
	b.Write(cmdBoldOff)
	b.Write(cmdSizeNormal)
	b.Write(cmdAlignLeft)

	table := "Takeaway"
	if slip.Table != "" {
		table = "Table " + slip.Table
	}
	r.spread(table, "#"+slip.OrderNumber)
	r.text(slip.PlacedAt.Format("02 Jan 2006 15:04"))
	r.text(strings.Repeat("-", cols))

	for _, it := range slip.Items {
		prefix := fmt.Sprintf("%d x ", it.Quantity)
		b.Write(cmdBoldOn)
		r.block(prefix+it.Name, cols, len(prefix), "", "", style{bold: true})
		b.Write(cmdBoldOff)
		if it.Notes != "" {
			r.block(it.Notes, cols-len(prefix)-2, 0, strings.Repeat(" ", len(prefix))+"- ", strings.Repeat(" ", len(prefix)+2), style{})
		}
	}
	r.text(strings.Repeat("-", cols))

	if slip.Notes != "" {
		b.Write(cmdBoldOn)
		r.block("NOTE: "+slip.Notes, cols, 6, "", "", style{bold: true})
		b.Write(cmdBoldOff)
	}

	b.Write(cmdFeedLines)
	b.Write(cmdCut)
	if r.err != nil {
		return nil, r.err
	}
	return b.Bytes(), nil
}

// style is how a paragraph looks. On the text path the ESC/POS commands
// around the paragraph do the work; the raster path draws it.
type style struct {
	bold, double, centre, reverse bool
}

// block writes one paragraph: wrapped to width columns (continuation lines
// hang further in), the first line prefixed by first and the rest by rest.
func (r *renderer) block(text string, width, hang int, first, rest string, st style) {
	if r.err != nil {
		return
	}
	if !needsRaster(text) {
		for _, l := range indent(wrap(text, width, hang), first, rest) {
			r.text(l)
		}
		return
	}
	scale := 1
	if st.double {
		scale = 2
	}
	// Leading spaces in the prefixes become an indent in dots, so raster
	// lines line up with text lines; the rest of the first prefix ("- ") is
	// drawn as part of the paragraph.
	lead := len(first) - len(strings.TrimLeft(first, " "))
	firstX := lead * colDots * scale
	restX := (len(rest) + hang) * colDots * scale
	r.err = rasterBlock(&r.b, strings.TrimLeft(first, " ")+text, r.paperDots, firstX, restX, st.bold, st.double, st.centre, st.reverse)
}

// banner is a centred, bold, white-on-black line.
func (r *renderer) banner(text string) {
	b := &r.b
	b.Write(cmdAlignCentre)
	b.Write(cmdBoldOn)
	b.Write(cmdReverseOn)
	if needsRaster(text) {
		if r.err == nil {
			r.err = rasterBlock(b, text, r.paperDots, 0, 0, true, false, true, true)
		}
	} else {
		for _, l := range wrap(text, r.cols-2, 0) {
			r.text(" " + l + " ")
		}
	}
	b.Write(cmdReverseOff)
	b.Write(cmdBoldOff)
}

// spread puts left and right on one line, or on two if they don't fit.
func (r *renderer) spread(left, right string) {
	if needsRaster(left) || needsRaster(right) {
		if r.err == nil {
			r.err = rasterSpread(&r.b, left, right, r.paperDots)
		}
		return
	}
	gap := r.cols - runeLen(left) - runeLen(right)
	if gap < 1 {
		r.text(left)
		r.text(strings.Repeat(" ", max(0, r.cols-runeLen(right))) + right)
		return
	}
	r.text(left + strings.Repeat(" ", gap) + right)
}

// text writes one line of WPC1252 text and a line feed. Callers have already
// checked the line needs no raster.
func (r *renderer) text(line string) {
	enc, err := encode(sanitise(line))
	if err != nil && r.err == nil {
		r.err = err
	}
	r.b.Write(enc)
	r.b.WriteByte('\n')
}

// checkSlip rejects slips that can't be printed, naming the field.
func checkSlip(s job.Slip) error {
	fields := []struct{ name, text string }{
		{"station", s.Station}, {"table", s.Table}, {"order_number", s.OrderNumber},
		{"notes", s.Notes}, {"duplicate_reason", s.DuplicateReason}, {"moved_from", s.MovedFrom},
	}
	for i, it := range s.Items {
		fields = append(fields,
			struct{ name, text string }{fmt.Sprintf("items[%d].name", i), it.Name},
			struct{ name, text string }{fmt.Sprintf("items[%d].notes", i), it.Notes})
		if it.Quantity < 1 {
			return fmt.Errorf("render: items[%d] has quantity %d", i, it.Quantity)
		}
	}
	for _, f := range fields {
		if !utf8.ValidString(f.text) {
			return fmt.Errorf("render: %s is not valid UTF-8", f.name)
		}
		if needsRaster(f.text) {
			if _, err := itemise([]rune(sanitise(f.text))); err != nil {
				return fmt.Errorf("render: %s: %w", f.name, err)
			}
		}
	}
	if s.Station == "" || s.OrderNumber == "" || len(s.Items) == 0 {
		return errors.New("render: slip needs a station, an order number and at least one item")
	}
	return nil
}

func indent(ls []string, first, rest string) []string {
	out := make([]string, len(ls))
	for i, l := range ls {
		if i == 0 {
			out[i] = first + l
		} else {
			out[i] = rest + l
		}
	}
	return out
}

// wrap breaks text into lines of at most cols runes, at spaces where it can
// and mid-word where a word is longer than a line. Continuation lines are
// indented by hang spaces. Control characters become spaces, so slip text
// can never smuggle in a printer command.
func wrap(text string, cols, hang int) []string {
	text = sanitise(text)
	if cols < 1 {
		cols = 1
	}
	if hang >= cols {
		hang = 0
	}
	words := strings.Fields(text)
	if len(words) == 0 {
		return []string{""}
	}
	var out []string
	var cur []rune
	prefix, room := "", cols // the first line has no hanging indent
	emit := func() {
		out = append(out, prefix+string(cur))
		cur = nil
		prefix, room = strings.Repeat(" ", hang), cols-hang
	}
	for _, word := range words {
		w := []rune(word)
		for len(w) > 0 {
			switch {
			case len(cur) == 0 && len(w) <= room:
				cur, w = w, nil
			case len(cur) == 0: // longer than a whole line: break it
				cur, w = w[:room], w[room:]
				emit()
			case len(cur)+1+len(w) <= room:
				cur = append(append(cur, ' '), w...)
				w = nil
			case len(w) > cols-hang && room-len(cur)-1 > 0:
				// Too long for any line: start it here rather than leave
				// this line short, and break it.
				n := room - len(cur) - 1
				cur = append(append(cur, ' '), w[:n]...)
				w = w[n:]
				emit()
			default:
				emit()
			}
		}
	}
	if len(cur) > 0 {
		out = append(out, prefix+string(cur))
	}
	return out
}

func sanitise(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7F {
			return ' '
		}
		return r
	}, s)
}

func runeLen(s string) int { return utf8.RuneCountInString(s) }

// encode converts one line of text to WPC1252.
func encode(s string) ([]byte, error) {
	out := make([]byte, 0, len(s))
	for _, r := range s {
		c, ok := to1252(r)
		if !ok {
			return nil, fmt.Errorf("render: %q (U+%04X) is not in code page WPC1252", r, r)
		}
		out = append(out, c)
	}
	return out, nil
}

// cp1252High maps the characters Windows-1252 puts in 0x80–0x9F.
var cp1252High = map[rune]byte{
	'€': 0x80, '‚': 0x82, 'ƒ': 0x83, '„': 0x84, '…': 0x85, '†': 0x86, '‡': 0x87,
	'ˆ': 0x88, '‰': 0x89, 'Š': 0x8A, '‹': 0x8B, 'Œ': 0x8C, 'Ž': 0x8E,
	'‘': 0x91, '’': 0x92, '“': 0x93, '”': 0x94, '•': 0x95, '–': 0x96, '—': 0x97,
	'˜': 0x98, '™': 0x99, 'š': 0x9A, '›': 0x9B, 'œ': 0x9C, 'ž': 0x9E, 'Ÿ': 0x9F,
}

func to1252(r rune) (byte, bool) {
	switch {
	case r < 0x80:
		return byte(r), true
	case r >= 0xA0 && r <= 0xFF:
		return byte(r), true
	}
	c, ok := cp1252High[r]
	return c, ok
}
