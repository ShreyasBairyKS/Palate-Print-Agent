//go:build windows

// Package dpapi wraps Windows DPAPI at machine scope, without third-party
// modules. The agent runs as a service with no user profile, so user-scope
// DPAPI is not an option.
package dpapi

import (
	"errors"
	"syscall"
	"unsafe"
)

// DPAPI through crypt32.dll, without third-party modules.

var (
	crypt32           = syscall.NewLazyDLL("crypt32.dll")
	kernel32          = syscall.NewLazyDLL("kernel32.dll")
	procProtectData   = crypt32.NewProc("CryptProtectData")
	procUnprotectData = crypt32.NewProc("CryptUnprotectData")
	procLocalFree     = kernel32.NewProc("LocalFree")
	errEmptyInput     = errors.New("dpapi: empty input")
)

const (
	cryptprotectUIForbidden  = 0x1
	cryptprotectLocalMachine = 0x4 // machine scope: the service has no user profile
)

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

// Protect encrypts plain at machine scope. entropy ties the blob to one use
// (credential, ledger), so a blob from one can't be swapped in for another.
// It is not a secret: an administrator on the till can still decrypt, which
// the design accepts.
func Protect(plain, entropy []byte) ([]byte, error) {
	if len(plain) == 0 {
		return nil, errEmptyInput
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

// Unprotect decrypts a blob made by Protect with the same entropy.
func Unprotect(blob, entropy []byte) ([]byte, error) {
	if len(blob) == 0 {
		return nil, errEmptyInput
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
