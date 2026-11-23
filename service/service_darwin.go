//go:build darwin

package service

// The macOS service is a launchd daemon in /Library/LaunchDaemons. It runs
// as root: macOS has no simple way to make a service user from a program.

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	// VirtualAccount and LocalSystem are both root on macOS.
	VirtualAccount = "root"
	LocalSystem    = "root"

	label     = "com.palate.print-agent"
	plistPath = "/Library/LaunchDaemons/" + label + ".plist"
	target    = "system/" + label
)

// PrepareAccount does nothing: the daemon runs as root.
func PrepareAccount(string) error { return nil }

// Install writes the launchd daemon that runs exe with args, starting with
// the Mac and restarting after failures. It does not start it.
func Install(exe string, args []string, _ string) error {
	if !IsElevated() {
		return errNeedRoot
	}
	if _, err := os.Stat(plistPath); err == nil {
		return fmt.Errorf("the %s service is already installed; uninstall it first", Name)
	}
	if err := os.WriteFile(plistPath, plist(exe, args), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", plistPath, err)
	}
	return nil
}

func plist(exe string, args []string) []byte {
	esc := func(s string) string {
		var b bytes.Buffer
		xml.EscapeText(&b, []byte(s))
		return b.String()
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<!-- Written by palate-agent install. Remove with: sudo palate-agent uninstall -->
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + label + `</string>
	<key>ProgramArguments</key>
	<array>
`)
	for _, a := range append([]string{exe}, args...) {
		b.WriteString("\t\t<string>" + esc(a) + "</string>\n")
	}
	b.WriteString(`	</array>
	<key>EnvironmentVariables</key>
	<dict>
		<key>` + envService + `</key>
		<string>1</string>
	</dict>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>ThrottleInterval</key>
	<integer>10</integer>
	<key>ExitTimeOut</key>
	<integer>25</integer>
	<key>StandardErrorPath</key>
	<string>/Library/Logs/palate-print-agent.log</string>
</dict>
</plist>
`)
	return []byte(b.String())
}

// loaded reports whether launchd has the daemon loaded, and what it says.
func loaded() (string, bool) {
	out, err := run("launchctl", "print", target)
	return out, err == nil
}

// Uninstall stops the service if it is running and removes it.
func Uninstall() error {
	if !IsElevated() {
		return errNeedRoot
	}
	if _, err := os.Stat(plistPath); errors.Is(err, os.ErrNotExist) {
		return ErrNotInstalled
	}
	if _, ok := loaded(); ok {
		if err := Stop(); err != nil {
			return err
		}
	}
	if err := os.Remove(plistPath); err != nil {
		return fmt.Errorf("removing the service: %w", err)
	}
	return nil
}

// Start loads the daemon (which starts it) or restarts a loaded one, and
// waits until it is running.
func Start() error {
	if !IsElevated() {
		return errNeedRoot
	}
	if _, err := Status(); err != nil {
		return err
	}
	var err error
	if _, ok := loaded(); ok {
		_, err = run("launchctl", "kickstart", target)
	} else {
		_, err = run("launchctl", "bootstrap", "system", plistPath)
	}
	if err != nil {
		return fmt.Errorf("starting the service: %w", err)
	}
	return confirmRunning()
}

// Stop unloads the daemon (launchd sends it SIGTERM) and waits until it has
// gone. It loads again when the Mac starts, or with Start.
func Stop() error {
	if !IsElevated() {
		return errNeedRoot
	}
	if _, err := Status(); err != nil {
		return err
	}
	if _, ok := loaded(); !ok {
		return nil
	}
	if _, err := run("launchctl", "bootout", target); err != nil {
		return fmt.Errorf("stopping the service: %w", err)
	}
	return waitState("stopped", 40*time.Second)
}

var (
	reState = regexp.MustCompile(`(?m)^\s*state = (.+)$`)
	rePID   = regexp.MustCompile(`(?m)^\s*pid = (\d+)$`)
	reExit  = regexp.MustCompile(`(?m)^\s*last exit code = (-?\d+)`)
	reArg   = regexp.MustCompile(`<string>(.*)</string>`)
)

// Status describes the service, or returns ErrNotInstalled. It works
// without root.
func Status() (Info, error) {
	b, err := os.ReadFile(plistPath)
	if errors.Is(err, os.ErrNotExist) {
		return Info{}, ErrNotInstalled
	}
	if err != nil {
		return Info{}, err
	}
	info := Info{State: "stopped", Account: "root", AutoStart: true, Command: command(string(b))}
	out, ok := loaded()
	if !ok {
		return info, nil
	}
	if m := reState.FindStringSubmatch(out); m != nil && strings.TrimSpace(m[1]) == "running" {
		info.State = "running"
	}
	if m := rePID.FindStringSubmatch(out); m != nil {
		if pid, err := strconv.ParseUint(m[1], 10, 32); err == nil {
			info.PID = uint32(pid)
		}
	}
	if m := reExit.FindStringSubmatch(out); m != nil && m[1] != "0" && info.State != "running" {
		info.LastExitErr = "the agent last stopped with an error (code " + m[1] + "); see the log"
	}
	return info, nil
}

// command is the daemon's program and arguments, from its plist.
func command(p string) string {
	start := strings.Index(p, "<key>ProgramArguments</key>")
	if start < 0 {
		return ""
	}
	p = p[start:]
	if end := strings.Index(p, "</array>"); end >= 0 {
		p = p[:end]
	}
	var parts []string
	for _, m := range reArg.FindAllStringSubmatch(p, -1) {
		a := html.UnescapeString(m[1])
		if strings.ContainsAny(a, " \t") {
			a = `"` + a + `"`
		}
		parts = append(parts, a)
	}
	return strings.Join(parts, " ")
}
