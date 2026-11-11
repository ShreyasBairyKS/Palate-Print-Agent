//go:build windows

package pair

import (
	"errors"
	"syscall"
	"unsafe"
)

// DPAPI through crypt32.dll, without third-party modules.

var (
	crypt32            = syscall.NewLazyDLL("crypt32.dll")
	kernel32           = syscall.NewLazyDLL("kernel32.dll")
	procProtectData    = crypt32.NewProc("CryptProtectData")
	procUnprotectData  = crypt32.NewProc("CryptUnprotectData")
	procLocalFree      = kernel32.NewProc("LocalFree")
	errEmptyDPAPIInput = errors.New("dpapi: empty input")
)

const (
	cryptprotectUIForbidden  = 0x1
	cryptprotectLocalMachine = 0x4 // machine scope: the service has no user profile
)

// entropy ties blobs to this program, so another application's machine-scope
// DPAPI blob can't be swapped in, and ours can't be read by a tool that
// doesn't know it. It is not a secret: anything running on the till as an
// administrator can still decrypt, which the design accepts.
var entropy = []byte("palate-print-agent/credential/v1")

type dataBlob struct {
	cbData uint32
	pbData *byte
}

func blobOf(b []byte) *dataBlob {
	return &dataBlob{cbData: uint32(len(b)), pbData: &b[0]}
}

func (b *dataBlob) bytes() []byte {
	out := make([]byte, b.cbData)
	copy(out, unsafe.Slice(b.pbData, b.cbData))
	return out
}

func protect(plain []byte) ([]byte, error) {
	if len(plain) == 0 {
		return nil, errEmptyDPAPIInput
	}
	var out dataBlob
	r, _, err := procProtectData.Call(
		uintptr(unsafe.Pointer(blobOf(plain))),
		0, // no description
		uintptr(unsafe.Pointer(blobOf(entropy))),
		0, 0,
		cryptprotectUIForbidden|cryptprotectLocalMachine,
		uintptr(unsafe.Pointer(&out)),
	)
	if r == 0 {
		return nil, err
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(out.pbData)))
	return out.bytes(), nil
}

func unprotect(blob []byte) ([]byte, error) {
	if len(blob) == 0 {
		return nil, errEmptyDPAPIInput
	}
	var out dataBlob
	r, _, err := procUnprotectData.Call(
		uintptr(unsafe.Pointer(blobOf(blob))),
		0,
		uintptr(unsafe.Pointer(blobOf(entropy))),
		0, 0,
		cryptprotectUIForbidden,
		uintptr(unsafe.Pointer(&out)),
	)
	if r == 0 {
		return nil, err
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(out.pbData)))
	return out.bytes(), nil
}
