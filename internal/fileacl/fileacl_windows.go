// Package fileacl restricts who can open the agent's secret files (the
// credential and the ledger).
//
// DPAPI at machine scope stops a copied file being read on another computer,
// but any account on the same till can decrypt it. %ProgramData% lets every
// local user read files by default, so the files themselves must say who may
// open them: SYSTEM, Administrators, the account running the agent, and the
// agent service's own account (NT SERVICE\PalatePrintAgent), so a credential
// saved by an administrator at pairing time can be read by the service.
//
// It calls Windows directly through syscall rather than golang.org/x/sys,
// which pulls in package net: the ledger uses this package, and the
// architecture rules keep the ledger off the network.
package fileacl

import (
	"fmt"
	"syscall"
	"unsafe"
)

// advapi32.dll and kernel32.dll are among Windows' KnownDLLs, which always
// load from System32 whatever the search order, so loading them by name is
// safe. (golang.org/x/sys would pull in package net, and the ledger, which
// uses this package, may not depend on it.)
var (
	advapi32              = syscall.NewLazyDLL("advapi32.dll")
	procConvertStringSD   = advapi32.NewProc("ConvertStringSecurityDescriptorToSecurityDescriptorW")
	procConvertSDToString = advapi32.NewProc("ConvertSecurityDescriptorToStringSecurityDescriptorW")
	procSetFileSecurity   = advapi32.NewProc("SetFileSecurityW")
	procGetFileSecurity   = advapi32.NewProc("GetFileSecurityW")
	procSetNamedSecurity  = advapi32.NewProc("SetNamedSecurityInfoW")
	procGetSDDacl         = advapi32.NewProc("GetSecurityDescriptorDacl")
	kernel32              = syscall.NewLazyDLL("kernel32.dll")
	procLocalFree         = kernel32.NewProc("LocalFree")
)

const (
	seFileObject              = 1
	sddlRevision1             = 1
	daclSecurityInformation   = 0x00000004
	protectedDaclSecurityInfo = 0x80000000
)

// Restrict replaces the file's access list with full control for SYSTEM,
// Administrators, the account running this process and the agent service's
// account, and stops it inheriting anything else from its folder. Call it on
// a new file before writing secrets into it.
func Restrict(path string) error { return restrict(path, "") }

// ReadableByLocalUsers is Restrict, plus read-only access for every local
// user. It is for the till's status file, which holds no secrets and which
// the status window reads without administrator rights. Opening it by its
// full path works even though its folder is closed to them: Windows lets
// every account pass through folders it can't list.
func ReadableByLocalUsers(path string) error {
	// FR = file read. BU = BUILTIN\Users.
	return restrict(path, "(A;;FR;;;BU)")
}

func restrict(path, extra string) error {
	sid, err := currentUserSID()
	if err != nil {
		return fmt.Errorf("fileacl: %w", err)
	}
	// P = protected (no inherited entries). FA = full access.
	sddl := "D:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FA;;;" + sid + ")(A;;FA;;;" + ServiceSID(ServiceName) + ")" + extra
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

// RestrictDir locks the agent's data folder to SYSTEM, Administrators and
// the agent service's account, for the folder and everything in it, and
// stops it inheriting the broad read access %ProgramData% gives every user.
// Files inside that inherit their access are updated too; files with their
// own access list (the credential, the ledger) keep it.
func RestrictDir(path string) error {
	sddl := "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;" + ServiceSID(ServiceName) + ")"
	return setNamedDACL(path, sddl, true)
}

// setNamedDACL sets a file or folder's access list from SDDL. Unlike
// SetFileSecurity, SetNamedSecurityInfo passes inheritable entries on to
// what is already inside a folder.
func setNamedDACL(path, sddl string, protected bool) error {
	sddlPtr, err := syscall.UTF16PtrFromString(sddl)
	if err != nil {
		return err
	}
	var sd uintptr
	if r, _, err := procConvertStringSD.Call(uintptr(unsafe.Pointer(sddlPtr)), sddlRevision1, uintptr(unsafe.Pointer(&sd)), 0); r == 0 {
		return fmt.Errorf("fileacl: building security descriptor: %w", err)
	}
	defer procLocalFree.Call(sd)
	var present, defaulted int32
	var dacl uintptr
	if r, _, err := procGetSDDacl.Call(sd, uintptr(unsafe.Pointer(&present)), uintptr(unsafe.Pointer(&dacl)), uintptr(unsafe.Pointer(&defaulted))); r == 0 {
		return fmt.Errorf("fileacl: %w", err)
	}
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	info := uintptr(daclSecurityInformation)
	if protected {
		info |= protectedDaclSecurityInfo
	}
	// SetNamedSecurityInfoW returns its error code instead of setting the last error.
	if r, _, _ := procSetNamedSecurity.Call(uintptr(unsafe.Pointer(p)), seFileObject, info, 0, 0, dacl, 0); r != 0 {
		return fmt.Errorf("fileacl: restricting %s: %w", path, syscall.Errno(r))
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
