//go:build windows

package main

// Where the agent lives on Windows, and how its messages name Windows.

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	osName            = "Windows"
	adminHint         = `open a terminal with "Run as administrator" and run it again`
	asAdmin           = "(as administrator)"
	serviceWords      = "the Windows service"
	startsWith        = "starts with Windows"
	browserName       = "Microsoft Edge"
	browserShort      = "Edge"
	printerSystem     = "Windows printers"
	printerNameHint   = "Windows printer name"
	elevateSay        = "Windows will ask you to allow it; setup continues in a new window."
	elevateFailHow    = `Right-click palate-agent.exe and choose "Run as administrator"`
	pauseAfterElevate = true
	sayAdmin          = "Close this window, double-click palate-agent.exe again, and choose Yes when Windows asks."
	setUpHow          = "double-click palate-agent.exe and choose Yes when Windows asks"
)

// defaultConfigPath is %ProgramData%\PalatePrintAgent\agent.json.
func defaultConfigPath() string {
	base := os.Getenv("ProgramData")
	if base == "" {
		base = "."
	}
	return filepath.Join(base, "PalatePrintAgent", "agent.json")
}

// installedExe is where install puts the program: a service account can't
// read files in a user's folders (Desktop, Downloads, OneDrive).
func installedExe() string {
	base := os.Getenv("ProgramFiles")
	if base == "" {
		base = `C:\Program Files`
	}
	return filepath.Join(base, "PalatePrintAgent", "palate-agent.exe")
}

// dataFilesForService are other files install must open to the service; on
// Windows they inherit the data folder's access list.
func dataFilesForService(string) []string { return nil }

// linkCommand and unlinkCommand do nothing on Windows.
func linkCommand(string)   {}
func unlinkCommand(string) {}

// inUserFolder reports whether path is under the folder holding users'
// profiles (C:\Users).
func inUserFolder(path string) bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	users := strings.ToLower(filepath.Dir(home)) + string(filepath.Separator)
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	return strings.HasPrefix(strings.ToLower(abs), users)
}
