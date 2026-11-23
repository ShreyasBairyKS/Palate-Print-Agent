//go:build !windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

func startedByDoubleClick() bool { return false }

// relaunchElevated runs this program again through sudo, in this terminal
// (sudo asks for the password here), and waits for it.
func relaunchElevated(args []string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command("sudo", append([]string{exe}, args...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// runVersion runs another copy of the agent's "version" command and returns
// what it printed.
func runVersion(exe string) (string, error) {
	out, err := exec.Command(exe, "version").Output()
	return strings.TrimSpace(string(out)), err
}

// removeAtRestart removes a file or empty folder now: here a running
// program can delete itself.
func removeAtRestart(path string) { os.Remove(path) }

var reOSName = regexp.MustCompile(`(?m)^PRETTY_NAME="?([^"\n]+)"?`)

// windowsVersion is this system's name and version, for the support report
// ("Ubuntu 24.04.1 LTS (linux/amd64)", "macOS 14.5 (darwin/arm64)").
func windowsVersion() string {
	arch := " (" + runtime.GOOS + "/" + runtime.GOARCH + ")"
	if runtime.GOOS == "darwin" {
		if out, err := exec.Command("sw_vers", "-productVersion").Output(); err == nil {
			return "macOS " + strings.TrimSpace(string(out)) + arch
		}
		return "macOS" + arch
	}
	if b, err := os.ReadFile("/etc/os-release"); err == nil {
		if m := reOSName.FindSubmatch(b); m != nil {
			return string(m[1]) + arch
		}
	}
	return osName + arch
}

// desktopDir is the person's Desktop folder (under sudo, the person who ran
// sudo, not root's).
func desktopDir() string {
	home, _ := os.UserHomeDir()
	if u := os.Getenv("SUDO_USER"); u != "" && os.Geteuid() == 0 {
		if runtime.GOOS == "darwin" {
			home = filepath.Join("/Users", u)
		} else {
			home = filepath.Join("/home", u)
		}
	}
	return filepath.Join(home, "Desktop")
}
