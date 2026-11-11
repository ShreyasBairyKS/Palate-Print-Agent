// Package render is the paper track: it turns one station's slip into the
// exact ESC/POS bytes a printer will accept.
//
// Rules (enforced by arch_test.go at the repo root):
//   - no sockets: this package never imports net or net/http
//   - no config, no credentials: everything it needs is in its arguments
//   - deterministic: the same slip and width give byte-identical output on
//     every till, which is what the golden tests check. In particular it
//     never reads the clock or the time zone: PlacedAt is printed in
//     whatever location the caller gave it.
//
// M1 covers Latin slips: text the printer can print from code page WPC1252.
// Any other character makes Render return an error wrapping ErrNeedsRaster;
// M2 adds the raster path for Kannada, Devanagari and Tamil.
package render

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/job"
)

// ErrNeedsRaster means the slip has characters the printer's code page lacks.
var ErrNeedsRaster = errors.New("render: text needs the raster path (M2)")

// UnsupportedRuneError names the first character that can't be printed as text.
type UnsupportedRuneError struct {
	Field string // "items[2].name", "station", ...
	Rune  rune
}

func (e *UnsupportedRuneError) Error() string {
	return fmt.Sprintf("render: %s contains %q (U+%04X), not in code page WPC1252; needs the raster path (M2)", e.Field, e.Rune, e.Rune)
}

func (e *UnsupportedRuneError) Unwrap() error { return ErrNeedsRaster }

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
	var b bytes.Buffer
	b.Write(cmdInit)
	b.Write(cmdCodePage)

	// Banners first, so nobody misses them.
	if slip.Duplicate {
		banner(&b, "DUPLICATE", cols)
		if slip.DuplicateReason != "" {
			b.Write(cmdAlignCentre)
			lines(&b, wrap(slip.DuplicateReason, cols, 0))
		}
	}
	if slip.MovedFrom != "" {
		banner(&b, "MOVED FROM "+strings.ToUpper(slip.MovedFrom), cols)
		b.Write(cmdAlignCentre)
		lines(&b, []string{"check for duplicate"})
	}
	if slip.Delayed {
		banner(&b, "DELAYED", cols)
	}

	// Station, large and centred. Double width halves the line length.
	b.Write(cmdAlignCentre)
	b.Write(cmdSizeDouble)
	b.Write(cmdBoldOn)
	lines(&b, wrap(strings.ToUpper(slip.Station), cols/2, 0))
	b.Write(cmdBoldOff)
	b.Write(cmdSizeNormal)
	b.Write(cmdAlignLeft)

	table := "Takeaway"
	if slip.Table != "" {
		table = "Table " + slip.Table
	}
	lines(&b, []string{spread(table, "#"+slip.OrderNumber, cols)})
	lines(&b, []string{slip.PlacedAt.Format("02 Jan 2006 15:04")})
	lines(&b, []string{strings.Repeat("-", cols)})

	for _, it := range slip.Items {
		prefix := fmt.Sprintf("%d x ", it.Quantity)
		b.Write(cmdBoldOn)
		lines(&b, wrap(prefix+it.Name, cols, len(prefix)))
		b.Write(cmdBoldOff)
		if it.Notes != "" {
			lines(&b, indent(wrap(it.Notes, cols-len(prefix)-2, 0), strings.Repeat(" ", len(prefix))+"- ", strings.Repeat(" ", len(prefix)+2)))
		}
	}
	lines(&b, []string{strings.Repeat("-", cols)})

	if slip.Notes != "" {
		b.Write(cmdBoldOn)
		lines(&b, wrap("NOTE: "+slip.Notes, cols, 6))
		b.Write(cmdBoldOff)
	}

	b.Write(cmdFeedLines)
	b.Write(cmdCut)

	// Encoding last: the layout works in runes, the printer in WPC1252 bytes.
	return encode(b.Bytes())
}

// checkSlip rejects what can't be printed as text, naming the field.
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
		for _, r := range f.text {
			if r >= 0x20 && r != 0x7F {
				if _, ok := to1252(r); !ok {
					return &UnsupportedRuneError{Field: f.name, Rune: r}
				}
			}
		}
	}
	if s.Station == "" || s.OrderNumber == "" || len(s.Items) == 0 {
		return errors.New("render: slip needs a station, an order number and at least one item")
	}
	return nil
}

func banner(b *bytes.Buffer, text string, cols int) {
	b.Write(cmdAlignCentre)
	b.Write(cmdBoldOn)
	b.Write(cmdReverseOn)
	for _, l := range wrap(text, cols-2, 0) {
		b.WriteString(" " + l + " ")
		b.WriteByte('\n')
	}
	b.Write(cmdReverseOff)
	b.Write(cmdBoldOff)
}

func lines(b *bytes.Buffer, ls []string) {
	for _, l := range ls {
		b.WriteString(l)
		b.WriteByte('\n')
	}
}

// spread puts left and right on one line, or on two if they don't fit.
func spread(left, right string, cols int) string {
	gap := cols - runeLen(left) - runeLen(right)
	if gap < 1 {
		return left + "\n" + strings.Repeat(" ", max(0, cols-runeLen(right))) + right
	}
	return left + strings.Repeat(" ", gap) + right
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

// encode converts the UTF-8 layout to WPC1252. ESC/POS command bytes are all
// below 0x80, so they pass through unchanged. checkSlip has already proved
// every printable rune maps.
func encode(layout []byte) ([]byte, error) {
	out := make([]byte, 0, len(layout))
	for i := 0; i < len(layout); {
		r, size := utf8.DecodeRune(layout[i:])
		if r < 0x80 && size == 1 {
			out = append(out, layout[i])
			i++
			continue
		}
		c, ok := to1252(r)
		if !ok {
			return nil, &UnsupportedRuneError{Field: "layout", Rune: r}
		}
		out = append(out, c)
		i += size
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
