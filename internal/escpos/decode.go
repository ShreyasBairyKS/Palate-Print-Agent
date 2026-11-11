// Package escpos decodes an ESC/POS byte stream into readable commands.
//
// It is a debugging aid, not a printer: it backs escpos-dump and makes golden
// test failures readable. It knows the commands thermal receipt printers
// commonly use; anything else is shown as "unknown" with its raw bytes, and
// decoding carries on.
package escpos

import (
	"fmt"
	"strings"
)

// Command is one decoded element of the stream: a control sequence or a run
// of text.
type Command struct {
	Offset int    // byte offset of Raw[0] in the stream
	Raw    []byte // the exact bytes this command covers
	Name   string // "ESC @", "GS V", "text", "LF", ...
	Desc   string // human reading, e.g. "initialise printer"
}

const (
	esc = 0x1B
	gs  = 0x1D
	dle = 0x10
	fs  = 0x1C
)

// fixed is a command with a fixed number of argument bytes after the two
// command bytes.
type fixed struct {
	name string
	args int
	desc func(a []byte) string
}

func s(text string) func([]byte) string { return func([]byte) string { return text } }

func onOff(what string) func([]byte) string {
	return func(a []byte) string {
		if a[0]&1 == 1 {
			return what + " on"
		}
		return what + " off"
	}
}

var escCommands = map[byte]fixed{
	'@': {"ESC @", 0, s("initialise printer")},
	'!': {"ESC !", 1, printMode},
	'E': {"ESC E", 1, onOff("bold")},
	'G': {"ESC G", 1, onOff("double-strike")},
	'-': {"ESC -", 1, func(a []byte) string {
		switch a[0] {
		case 0, '0':
			return "underline off"
		case 2, '2':
			return "underline on (2 dots)"
		}
		return "underline on (1 dot)"
	}},
	'a': {"ESC a", 1, func(a []byte) string {
		switch a[0] {
		case 0, '0':
			return "align left"
		case 1, '1':
			return "align centre"
		case 2, '2':
			return "align right"
		}
		return fmt.Sprintf("align ?(%d)", a[0])
	}},
	'd': {"ESC d", 1, func(a []byte) string { return fmt.Sprintf("print and feed %d lines", a[0]) }},
	'J': {"ESC J", 1, func(a []byte) string { return fmt.Sprintf("print and feed %d dots", a[0]) }},
	't': {"ESC t", 1, func(a []byte) string { return "code page " + codePage(a[0]) }},
	'R': {"ESC R", 1, func(a []byte) string { return fmt.Sprintf("international charset %d", a[0]) }},
	'M': {"ESC M", 1, func(a []byte) string {
		switch a[0] {
		case 0, '0':
			return "font A"
		case 1, '1':
			return "font B"
		}
		return fmt.Sprintf("font %d", a[0])
	}},
	'2': {"ESC 2", 0, s("default line spacing")},
	'3': {"ESC 3", 1, func(a []byte) string { return fmt.Sprintf("line spacing %d dots", a[0]) }},
	'V': {"ESC V", 1, onOff("rotate 90°")},
	'{': {"ESC {", 1, onOff("upside-down")},
	'p': {"ESC p", 3, func(a []byte) string { return fmt.Sprintf("cash drawer pulse pin %d", a[0]&1+2) }},
	'i': {"ESC i", 0, s("partial cut (legacy)")},
	'm': {"ESC m", 0, s("partial cut (legacy)")},
}

var gsCommands = map[byte]fixed{
	'!': {"GS !", 1, func(a []byte) string {
		return fmt.Sprintf("character size %dx wide, %dx high", a[0]>>4+1, a[0]&0x0F+1)
	}},
	'B': {"GS B", 1, onOff("reverse (white on black)")},
	'L': {"GS L", 2, func(a []byte) string { return fmt.Sprintf("left margin %d dots", le16(a)) }},
	'W': {"GS W", 2, func(a []byte) string { return fmt.Sprintf("print width %d dots", le16(a)) }},
	'h': {"GS h", 1, func(a []byte) string { return fmt.Sprintf("barcode height %d dots", a[0]) }},
	'w': {"GS w", 1, func(a []byte) string { return fmt.Sprintf("barcode module width %d", a[0]) }},
	'H': {"GS H", 1, func(a []byte) string { return fmt.Sprintf("barcode text position %d", a[0]) }},
	'r': {"GS r", 1, func(a []byte) string { return fmt.Sprintf("transmit status %d", a[0]) }},
	'a': {"GS a", 1, func(a []byte) string { return fmt.Sprintf("automatic status back 0x%02x", a[0]) }},
}

func printMode(a []byte) string {
	n := a[0]
	var parts []string
	if n&0x01 != 0 {
		parts = append(parts, "font B")
	} else {
		parts = append(parts, "font A")
	}
	if n&0x08 != 0 {
		parts = append(parts, "bold")
	}
	if n&0x10 != 0 {
		parts = append(parts, "double height")
	}
	if n&0x20 != 0 {
		parts = append(parts, "double width")
	}
	if n&0x80 != 0 {
		parts = append(parts, "underline")
	}
	return "print mode: " + strings.Join(parts, ", ")
}

func codePage(n byte) string {
	names := map[byte]string{
		0: "PC437 (USA)", 2: "PC850 (Multilingual)", 3: "PC860", 16: "WPC1252",
		17: "PC866 (Cyrillic)", 19: "PC858 (Euro)", 255: "user-defined",
	}
	if name, ok := names[n]; ok {
		return fmt.Sprintf("%d %s", n, name)
	}
	return fmt.Sprintf("%d", n)
}

func le16(a []byte) int { return int(a[0]) | int(a[1])<<8 }

// Decode reads the whole stream. It never fails: unknown or truncated
// sequences become commands named "unknown" or "truncated".
func Decode(b []byte) []Command {
	var out []Command
	i := 0
	emit := func(n int, name, desc string) {
		out = append(out, Command{Offset: i, Raw: b[i : i+n], Name: name, Desc: desc})
		i += n
	}
	truncated := func(name string) {
		emit(len(b)-i, "truncated", name+" cut off by end of stream")
	}

	for i < len(b) {
		c := b[i]
		switch {
		case c == 0x0A:
			emit(1, "LF", "print and line feed")
		case c == 0x0D:
			emit(1, "CR", "carriage return")
		case c == 0x09:
			emit(1, "HT", "horizontal tab")
		case c == 0x0C:
			emit(1, "FF", "print and return to standard mode")
		case c == 0x18:
			emit(1, "CAN", "cancel print data in page mode")

		case c == esc || c == gs || c == fs || c == dle:
			if i+1 >= len(b) {
				truncated(prefixName(c))
				continue
			}
			n, name, desc := decodeSequence(b[i:])
			if n == 0 {
				truncated(name)
				continue
			}
			emit(n, name, desc)

		case c >= 0x20 && c != 0x7F:
			j := i
			for j < len(b) && b[j] >= 0x20 && b[j] != 0x7F {
				j++
			}
			emit(j-i, "text", quote(b[i:j]))

		default:
			emit(1, "unknown", fmt.Sprintf("control byte 0x%02x", c))
		}
	}
	return out
}

func prefixName(c byte) string {
	switch c {
	case esc:
		return "ESC"
	case gs:
		return "GS"
	case fs:
		return "FS"
	}
	return "DLE"
}

// decodeSequence decodes the sequence at the start of b (len(b) >= 2). It
// returns n == 0 if the sequence is cut off by the end of the stream.
func decodeSequence(b []byte) (n int, name, desc string) {
	prefix, cmd := b[0], b[1]
	need := func(total int) bool { return len(b) >= total }

	switch prefix {
	case esc:
		if f, ok := escCommands[cmd]; ok {
			if !need(2 + f.args) {
				return 0, f.name, ""
			}
			return 2 + f.args, f.name, f.desc(b[2 : 2+f.args])
		}

	case gs:
		if f, ok := gsCommands[cmd]; ok {
			if !need(2 + f.args) {
				return 0, f.name, ""
			}
			return 2 + f.args, f.name, f.desc(b[2 : 2+f.args])
		}
		switch cmd {
		case 'V':
			if !need(3) {
				return 0, "GS V", ""
			}
			switch m := b[2]; m {
			case 0, '0':
				return 3, "GS V", "full cut"
			case 1, '1':
				return 3, "GS V", "partial cut"
			case 65, 66:
				if !need(4) {
					return 0, "GS V", ""
				}
				kind := "full"
				if m == 66 {
					kind = "partial"
				}
				return 4, "GS V", fmt.Sprintf("feed %d dots, then %s cut", b[3], kind)
			default:
				return 3, "GS V", fmt.Sprintf("cut, unknown mode %d", m)
			}

		case 'v':
			// GS v 0 m xL xH yL yH d1...dk, k = (xL + xH*256) * (yL + yH*256)
			if !need(8) {
				return 0, "GS v 0", ""
			}
			x := le16(b[4:6])
			y := le16(b[6:8])
			total := 8 + x*y
			if !need(total) {
				return 0, "GS v 0", ""
			}
			return total, "GS v 0", fmt.Sprintf("raster image %d x %d dots (%d bytes, mode %d)", x*8, y, x*y, b[3])

		case 'k':
			// GS k m: m 0..6 is NUL-terminated data; m 65..73 is followed by n and n bytes.
			if !need(3) {
				return 0, "GS k", ""
			}
			m := b[2]
			if m <= 6 {
				for j := 3; j < len(b); j++ {
					if b[j] == 0 {
						return j + 1, "GS k", fmt.Sprintf("barcode type %d %q", m, b[3:j])
					}
				}
				return 0, "GS k", ""
			}
			if !need(4) || !need(4+int(b[3])) {
				return 0, "GS k", ""
			}
			return 4 + int(b[3]), "GS k", fmt.Sprintf("barcode type %d %q", m, b[4:4+int(b[3])])

		case '(':
			// GS ( fn pL pH ...: pL + pH*256 bytes follow.
			if !need(5) {
				return 0, "GS (", ""
			}
			size := le16(b[3:5])
			if !need(5 + size) {
				return 0, fmt.Sprintf("GS ( %c", b[2]), ""
			}
			what := "extended command"
			if b[2] == 'k' {
				what = "2D code (QR/PDF417)"
			}
			return 5 + size, fmt.Sprintf("GS ( %c", b[2]), fmt.Sprintf("%s, %d bytes", what, size)
		}

	case dle:
		if cmd == 0x04 {
			if !need(3) {
				return 0, "DLE EOT", ""
			}
			kinds := map[byte]string{1: "printer", 2: "offline", 3: "error", 4: "paper sensor"}
			k, ok := kinds[b[2]]
			if !ok {
				k = fmt.Sprintf("?(%d)", b[2])
			}
			return 3, "DLE EOT", "real-time status query: " + k
		}
		if cmd == 0x05 {
			if !need(3) {
				return 0, "DLE ENQ", ""
			}
			return 3, "DLE ENQ", fmt.Sprintf("real-time request %d", b[2])
		}

	case fs:
		switch cmd {
		case '.':
			return 2, "FS .", "cancel Kanji mode"
		case '&':
			return 2, "FS &", "select Kanji mode"
		}
	}

	return 2, "unknown", fmt.Sprintf("%s 0x%02x (%q), argument bytes may follow as text", prefixName(prefix), cmd, cmd)
}

// quote shows a text run in double quotes, with bytes above 0x7E as \xNN
// because their meaning depends on the selected code page.
func quote(b []byte) string {
	var sb strings.Builder
	sb.WriteByte('"')
	for _, c := range b {
		switch {
		case c == '"' || c == '\\':
			sb.WriteByte('\\')
			sb.WriteByte(c)
		case c >= 0x20 && c < 0x7F:
			sb.WriteByte(c)
		default:
			fmt.Fprintf(&sb, `\x%02x`, c)
		}
	}
	sb.WriteByte('"')
	return sb.String()
}

// Format renders commands as a dump: offset, hex (shortened past 8 bytes),
// command name and reading, one command per line.
func Format(cmds []Command) string {
	var sb strings.Builder
	for _, c := range cmds {
		fmt.Fprintf(&sb, "%06x  %-34s %-9s %s\n", c.Offset, hexBytes(c.Raw), c.Name, c.Desc)
	}
	return sb.String()
}

func hexBytes(b []byte) string {
	const show = 8
	n := len(b)
	if n > show {
		n = show
	}
	parts := make([]string, n)
	for i := 0; i < n; i++ {
		parts[i] = fmt.Sprintf("%02x", b[i])
	}
	h := strings.Join(parts, " ")
	if len(b) > show {
		h += fmt.Sprintf(" …+%d", len(b)-show)
	}
	return h
}
