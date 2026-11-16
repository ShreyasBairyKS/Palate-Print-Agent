// Package fileacl restricts who can open the agent's secret files (the
// credential and the ledger).
//
// DPAPI at machine scope stops a copied file being read on another computer,
// but any account on the same till can decrypt it. %ProgramData% lets every
// local user read files by default, so the files themselves must say who may
// open them: SYSTEM, Administrators, and the account running the agent.
package fileacl

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	advapi32              = syscall.NewLazyDLL("advapi32.dll")
	procConvertStringSD   = advapi32.NewProc("ConvertStringSecurityDescriptorToSecurityDescriptorW")
	procConvertSDToString = advapi32.NewProc("ConvertSecurityDescriptorToStringSecurityDescriptorW")
	procSetFileSecurity   = advapi32.NewProc("SetFileSecurityW")
	procGetFileSecurity   = advapi32.NewProc("GetFileSecurityW")
	kernel32              = syscall.NewLazyDLL("kernel32.dll")
	procLocalFree         = kernel32.NewProc("LocalFree")
)

const (
	sddlRevision1             = 1
	daclSecurityInformation   = 0x00000004
	protectedDaclSecurityInfo = 0x80000000
)

// Restrict replaces the file's access list with full control for SYSTEM,
// Administrators and the account running this process, and stops it
// inheriting anything else from its folder. Call it on a new file before
// writing secrets into it.
func Restrict(path string) error {
	sid, err := currentUserSID()
	if err != nil {
		return fmt.Errorf("fileacl: %w", err)
	}
	// P = protected (no inherited entries). FA = full access.
	sddl := "D:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FA;;;" + sid + ")"
	sddlPtr, err := syscall.UTF16PtrFromString(sddl)
	if err != nil {
		return err
	}
	var sd uintptr
	if r, _, err := procConvertStringSD.Call(uintptr(unsafe.Pointer(sddlPtr)), sddlRevision1, uintptr(unsafe.Pointer(&sd)), 0); r == 0 {
		return fmt.Errorf("fileacl: building security descriptor: %w", err)
	}
	defer procLocalFree.Call(sd)
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	if r, _, err := procSetFileSecurity.Call(uintptr(unsafe.Pointer(p)), daclSecurityInformation|protectedDaclSecurityInfo, sd); r == 0 {
		return fmt.Errorf("fileacl: restricting %s: %w", path, err)
	}
	return nil
}

// Describe returns the file's access list in SDDL form, for tests and
// diagnostics.
func Describe(path string) (string, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	var need uint32
	procGetFileSecurity.Call(uintptr(unsafe.Pointer(p)), daclSecurityInformation, 0, 0, uintptr(unsafe.Pointer(&need)))
	if need == 0 {
		return "", fmt.Errorf("fileacl: reading %s: no security descriptor", path)
	}
	buf := make([]byte, need)
	if r, _, err := procGetFileSecurity.Call(uintptr(unsafe.Pointer(p)), daclSecurityInformation,
		uintptr(unsafe.Pointer(&buf[0])), uintptr(need), uintptr(unsafe.Pointer(&need))); r == 0 {
		return "", fmt.Errorf("fileacl: reading %s: %w", path, err)
	}
	var str *uint16
	if r, _, err := procConvertSDToString.Call(uintptr(unsafe.Pointer(&buf[0])), sddlRevision1, daclSecurityInformation,
		uintptr(unsafe.Pointer(&str)), 0); r == 0 {
		return "", fmt.Errorf("fileacl: describing %s: %w", path, err)
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(str)))
	return utf16PtrToString(str), nil
}

func currentUserSID() (string, error) {
	tok, err := syscall.OpenCurrentProcessToken()
	if err != nil {
		return "", err
	}
	defer tok.Close()
	u, err := tok.GetTokenUser()
	if err != nil {
		return "", err
	}
	return u.User.Sid.String()
}

func utf16PtrToString(p *uint16) string {
	var s []uint16
	for ptr := unsafe.Pointer(p); ; ptr = unsafe.Add(ptr, 2) {
		c := *(*uint16)(ptr)
		if c == 0 {
			break
		}
		s = append(s, c)
	}
	return syscall.UTF16ToString(s)
}
