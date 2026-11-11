//go:build windows

package usbprinter

import (
	"syscall"
	"unsafe"
)

var (
	winspool             = syscall.NewLazyDLL("winspool.drv")
	procOpenPrinterW     = winspool.NewProc("OpenPrinterW")
	procClosePrinter     = winspool.NewProc("ClosePrinter")
	procStartDocPrinterW = winspool.NewProc("StartDocPrinterW")
	procEndDocPrinter    = winspool.NewProc("EndDocPrinter")
	procStartPagePrinter = winspool.NewProc("StartPagePrinter")
	procEndPagePrinter   = winspool.NewProc("EndPagePrinter")
	procWritePrinter     = winspool.NewProc("WritePrinter")
	procEnumPrintersW    = winspool.NewProc("EnumPrintersW")
)

type docInfo1 struct {
	docName    *uint16
	outputFile *uint16
	datatype   *uint16
}

type printerInfo4 struct {
	printerName *uint16
	serverName  *uint16
	attributes  uint32
}

const (
	printerEnumLocal       = 0x2
	printerEnumConnections = 0x4
)

// spool sends data as one RAW spooler job. It returns how many bytes the
// spooler accepted and which call failed.
func spool(name string, data []byte) (int, string, error) {
	pname, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return 0, "printer name", err
	}
	var h syscall.Handle
	if r, _, e := procOpenPrinterW.Call(uintptr(unsafe.Pointer(pname)), uintptr(unsafe.Pointer(&h)), 0); r == 0 {
		return 0, "opening printer", e
	}
	defer procClosePrinter.Call(uintptr(h))

	doc := docInfo1{docName: utf16("Palate ticket"), datatype: utf16("RAW")}
	if r, _, e := procStartDocPrinterW.Call(uintptr(h), 1, uintptr(unsafe.Pointer(&doc))); r == 0 {
		return 0, "starting print job", e
	}
	defer procEndDocPrinter.Call(uintptr(h))
	if r, _, e := procStartPagePrinter.Call(uintptr(h)); r == 0 {
		return 0, "starting page", e
	}
	defer procEndPagePrinter.Call(uintptr(h))

	total := 0
	for total < len(data) {
		var n uint32
		r, _, e := procWritePrinter.Call(uintptr(h), uintptr(unsafe.Pointer(&data[total])), uintptr(len(data)-total), uintptr(unsafe.Pointer(&n)))
		total += int(n)
		if r == 0 {
			return total, "writing", e
		}
		if n == 0 {
			return total, "writing", syscall.Errno(29) // ERROR_WRITE_FAULT
		}
	}
	return total, "", nil
}

func list() ([]string, error) {
	flags := uintptr(printerEnumLocal | printerEnumConnections)
	var needed, returned uint32
	procEnumPrintersW.Call(flags, 0, 4, 0, 0, uintptr(unsafe.Pointer(&needed)), uintptr(unsafe.Pointer(&returned)))
	if needed == 0 {
		return nil, nil
	}
	buf := make([]byte, needed)
	r, _, e := procEnumPrintersW.Call(flags, 0, 4, uintptr(unsafe.Pointer(&buf[0])), uintptr(needed),
		uintptr(unsafe.Pointer(&needed)), uintptr(unsafe.Pointer(&returned)))
	if r == 0 {
		return nil, e
	}
	infos := unsafe.Slice((*printerInfo4)(unsafe.Pointer(&buf[0])), returned)
	names := make([]string, 0, returned)
	for _, in := range infos {
		names = append(names, utf16ToString(in.printerName))
	}
	return names, nil
}

func utf16(s string) *uint16 {
	p, _ := syscall.UTF16PtrFromString(s)
	return p
}

func utf16ToString(p *uint16) string {
	if p == nil {
		return ""
	}
	n := 0
	for ptr := unsafe.Pointer(p); *(*uint16)(ptr) != 0; n++ {
		ptr = unsafe.Add(ptr, 2)
	}
	return syscall.UTF16ToString(unsafe.Slice(p, n))
}
