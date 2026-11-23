//go:build linux

package service

// The Linux service is a systemd unit. By default it runs as its own system
// user, palate-agent, which owns the agent's data folder and nothing else;
// "-account localsystem" runs it as root instead.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	// VirtualAccount is the default: a system user made for this service
	// alone, with no password, no home and no login shell.
	VirtualAccount = "palate-agent"
	// LocalSystem is the fallback, for a printer only root can reach.
	LocalSystem = "root"

	unitName = "palate-print-agent.service"
)

var unitPath = filepath.Join("/etc/systemd/system", unitName)

func hasSystemd() bool {
	_, err := os.Stat("/run/systemd/system")
	return err == nil
}

// Install writes and enables the systemd unit that runs exe with args as
// account (VirtualAccount, made if needed, or LocalSystem), starting with
// the computer and restarting after failures. It does not start it.
func Install(exe string, args []string, account string) error {
	if !IsElevated() {
		return errNeedRoot
	}
	if !hasSystemd() {
		return errors.New("this Linux doesn't use systemd, so the service can't be installed; run \"palate-agent run\" from your own startup system instead")
	}
	if _, err := os.Stat(unitPath); err == nil {
		return fmt.Errorf("the %s service is already installed; uninstall it first", Name)
	}
	if err := PrepareAccount(account); err != nil {
		return err
	}
	if err := os.WriteFile(unitPath, []byte(unitFile(exe, args, account)), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", unitPath, err)
	}
	if _, err := run("systemctl", "daemon-reload"); err != nil {
		os.Remove(unitPath)
		return err
	}
	if _, err := run("systemctl", "enable", unitName); err != nil {
		os.Remove(unitPath)
		run("systemctl", "daemon-reload")
		return err
	}
	return nil
}

// unitFile is the systemd unit. The agent writes only its data folder (the
// config's folder, owned by the service user) and temporary files, and the
// browser that draws bills gets a private /tmp as its home.
func unitFile(exe string, args []string, account string) string {
	var b strings.Builder
	b.WriteString("# Written by palate-agent install. Remove with: sudo palate-agent uninstall\n")
	b.WriteString("[Unit]\n")
	b.WriteString("Description=" + DisplayName + ": " + description + "\n")
	b.WriteString("After=network-online.target cups.service\n")
	b.WriteString("Wants=network-online.target\n\n")
	b.WriteString("[Service]\n")
	b.WriteString("Type=simple\n")
	b.WriteString("ExecStart=" + quoteArgs(append([]string{exe}, args...)) + "\n")
	b.WriteString("Environment=" + envService + "=1\n")
	b.WriteString("Environment=HOME=/tmp\n")
	if account != LocalSystem {
		b.WriteString("User=" + account + "\n")
		b.WriteString("Group=" + account + "\n")
	}
	b.WriteString("Restart=on-failure\n")
	b.WriteString("RestartSec=5\n")
	b.WriteString("TimeoutStopSec=25\n")
	b.WriteString("NoNewPrivileges=yes\n")
	b.WriteString("PrivateTmp=yes\n")
	b.WriteString("ProtectSystem=full\n\n")
	b.WriteString("[Install]\n")
	b.WriteString("WantedBy=multi-user.target\n")
	return b.String()
}

// quoteArgs quotes each argument for a systemd ExecStart line.
func quoteArgs(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		a = strings.ReplaceAll(a, `\`, `\\`)
		a = strings.ReplaceAll(a, `"`, `\"`)
		a = strings.ReplaceAll(a, "%", "%%")
		q[i] = `"` + a + `"`
	}
	return strings.Join(q, " ")
}

// PrepareAccount makes the service's own user, before install hands it the
// data folder. LocalSystem (root) needs nothing.
func PrepareAccount(account string) error {
	if account != VirtualAccount {
		return nil
	}
	if !IsElevated() {
		return errNeedRoot
	}
	return ensureUser()
}

// ensureUser makes the service's system user, in the lp group (for printers
// plugged in by USB) where there is one.
func ensureUser() error {
	if _, err := run("id", "-u", VirtualAccount); err == nil {
		return nil
	}
	if _, err := exec.LookPath("useradd"); err != nil {
		return errors.New("can't make the palate-agent user here (no useradd); install with -account localsystem to run the service as root")
	}
	shell := "/usr/sbin/nologin"
	if _, err := os.Stat(shell); err != nil {
		shell = "/sbin/nologin"
	}
	args := []string{"--system", "--user-group", "--no-create-home", "--home-dir", "/nonexistent", "--shell", shell}
	if _, err := run("getent", "group", "lp"); err == nil {
		args = append(args, "--groups", "lp")
	}
	if _, err := run("useradd", append(args, VirtualAccount)...); err != nil {
		return fmt.Errorf("making the %s user: %w", VirtualAccount, err)
	}
	return nil
}

// Uninstall stops the service if it is running and removes it. The service
// user stays, as owner of the data folder that is kept.
func Uninstall() error {
	if !IsElevated() {
		return errNeedRoot
	}
	if _, err := os.Stat(unitPath); errors.Is(err, os.ErrNotExist) {
		return ErrNotInstalled
	}
	if _, err := run("systemctl", "disable", "--now", unitName); err != nil {
		return err
	}
	if err := os.Remove(unitPath); err != nil {
		return fmt.Errorf("removing the service: %w", err)
	}
	run("systemctl", "daemon-reload")
	run("systemctl", "reset-failed", unitName)
	return nil
}

// Start starts the service and waits until it is running.
func Start() error {
	if !IsElevated() {
		return errNeedRoot
	}
	if _, err := Status(); err != nil {
		return err
	}
	if _, err := run("systemctl", "start", unitName); err != nil {
		return fmt.Errorf("starting the service: %w", err)
	}
	return confirmRunning()
}

// Stop asks the service to stop and waits until it has (systemctl waits).
func Stop() error {
	if !IsElevated() {
		return errNeedRoot
	}
	if _, err := Status(); err != nil {
		return err
	}
	if _, err := run("systemctl", "stop", unitName); err != nil {
		return fmt.Errorf("stopping the service: %w", err)
	}
	return nil
}

// Status describes the service, or returns ErrNotInstalled. It works
// without root.
func Status() (Info, error) {
	if _, err := os.Stat(unitPath); errors.Is(err, os.ErrNotExist) {
		return Info{}, ErrNotInstalled
	}
	out, err := run("systemctl", "show", unitName,
		"-p", "LoadState,ActiveState,MainPID,User,UnitFileState,Result,ExecMainStatus")
	if err != nil {
		return Info{}, err
	}
	p := map[string]string{}
	for _, l := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(l), "="); ok {
			p[k] = v
		}
	}
	if p["LoadState"] == "not-found" {
		return Info{}, ErrNotInstalled
	}
	info := Info{State: stateName(p["ActiveState"]), Account: p["User"], AutoStart: p["UnitFileState"] == "enabled"}
	if info.Account == "" {
		info.Account = "root"
	}
	if pid, err := strconv.ParseUint(p["MainPID"], 10, 32); err == nil {
		info.PID = uint32(pid)
	}
	info.Command = execStart()
	if info.State == "stopped" && p["Result"] != "" && p["Result"] != "success" {
		info.LastExitErr = fmt.Sprintf("the agent last stopped with an error (%s, code %s); see the log", p["Result"], p["ExecMainStatus"])
	}
	return info, nil
}

func execStart() string {
	b, err := os.ReadFile(unitPath)
	if err != nil {
		return ""
	}
	for _, l := range strings.Split(string(b), "\n") {
		if s, ok := strings.CutPrefix(l, "ExecStart="); ok {
			return s
		}
	}
	return ""
}

func stateName(active string) string {
	switch active {
	case "active", "reloading":
		return "running"
	case "activating":
		return "starting"
	case "deactivating":
		return "stopping"
	case "inactive", "failed", "":
		return "stopped"
	}
	return active
}
