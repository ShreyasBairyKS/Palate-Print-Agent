//go:build !windows

package usbprinter

// On Linux and macOS a "usb" printer is either a printer in CUPS, the print
// system both use (printed with "lp -o raw", so the ESC/POS bytes reach the
// printer untouched by any driver), or a device file such as /dev/usb/lp0
// on Linux, written directly.

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

func spool(name string, data []byte) (int, string, error) {
	if strings.HasPrefix(name, "/dev/") {
		f, err := os.OpenFile(name, os.O_WRONLY, 0)
		if err != nil {
			return 0, "opening printer", err
		}
		n, err := f.Write(data)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return n, "writing", err
		}
		return n, "", nil
	}
	lp, err := exec.LookPath("lp")
	if err != nil {
		return 0, "opening printer", errors.New("the print system (CUPS) isn't installed: no lp command")
	}
	cmd := exec.Command(lp, "-s", "-d", name, "-o", "raw", "-t", "Palate ticket", "-")
	cmd.Stdin = strings.NewReader(string(data))
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			err = errors.New(msg)
		}
		return 0, "sending to the print system", err
	}
	return len(data), "", nil
}

// list returns CUPS's printers, and on Linux any USB printer device files.
func list() ([]string, error) {
	var names []string
	lpstat, lookErr := exec.LookPath("lpstat")
	if lookErr == nil {
		if out, err := exec.Command(lpstat, "-e").Output(); err == nil {
			for _, l := range strings.Split(string(out), "\n") {
				if l = strings.TrimSpace(l); l != "" {
					names = append(names, l)
				}
			}
		}
	}
	devs, _ := filepath.Glob("/dev/usb/lp*")
	sort.Strings(devs)
	names = append(names, devs...)
	if lookErr != nil && len(devs) == 0 {
		return nil, errors.New("the print system (CUPS) isn't installed (no lpstat) and no /dev/usb/lp* printer is plugged in")
	}
	return names, nil
}
