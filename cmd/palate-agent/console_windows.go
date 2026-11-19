//go:build windows

package main

import (
	"os"
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
