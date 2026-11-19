package render

import (
	"bytes"
	"image"
	"image/color"
	"testing"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/job"
)

func whiteGray(w, h int) *image.Gray {
	img := image.NewGray(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = 0xFF
	}
	return img
}

// bands finds every GS v 0 command and returns its width in bytes and height.
func bands(data []byte) [][2]int {
	var out [][2]int
	for i := 0; i+8 <= len(data); {
		j := bytes.Index(data[i:], []byte{0x1D, 'v', '0', 0})
		if j < 0 {
			break
		}
		k := i + j
		xb := int(data[k+4]) | int(data[k+5])<<8
		h := int(data[k+6]) | int(data[k+7])<<8
		out = append(out, [2]int{xb, h})
		i = k + 8 + xb*h
	}
	return out
}

func TestPictureSendsBandsAtThePapersWidth(t *testing.T) {
	img := whiteGray(576, 600)
	img.SetGray(0, 0, color.Gray{})
	data, err := Picture(Banners{}, img, job.Width80)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(data, cmdInit) || !bytes.HasSuffix(data, cmdCut) {
		t.Fatal("a picture must start with init and end with a cut")
	}
	got := bands(data)
	want := [][2]int{{72, 256}, {72, 256}, {72, 88}}
	if len(got) != len(want) {
		t.Fatalf("bands = %v; want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("bands = %v; want %v", got, want)
		}
	}
	// The top-left dot is black: first byte of the first band's bitmap.
	k := bytes.Index(data, []byte{0x1D, 'v', '0', 0})
	if data[k+8] != 0x80 {
		t.Fatalf("first bitmap byte = %#x; want 0x80 (top-left dot black)", data[k+8])
	}
}

func TestPictureCentresANarrowImageAndCropsAWideOne(t *testing.T) {
	narrow := whiteGray(368, 10) // 384-dot paper: 8 dots margin each side
	narrow.SetGray(0, 0, color.Gray{})
	data, err := Picture(Banners{}, narrow, job.Width58)
	if err != nil {
		t.Fatal(err)
	}
	k := bytes.Index(data, []byte{0x1D, 'v', '0', 0})
	if xb := int(data[k+4]); xb != 48 {
		t.Fatalf("band is %d bytes wide; want 48 for 58 mm", xb)
	}
	if data[k+8] != 0x00 || data[k+9] != 0x80 {
		t.Fatalf("first bytes = %#x %#x; want the image moved 8 dots right", data[k+8], data[k+9])
	}
	if _, err := Picture(Banners{}, whiteGray(800, 10), job.Width80); err != nil {
		t.Fatalf("a wide image should print cropped, got %v", err)
	}
}

func TestPictureBanners(t *testing.T) {
	data, err := Picture(Banners{Duplicate: true, DuplicateReason: "crash-recovery", MovedFrom: "front", FallbackFrom: "bill", Delayed: true},
		whiteGray(576, 10), job.Width80)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"DUPLICATE", "crash-recovery", "MOVED FROM FRONT", "BACKUP FOR BILL", "DELAYED"} {
		if !bytes.Contains(data, []byte(want)) {
			t.Errorf("no %q banner", want)
		}
	}
	if bytes.Index(data, []byte("DUPLICATE")) > bytes.Index(data, []byte{0x1D, 'v', '0', 0}) {
		t.Error("banners must come before the picture")
	}
}

func TestPictureRefusesBadInput(t *testing.T) {
	if _, err := Picture(Banners{}, whiteGray(576, 10), job.Width(70)); err == nil {
		t.Error("a 70 mm width was accepted")
	}
	if _, err := Picture(Banners{}, whiteGray(576, MaxPictureRows+1), job.Width80); err == nil {
		t.Error("a picture longer than 150 cm was accepted")
	}
	if PaperDots(job.Width80) != 576 || PaperDots(job.Width58) != 384 {
		t.Error("paper widths in dots are wrong")
	}
}
