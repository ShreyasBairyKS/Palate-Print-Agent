package usbprinter

import (
	"context"
	"runtime"
	"testing"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/printer"
)

// These tests never send a job to a real printer: they list printers (read
// only) and write to one that doesn't exist.

func TestList(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("needs the Windows print spooler")
	}
	names, err := List()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("printers visible to this process: %q", names)
	for _, n := range names {
		if n == "" {
			t.Fatal("an empty printer name came back from the spooler")
		}
	}
}

func TestMissingPrinterIsRefused(t *testing.T) {
	w := &Writer{Name: "Palate test printer that does not exist 7f3a"}
	_, err := w.Write(context.Background(), []byte("\x1b@hello\n"))
	if printer.KindOf(err) != printer.Refused {
		t.Fatalf("err = %v, want refused", err)
	}
}
