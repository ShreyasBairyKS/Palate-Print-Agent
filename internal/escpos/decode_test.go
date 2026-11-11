package escpos

import (
	"bytes"
	"strings"
	"testing"
)

func TestDecodeTypicalTicket(t *testing.T) {
	var b bytes.Buffer
	b.Write([]byte{0x1B, '@'})                     // init
	b.Write([]byte{0x1B, 'a', 1})                  // centre
	b.Write([]byte{0x1D, '!', 0x11})               // 2x2
	b.WriteString("TANDOOR")                       //
	b.WriteByte(0x0A)                              // LF
	b.Write([]byte{0x1B, 'E', 1})                  // bold on
	b.WriteString("2 x Paneer Tikka")              //
	b.WriteByte(0x0A)                              //
	b.Write([]byte{0x1D, 'v', '0', 0, 2, 0, 3, 0}) // raster 16x3 dots
	b.Write(make([]byte, 6))                       // 2*3 bytes of raster data
	b.Write([]byte{0x1D, 'V', 66, 3})              // feed 3 then partial cut
	b.Write([]byte{0x10, 0x04, 4})                 // paper status query

	cmds := Decode(b.Bytes())
	want := []struct{ name, desc string }{
		{"ESC @", "initialise printer"},
		{"ESC a", "align centre"},
		{"GS !", "character size 2x wide, 2x high"},
		{"text", `"TANDOOR"`},
		{"LF", "print and line feed"},
		{"ESC E", "bold on"},
		{"text", `"2 x Paneer Tikka"`},
		{"LF", "print and line feed"},
		{"GS v 0", "raster image 16 x 3 dots (6 bytes, mode 0)"},
		{"GS V", "feed 3 dots, then partial cut"},
		{"DLE EOT", "real-time status query: paper sensor"},
	}
	if len(cmds) != len(want) {
		t.Fatalf("got %d commands, want %d:\n%s", len(cmds), len(want), Format(cmds))
	}
	total := 0
	for i, w := range want {
		if cmds[i].Name != w.name || cmds[i].Desc != w.desc {
			t.Errorf("command %d = %s %q, want %s %q", i, cmds[i].Name, cmds[i].Desc, w.name, w.desc)
		}
		if cmds[i].Offset != total {
			t.Errorf("command %d offset = %d, want %d", i, cmds[i].Offset, total)
		}
		total += len(cmds[i].Raw)
	}
	if total != b.Len() {
		t.Errorf("commands cover %d bytes, stream has %d", total, b.Len())
	}
}

func TestDecodeTruncatedAndUnknown(t *testing.T) {
	// Unknown ESC command, then a raster header that promises more data than exists.
	stream := []byte{0x1B, 'Z', 'h', 'i', 0x1D, 'v', '0', 0, 10, 0, 10, 0, 1, 2, 3}
	cmds := Decode(stream)
	if cmds[0].Name != "unknown" {
		t.Errorf("first command = %s, want unknown", cmds[0].Name)
	}
	last := cmds[len(cmds)-1]
	if last.Name != "truncated" || !strings.Contains(last.Desc, "GS v 0") {
		t.Errorf("last command = %s %q, want truncated GS v 0", last.Name, last.Desc)
	}
	covered := 0
	for _, c := range cmds {
		covered += len(c.Raw)
	}
	if covered != len(stream) {
		t.Errorf("commands cover %d bytes, stream has %d", covered, len(stream))
	}
}

func TestQuoteShowsCodePageBytes(t *testing.T) {
	cmds := Decode([]byte{'C', 'a', 'f', 0x82})
	if got, want := cmds[0].Desc, `"Caf\x82"`; got != want {
		t.Errorf("Desc = %s, want %s", got, want)
	}
}

func TestDecodeNeverPanics(t *testing.T) {
	// Every prefix followed by every byte, cut at every length.
	for _, p := range []byte{0x1B, 0x1D, 0x10, 0x1C} {
		for c := 0; c < 256; c++ {
			seq := []byte{p, byte(c), 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}
			for n := 0; n <= len(seq); n++ {
				Decode(seq[:n])
			}
		}
	}
}

func FuzzDecode(f *testing.F) {
	f.Add([]byte{0x1B, '@', 'h', 'i', 0x0A, 0x1D, 'V', 0})
	f.Fuzz(func(t *testing.T, b []byte) {
		covered := 0
		for _, c := range Decode(b) {
			covered += len(c.Raw)
		}
		if covered != len(b) {
			t.Fatalf("commands cover %d bytes, stream has %d", covered, len(b))
		}
	})
}
