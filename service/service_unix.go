//go:build !windows

// Package service runs the agent as a background service and installs,
// removes, starts, stops and describes that service: a systemd unit on
// Linux, a launchd daemon on macOS.
//
// Either way the service starts when the computer starts and is restarted
// after it fails. It runs the program with "run", and the PALATE_AGENT_SERVICE
// environment variable tells that process it is the service.
package service

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

const (
	Name        = "PalatePrintAgent"
	DisplayName = "Palate Print Agent"
	description = "Prints Palate kitchen tickets and bills on this computer's printers. It only connects out to Palate's server and opens no ports."

	// envService is set in the service's environment by the unit or plist.
	envService = "PALATE_AGENT_SERVICE"
)

// stopBudget is how long Run waits for the agent to finish the tickets in
// hand once asked to stop. systemd and launchd are told to wait longer.
const stopBudget = 18 * time.Second

var ErrNotInstalled = errors.New("the Palate Print Agent service is not installed")

// Info describes the installed service.
type Info struct {
	State       string
	PID         uint32
	Account     string
	Command     string
	AutoStart   bool
	LastExitErr string
}

// IsService reports whether this process was started as the service.
func IsService() (bool, error) { return os.Getenv(envService) == "1", nil }

// Run runs fn until the service manager asks it to stop (SIGTERM, then fn's
// context is cancelled) or fn returns. An error return makes the process
// exit with an error, which the service manager restarts.
func Run(fn func(ctx context.Context) error, log *slog.Logger) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sig)
	done := make(chan error, 1)
	go func() { done <- fn(ctx) }()
	log.Info("service running")
	select {
	case err := <-done:
		if err != nil {
			log.Error("agent stopped; the service manager will restart it", "err", err)
			return err
		}
		log.Info("agent stopped")
		return nil
	case s := <-sig:
		log.Info("stopping: finishing the tickets in hand", "reason", s.String())
		cancel()
		select {
		case <-done:
			log.Info("stopped cleanly")
		case <-time.After(stopBudget):
			log.Warn("agent did not stop in time; leaving anyway", "waited", stopBudget)
		}
		return nil
	}
}

// IsElevated reports whether this process runs as root, which installing,
// removing, starting and stopping the service need.
func IsElevated() bool { return os.Geteuid() == 0 }

// run runs a command and returns its output, with its error output folded
// into the error.
func run(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(string(out))
		}
		if msg != "" {
			return string(out), errors.New(name + " " + strings.Join(args, " ") + ": " + msg)
		}
		return string(out), errors.New(name + " " + strings.Join(args, " ") + ": " + err.Error())
	}
	return string(out), nil
}

var errNeedRoot = errors.New("this needs administrator rights: run it again with sudo")

// confirmRunning checks a service that was just started is still running
// a few seconds later, as the same process: systemd and launchd call a
// service started the moment its process is, and restart one that fails,
// so a single look could catch a failing agent between restarts.
func confirmRunning() error {
	failed := errors.New("the service stopped right after starting; see the log (palate-agent status)")
	time.Sleep(2 * time.Second)
	a, err := Status()
	if err != nil {
		return err
	}
	if a.State != "running" {
		return failed
	}
	time.Sleep(1500 * time.Millisecond)
	b, err := Status()
	if err != nil {
		return err
	}
	if b.State != "running" || b.PID != a.PID {
		return failed
	}
	return nil
}

// waitState polls Status until the service is in state want.
func waitState(want string, limit time.Duration) error {
	deadline := time.Now().Add(limit)
	for {
		info, err := Status()
		if err != nil {
			return err
		}
		if info.State == want {
			return nil
		}
		if want == "running" && info.State == "stopped" {
			return errors.New("the service stopped right after starting; see the log (palate-agent status)")
		}
		if time.Now().After(deadline) {
			return errors.New("the service is " + info.State + " after " + limit.String() + ", not " + want)
		}
		time.Sleep(300 * time.Millisecond)
	}
}
