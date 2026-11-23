package render

import (
	"bytes"
	"image"
	"image/color"
	"strings"
	"testing"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/internal/escpos"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/job"
)

// otherWidths are printers that aren't the usual 384 or 576 dots: an Epson
// TM-T88 (512), a 112 mm printer (832), and an odd one.
var otherWidths = []int{512, 832, 640}

// On a printer of another width every text line fits it and every picture
// line is exactly as wide as it prints.
func TestOtherWidthsFitThePrinter(t *testing.T) {
	for name, mk := range goldenCases {
		for _, dots := range otherWidths {
			out, err := RenderDots(mk(), dots)
			if err != nil {
				t.Fatalf("%s %d dots: %v", name, dots, err)
			}
			for _, c := range escpos.Decode(out) {
				switch c.Name {
				case "text":
					limit := dots / colDots
					if strings.EqualFold(string(c.Raw), strings.ToUpper(mk().Station)) {
						limit /= 2
					}
					if len(c.Raw) > limit {
						t.Errorf("%s %d dots: %q is %d columns, the printer fits %d", name, dots, c.Raw, len(c.Raw), limit)
					}
				case "GS v 0":
					if x := (int(c.Raw[4]) | int(c.Raw[5])<<8) * 8; x != dots {
						t.Errorf("%s %d dots: raster is %d dots wide", name, dots, x)
					}
				}
			}
		}
	}
}

// The usual widths come out byte for byte as before: RenderDots with 384 or
// 576 is Render with 58 or 80 mm.
func TestUsualDotsAreTheOldWidths(t *testing.T) {
	for name, mk := range goldenCases {
		for _, w := range []job.Width{job.Width58, job.Width80} {
			a, err1 := Render(mk(), w)
			b, err2 := RenderDots(mk(), w.Dots())
			if err1 != nil || err2 != nil || !bytes.Equal(a, b) {
				t.Errorf("%s %s: Render and RenderDots differ (%v, %v)", name, w, err1, err2)
			}
		}
	}
}

func TestBadDotsAreRefused(t *testing.T) {
	for _, dots := range []int{0, 100, 250, 513, 840, 1000} {
		if _, err := RenderDots(baseSlip(), dots); err == nil {
			t.Errorf("RenderDots %d dots: accepted", dots)
		}
		img := image.NewGray(image.Rect(0, 0, 64, 8))
		if _, err := PictureDots(Banners{}, img, dots); err == nil {
			t.Errorf("PictureDots %d dots: accepted", dots)
		}
	}
}

// A bill drawn 512 dots wide prints 512 dots wide.
func TestPictureAtOtherWidths(t *testing.T) {
	for _, dots := range otherWidths {
		img := image.NewGray(image.Rect(0, 0, dots, 40))
		for x := 0; x < dots; x++ {
			img.SetGray(x, 20, color.Gray{})
		}
		out, err := PictureDots(Banners{Delayed: true}, img, dots)
		if err != nil {
			t.Fatalf("%d dots: %v", dots, err)
		}
		bands := 0
		for _, c := range escpos.Decode(out) {
			if c.Name == "GS v 0" {
				bands++
				if x := (int(c.Raw[4]) | int(c.Raw[5])<<8) * 8; x != dots {
					t.Errorf("%d dots: band is %d dots wide", dots, x)
				}
			}
		}
		if bands == 0 {
			t.Errorf("%d dots: no picture printed", dots)
		}
	}
}
