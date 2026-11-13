package escpos

import (
	"os"
	"strings"
	"testing"
)

func TestPreviewOfGoldenTicket(t *testing.T) {
	data, err := os.ReadFile("../../render/testdata/takeaway-banners-58mm.bin")
	if err != nil {
		t.Fatal(err)
	}
	lines := Preview(data)
	find := func(text string) Line {
		for _, l := range lines {
			if strings.TrimSpace(l.Text) == text {
				return l
			}
		}
		t.Fatalf("no line %q in %+v", text, lines)
		return Line{}
	}
	if l := find("DUPLICATE"); !l.Reverse || !l.Bold || l.Align != "centre" {
		t.Errorf("DUPLICATE banner = %+v, want reverse, bold, centred", l)
	}
	if l := find("TANDOOR"); !l.Double || !l.Bold || l.Align != "centre" {
		t.Errorf("station = %+v, want double, bold, centred", l)
	}
	if l := find("2 x Paneer Tikka"); !l.Bold || l.Double || l.Align != "left" {
		t.Errorf("item = %+v, want bold, normal size, left", l)
	}
	if l := find("- less spicy"); l.Bold {
		t.Errorf("item note = %+v, want not bold", l)
	}
	if lines[len(lines)-1].Kind != "cut" {
		t.Errorf("last line = %+v, want the cut", lines[len(lines)-1])
	}
}

func TestPreviewDecodesCodePageAndRaster(t *testing.T) {
	accents, _ := os.ReadFile("../../render/testdata/latin-accents-80mm.bin")
	found := false
	for _, l := range Preview(accents) {
		if strings.Contains(l.Text, "Café crème brûlée") {
			found = true
		}
	}
	if !found {
		t.Error("accented text was not decoded from WPC1252")
	}

	kannada, _ := os.ReadFile("../../render/testdata/kannada-80mm.bin")
	images := 0
	for _, l := range Preview(kannada) {
		if l.Kind == "image" {
			images++
			if !strings.HasPrefix(l.Image, "data:image/png;base64,") || l.Width != 576 {
				t.Errorf("image line = %.60q width %d", l.Image, l.Width)
			}
		}
	}
	if images == 0 {
		t.Error("no raster images in the Kannada ticket preview")
	}
}

func TestPreviewNeverPanics(t *testing.T) {
	for _, b := range [][]byte{nil, {0x1D, 'v', '0'}, {0x1B, 'E'}, {0x1D, 'v', '0', 0, 0, 0, 0, 0}, []byte("plain\n")} {
		Preview(b)
	}
}
