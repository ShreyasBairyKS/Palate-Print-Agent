//go:build !windows

package main

// Where the agent lives on Linux and macOS, and how its messages say so.
//
//	program:  /opt/palate-print-agent/palate-agent (Linux)
//	          /usr/local/palate-print-agent/palate-agent (macOS)
//	          with a link /usr/local/bin/palate-agent, so "palate-agent" works in a terminal
//	data:     /var/lib/palate-print-agent/ (Linux)
//	          /Library/Application Support/PalatePrintAgent/ (macOS)

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	adminHint         = "run it again with sudo in front (sudo palate-agent ...)"
	asAdmin           = "(with sudo)"
	serviceWords      = "the background service"
	startsWith        = "starts when this computer starts"
	browserName       = "a Chromium browser (Google Chrome, Chromium or Microsoft Edge)"
	browserShort      = "Browser"
	printerSystem     = "printers in this computer's print system (CUPS)"
	printerNameHint   = "printer name in CUPS, or /dev/usb/lp0"
	elevateSay        = "It asks for your password (sudo), then carries on here."
	elevateFailHow    = "Run it with sudo: sudo palate-agent setup"
	pauseAfterElevate = false
	sayAdmin          = "Run it again with sudo in front: sudo palate-agent ..."
	setUpHow          = "run \"sudo palate-agent setup\" in a terminal"
)

var osName = map[string]string{"darwin": "macOS", "linux": "Linux"}[runtime.GOOS]

func init() {
	if osName == "" {
		osName = runtime.GOOS
	}
}

// commandLink is where install links the program, so it runs by name.
const commandLink = "/usr/local/bin/palate-agent"

func defaultConfigPath() string {
	if runtime.GOOS == "darwin" {
		return "/Library/Application Support/PalatePrintAgent/agent.json"
	}
	return "/var/lib/palate-print-agent/agent.json"
}

func installedExe() string {
	if runtime.GOOS == "darwin" {
		return "/usr/local/palate-print-agent/palate-agent"
	}
	return "/opt/palate-print-agent/palate-agent"
}

// dataFilesForService are files root made during setup that the service
// must read: the config.
func dataFilesForService(cfgPath string) []string { return []string{cfgPath} }

// linkCommand points /usr/local/bin/palate-agent at the installed program,
// unless something else already has that name.
func linkCommand(exe string) {
	if cur, err := os.Readlink(commandLink); err == nil && cur != exe {
		os.Remove(commandLink)
	} else if err == nil {
		return
	} else if _, err := os.Lstat(commandLink); err == nil {
		return // a real file of someone else's
	}
	os.MkdirAll(filepath.Dir(commandLink), 0o755)
	os.Symlink(exe, commandLink)
}

// unlinkCommand removes the link, if it is the agent's.
func unlinkCommand(exe string) {
	if cur, err := os.Readlink(commandLink); err == nil && cur == exe {
		os.Remove(commandLink)
	}
}

// inUserFolder reports whether path is in someone's home folder, which the
// service may not read (Linux: its own user; macOS: privacy protection
// keeps even root out of Desktop, Documents and Downloads).
func inUserFolder(path string) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	for _, p := range []string{"/home/", "/Users/", "/root/"} {
		if strings.HasPrefix(abs, p) {
			return true
		}
	}
	return false
}
