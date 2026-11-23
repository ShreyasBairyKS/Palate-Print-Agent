//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var procGetConsoleProcessList = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleProcessList")

// startedByDoubleClick reports whether Windows opened a console just for this
// program, as it does when someone double-clicks it in Explorer. Run from a
// terminal, the terminal's shell shares the console too.
func startedByDoubleClick() bool {
	var ids [4]uint32
	n, _, _ := procGetConsoleProcessList.Call(uintptr(unsafe.Pointer(&ids[0])), uintptr(len(ids)))
	return n == 1
}

// relaunchElevated starts this program again with administrator rights,
// which shows Windows' "allow this app to make changes" prompt.
func relaunchElevated(args []string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	dir, _ := os.Getwd()
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = syscall.EscapeArg(a)
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(exe)
	params, _ := windows.UTF16PtrFromString(strings.Join(quoted, " "))
	wd, _ := windows.UTF16PtrFromString(dir)
	return windows.ShellExecute(0, verb, file, params, wd, windows.SW_NORMAL)
}

// runVersion runs another copy of the agent's "version" command, without a
// console window flashing up, and returns what it printed.
func runVersion(exe string) (string, error) {
	cmd := exec.Command(exe, "version")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// removeAtRestart asks Windows to delete a file, or an empty folder, at the
// next restart: a running program can't delete itself.
func removeAtRestart(path string) {
	p, err := windows.UTF16PtrFromString(path)
	if err == nil {
		windows.MoveFileEx(p, nil, windows.MOVEFILE_DELAY_UNTIL_REBOOT)
	}
}

// windowsVersion is "Windows 10.0.26200" (Windows 11 reports 10.0 too; its
// build is 22000 or later).
func windowsVersion() string {
	v := windows.RtlGetVersion()
	s := fmt.Sprintf("Windows %d.%d.%d", v.MajorVersion, v.MinorVersion, v.BuildNumber)
	if v.MajorVersion == 10 && v.BuildNumber >= 22000 {
		s += " (Windows 11)"
	}
	return s
}

// desktopDir is the signed-in person's Desktop (which OneDrive may have
// moved).
func desktopDir() string {
	if d, err := windows.KnownFolderPath(windows.FOLDERID_Desktop, 0); err == nil {
		return d
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, "Desktop")
}
