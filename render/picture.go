package render

import (
	"errors"
	"fmt"
	"image"
	"strings"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/job"
)

// Banners are the marks a reprinted, moved or late job carries. Kitchen
// tickets and documents print the same ones, in the same order, at the top.
type Banners struct {
	Duplicate       bool   // every reprint is marked DUPLICATE
	DuplicateReason string // e.g. "crash-recovery"
	MovedFrom       string // printer the server moved this job from
	FallbackFrom    string // printer on this till that was down
	Delayed         bool   // job older than the configured age
}

// bandRows is the most rows sent in one GS v 0 command. Many printers have a
// small receive buffer and choke on one tall image, so a long picture goes
// out as a stack of bands.
const bandRows = 256

// MaxPictureRows is the longest picture Picture prints: 150 cm at 8 dots/mm.
const MaxPictureRows = 12000

// Picture turns a finished document, already drawn as an image, into bytes a
// printer will accept: the banners as text, then the image as raster bands,
// then a feed and a cut. Pixels darker than mid-grey print black.
//
// The image should be exactly as wide as the paper (576 dots on 80 mm, 384 on
// 58 mm). A narrower one is centred; a wider one loses its right edge.
func Picture(bn Banners, img image.Image, width job.Width) ([]byte, error) {
	if !width.Valid() {
		return nil, errors.New("render: width must be 58 or 80 mm")
	}
	return PictureDots(bn, img, width.Dots())
}

// PictureDots is Picture for a printer that prints dots across; the image
// should be exactly that wide.
func PictureDots(bn Banners, img image.Image, dots int) ([]byte, error) {
	if !job.ValidDots(dots) {
		return nil, fmt.Errorf("render: %d dots across isn't a printer width (a multiple of 8 from %d to %d)", dots, job.MinDots, job.MaxDots)
	}
	if img == nil || img.Bounds().Empty() {
		return nil, errors.New("render: empty picture")
	}
	if img.Bounds().Dy() > MaxPictureRows {
		return nil, errors.New("render: picture is longer than 150 cm")
	}
	r := &renderer{cols: columns(dots), paperDots: dots}
	b := &r.b
	b.Write(cmdInit)
	b.Write(cmdCodePage)
	r.banners(bn)
	b.Write(cmdAlignLeft)

	bounds := img.Bounds()
	offset := max(0, (r.paperDots-bounds.Dx())/2)
	for top := bounds.Min.Y; top < bounds.Max.Y; top += bandRows {
		h := min(bandRows, bounds.Max.Y-top)
		bm := &bitmap{w: r.paperDots, h: h, bits: make([]byte, r.paperDots/8*h)}
		for y := 0; y < h; y++ {
			for x := bounds.Min.X; x < bounds.Max.X && x-bounds.Min.X+offset < r.paperDots; x++ {
				if dark(img, x, top+y) {
					bm.set(x-bounds.Min.X+offset, y)
				}
			}
		}
		writeRaster(b, bm)
	}

	b.Write(cmdFeedLines)
	b.Write(cmdCut)
	if r.err != nil {
		return nil, r.err
	}
	return b.Bytes(), nil
}

// dark reports whether a pixel prints as a black dot: Rec. 601 luma below
// mid-grey. Transparent pixels count as white paper.
func dark(img image.Image, x, y int) bool {
	rr, g, bl, a := img.At(x, y).RGBA()
	if a < 0x8000 {
		return false
	}
	return (299*rr+587*g+114*bl)/1000 < 0x8000
}

// banners draws the banners in the order the kitchen expects them.
func (r *renderer) banners(bn Banners) {
	b := &r.b
	if bn.Duplicate {
		r.banner("DUPLICATE")
		if bn.DuplicateReason != "" {
			b.Write(cmdAlignCentre)
			r.block(bn.DuplicateReason, r.cols, 0, "", "", style{centre: true})
		}
	}
	if bn.MovedFrom != "" {
		r.banner("MOVED FROM " + strings.ToUpper(bn.MovedFrom))
		b.Write(cmdAlignCentre)
		r.text("check for duplicate")
	}
	if bn.FallbackFrom != "" {
		r.banner("BACKUP FOR " + strings.ToUpper(bn.FallbackFrom))
	}
	if bn.Delayed {
		r.banner("DELAYED")
	}
}

// PaperDots is how many dots wide a picture must be for the paper: 576 on
// 80 mm, 384 on 58 mm.
func PaperDots(w job.Width) int { return w.Dots() }
