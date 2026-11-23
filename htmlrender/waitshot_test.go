package htmlrender

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func pngBytes(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewGray(image.Rect(0, 0, 64, 32))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// Edge 158's launcher exits before the picture exists; the picture then
// arrives in pieces. waitForShot returns it only once it's whole.
func TestWaitForShotWaitsForALatePicture(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shot.png")
	want := pngBytes(t)
	go func() {
		time.Sleep(300 * time.Millisecond)
		os.WriteFile(path, want[:len(want)/2], 0o600) // half written
		time.Sleep(200 * time.Millisecond)
		os.WriteFile(path, want, 0o600)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := waitForShot(ctx, path)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("waitForShot = %d bytes, %v; want the whole %d-byte picture", len(got), err, len(want))
	}
}

// A picture already there when Edge exits (Edge 157 and earlier) is read at
// once.
func TestWaitForShotReadsAPictureAlreadyThere(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shot.png")
	want := pngBytes(t)
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	got, err := waitForShot(context.Background(), path)
	if err != nil || !bytes.Equal(got, want) || time.Since(start) > time.Second {
		t.Fatalf("waitForShot = %d bytes, %v after %s", len(got), err, time.Since(start))
	}
}

// Nothing ever drawn: it gives up when the time is up, saying the file
// doesn't exist.
func TestWaitForShotGivesUp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shot.png")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := waitForShot(ctx, path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v; want not-exist", err)
	}
	// Garbage that never becomes a PNG isn't accepted either.
	if err := os.WriteFile(path, []byte("not a picture"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel2()
	if _, err := waitForShot(ctx2, path); err == nil {
		t.Fatal("accepted a file that isn't a PNG")
	}
}
