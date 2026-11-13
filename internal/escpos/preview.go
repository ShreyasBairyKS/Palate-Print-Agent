package escpos

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"strings"
)

// Line is one printed line of a ticket, as a person would see it on paper.
// It is what the demo page draws, so a ticket can be checked without a
// real printer.
type Line struct {
	Kind    string `json:"kind"` // "text", "image" or "cut"
	Text    string `json:"text,omitempty"`
	Bold    bool   `json:"bold,omitempty"`
	Double  bool   `json:"double,omitempty"`
	Reverse bool   `json:"reverse,omitempty"`
	Align   string `json:"align,omitempty"` // "left", "centre" or "right"
	Image   string `json:"image,omitempty"` // PNG data URI of a raster line
	Width   int    `json:"width,omitempty"` // raster width in dots
}

// Preview turns an ESC/POS stream back into printed lines: text decoded from
// code page WPC1252 with its style, raster images as PNGs, and cuts. It
// follows the commands Render uses; anything else is ignored.
func Preview(data []byte) []Line {
	var out []Line
	type style struct {
		bold, double, reverse bool
		align                 string
	}
	st := style{align: "left"}
	var buf strings.Builder
	pending := false // text since the last line feed
	lineStyle := st

	flush := func() {
		out = append(out, Line{Kind: "text", Text: buf.String(), Bold: lineStyle.bold,
			Double: lineStyle.double, Reverse: lineStyle.reverse, Align: lineStyle.align})
		buf.Reset()
		pending = false
	}

	for _, c := range Decode(data) {
		a := c.Raw
		switch c.Name {
		case "ESC @":
			st = style{align: "left"}
		case "ESC E":
			st.bold = a[2]&1 == 1
		case "ESC !":
			st.bold = a[2]&0x08 != 0
			st.double = a[2]&0x30 != 0
		case "GS !":
			st.double = a[2] != 0
		case "GS B":
			st.reverse = a[2]&1 == 1
		case "ESC a":
			switch a[2] {
			case 1, '1':
				st.align = "centre"
			case 2, '2':
				st.align = "right"
			default:
				st.align = "left"
			}
		case "text":
			if !pending {
				lineStyle = st
				pending = true
			}
			buf.WriteString(from1252(a))
		case "LF":
			if !pending {
				lineStyle = st
			}
			flush()
		case "ESC d":
			if pending {
				flush()
			}
			for i := 0; i < int(a[2]) && i < 3; i++ {
				out = append(out, Line{Kind: "text", Align: "left"})
			}
		case "GS v 0":
			if pending {
				flush()
			}
			if img, w := rasterPNG(a); img != "" {
				out = append(out, Line{Kind: "image", Image: img, Width: w, Align: st.align})
			}
		case "GS V":
			if pending {
				flush()
			}
			out = append(out, Line{Kind: "cut"})
		}
	}
	if pending {
		flush()
	}
	return out
}

// rasterPNG converts one GS v 0 command to a PNG data URI.
func rasterPNG(raw []byte) (string, int) {
	if len(raw) < 8 {
		return "", 0
	}
	x := int(raw[4]) | int(raw[5])<<8
	h := int(raw[6]) | int(raw[7])<<8
	if x == 0 || h == 0 || len(raw) < 8+x*h {
		return "", 0
	}
	w := x * 8
	img := image.NewGray(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		row := raw[8+y*x : 8+(y+1)*x]
		for px := 0; px < w; px++ {
			v := uint8(255)
			if row[px/8]&(0x80>>(px%8)) != 0 {
				v = 0
			}
			img.Pix[y*img.Stride+px] = v
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		return "", 0
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(b.Bytes()), w
}

// cp1252 maps Windows-1252's 0x80-0x9F to Unicode; 0xA0-0xFF are Latin-1.
var cp1252 = map[byte]rune{
	0x80: '€', 0x82: '‚', 0x83: 'ƒ', 0x84: '„', 0x85: '…', 0x86: '†', 0x87: '‡',
	0x88: 'ˆ', 0x89: '‰', 0x8A: 'Š', 0x8B: '‹', 0x8C: 'Œ', 0x8E: 'Ž',
	0x91: '‘', 0x92: '’', 0x93: '“', 0x94: '”', 0x95: '•', 0x96: '–', 0x97: '—',
	0x98: '˜', 0x99: '™', 0x9A: 'š', 0x9B: '›', 0x9C: 'œ', 0x9E: 'ž', 0x9F: 'Ÿ',
}

func from1252(b []byte) string {
	var sb strings.Builder
	for _, c := range b {
		switch {
		case c < 0x80, c >= 0xA0:
			sb.WriteRune(rune(c))
		default:
			if r, ok := cp1252[c]; ok {
				sb.WriteRune(r)
			} else {
				sb.WriteRune('?')
			}
		}
	}
	return sb.String()
}
