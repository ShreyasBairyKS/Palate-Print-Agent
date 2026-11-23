// Package service runs the agent as a Windows service and installs,
// removes, starts, stops and describes that service.
//
// The service starts with Windows, runs under its own low-privilege virtual
// account (NT SERVICE\PalatePrintAgent) unless told otherwise, and Windows
// restarts it after a crash: 5 s, then 30 s, then 60 s, with the count
// reset after a day.
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/internal/fileacl"
)

const (
	Name        = fileacl.ServiceName
	DisplayName = "Palate Print Agent"
	description = "Prints Palate kitchen tickets on this till's printers. It only connects out to Palate's server and opens no ports."

	// VirtualAccount is the default: an account Windows makes for this
	// service alone, with no password and few rights.
	VirtualAccount = `NT SERVICE\` + Name
	// LocalSystem is the fallback, for a USB printer the virtual account
	// can't see.
	LocalSystem = "LocalSystem"
)

// stopWait is how long Windows is told a stop may take, and stopBudget how
// long Execute waits for the agent before giving up on it.
const (
	stopWait   = 20 * time.Second
	stopBudget = 18 * time.Second
)

var ErrNotInstalled = errors.New("the Palate Print Agent service is not installed")

// IsService reports whether this process was started by Windows as a service.
func IsService() (bool, error) { return svc.IsWindowsService() }

// Run runs fn as the service until Windows asks it to stop (then fn's
// context is cancelled) or fn returns.
func Run(fn func(ctx context.Context) error, log *slog.Logger) error {
	return svc.Run(Name, &handler{fn: fn, log: log})
}

type handler struct {
	fn  func(ctx context.Context) error
	log *slog.Logger
}

func (h *handler) Execute(_ []string, req <-chan svc.ChangeRequest, st chan<- svc.Status) (bool, uint32) {
	return execute(h.fn, req, st, h.log, stopBudget)
}

// execute is the service's life: start fn, report Running, and stop it when
// Windows asks. A nonzero exit code tells Windows the service failed, which
// triggers its restart-on-failure settings.
func execute(fn func(ctx context.Context) error, req <-chan svc.ChangeRequest, st chan<- svc.Status, log *slog.Logger, budget time.Duration) (bool, uint32) {
	st <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- fn(ctx) }()
	running := svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	st <- running
	log.Info("service running")

	for {
		select {
		case err := <-done:
			// The agent stopped on its own: re-pair needed, or a bug.
			st <- svc.Status{State: svc.StopPending}
			if err != nil {
				log.Error("agent stopped; Windows will restart the service", "err", err)
				return true, 1
			}
			log.Info("agent stopped")
			return false, 0
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				st <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				why := "stop"
				if c.Cmd == svc.Shutdown {
					why = "Windows is shutting down"
				}
				log.Info("stopping: finishing the tickets in hand", "reason", why)
				st <- svc.Status{State: svc.StopPending, WaitHint: uint32(stopWait / time.Millisecond)}
				cancel()
				select {
				case <-done:
					log.Info("stopped cleanly")
				case <-time.After(budget):
					log.Warn("agent did not stop in time; leaving anyway", "waited", budget)
				}
				return false, 0
			}
		}
	}
}

// PrepareAccount does nothing on Windows: the virtual account exists once
// the service does, and the data folder's access list names the service.
func PrepareAccount(string) error { return nil }

// Install registers the service to run exe with the given arguments, as
// account (VirtualAccount or LocalSystem), starting with Windows and
// restarting after failures. It does not start it.
func Install(exe string, args []string, account string) error {
	m, err := mgr.Connect()
	if err != nil {
		return connectErr(err)
	}
	defer m.Disconnect()
	if s, err := m.OpenService(Name); err == nil {
		s.Close()
		return fmt.Errorf("the %s service is already installed; uninstall it first", Name)
	}
	cfg := mgr.Config{
		DisplayName:      DisplayName,
		Description:      description,
		StartType:        mgr.StartAutomatic,
		ErrorControl:     mgr.ErrorNormal,
		ServiceStartName: account,
	}
	if account == VirtualAccount {
		cfg.SidType = windows.SERVICE_SID_TYPE_UNRESTRICTED
	}
	s, err := m.CreateService(Name, exe, cfg, args...)
	if err != nil {
		return fmt.Errorf("creating the service: %w", err)
	}
	defer s.Close()
	actions := []mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 60 * time.Second},
	}
	if err := s.SetRecoveryActions(actions, uint32((24 * time.Hour).Seconds())); err != nil {
		s.Delete()
		return fmt.Errorf("setting restart-on-failure: %w", err)
	}
	// Also restart when the agent exits with an error, not only on a crash.
	if err := s.SetRecoveryActionsOnNonCrashFailures(true); err != nil {
		s.Delete()
		return fmt.Errorf("setting restart-on-failure: %w", err)
	}
	return nil
}

// Uninstall stops the service if it is running and removes it.
func Uninstall() error {
	m, s, err := open()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	defer s.Close()
	if st, err := s.Query(); err == nil && st.State != svc.Stopped {
		if err := stop(s); err != nil {
			return err
		}
	}
	if err := s.Delete(); err != nil {
		return fmt.Errorf("removing the service: %w", err)
	}
	return nil
}

// Start starts the service and waits until it reports Running.
func Start() error {
	m, s, err := open()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	defer s.Close()
	if err := s.Start(); err != nil {
		return fmt.Errorf("starting the service: %w", err)
	}
	return waitFor(s, svc.Running, 30*time.Second)
}

// Stop asks the service to stop and waits until it has.
func Stop() error {
	m, s, err := open()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	defer s.Close()
	return stop(s)
}

func stop(s *mgr.Service) error {
	if _, err := s.Control(svc.Stop); err != nil {
		return fmt.Errorf("stopping the service: %w", err)
	}
	return waitFor(s, svc.Stopped, stopWait+10*time.Second)
}

// Info describes the installed service.
type Info struct {
	State       string
	PID         uint32
	Account     string
	Command     string
	AutoStart   bool
	LastExitErr string
}

// Status describes the service, or returns ErrNotInstalled. It asks Windows
// only to read, so it works without administrator rights.
func Status() (Info, error) {
	h, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return Info{}, connectErr(err)
	}
	m := &mgr.Mgr{Handle: h}
	defer m.Disconnect()
	name, _ := windows.UTF16PtrFromString(Name)
	sh, err := windows.OpenService(h, name, windows.SERVICE_QUERY_STATUS|windows.SERVICE_QUERY_CONFIG)
	if err != nil {
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			return Info{}, ErrNotInstalled
		}
		return Info{}, err
	}
	s := &mgr.Service{Name: Name, Handle: sh}
	defer s.Close()
	st, err := s.Query()
	if err != nil {
		return Info{}, err
	}
	cfg, err := s.Config()
	if err != nil {
		return Info{}, err
	}
	info := Info{State: stateName(st.State), PID: st.ProcessId, Account: cfg.ServiceStartName,
		Command: cfg.BinaryPathName, AutoStart: cfg.StartType == mgr.StartAutomatic}
	if st.State == svc.Stopped && st.ServiceSpecificExitCode != 0 {
		info.LastExitErr = fmt.Sprintf("the agent last stopped with an error (code %d); see the log", st.ServiceSpecificExitCode)
	}
	return info, nil
}

func open() (*mgr.Mgr, *mgr.Service, error) {
	m, err := mgr.Connect()
	if err != nil {
		return nil, nil, connectErr(err)
	}
	s, err := m.OpenService(Name)
	if err != nil {
		m.Disconnect()
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			return nil, nil, ErrNotInstalled
		}
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return nil, nil, errors.New("access denied: run this from a terminal opened with \"Run as administrator\"")
		}
		return nil, nil, err
	}
	return m, s, nil
}

func connectErr(err error) error {
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return errors.New("access denied: run this from a terminal opened with \"Run as administrator\"")
	}
	return fmt.Errorf("connecting to the Windows service manager: %w", err)
}

func waitFor(s *mgr.Service, want svc.State, limit time.Duration) error {
	deadline := time.Now().Add(limit)
	for {
		st, err := s.Query()
		if err != nil {
			return err
		}
		if st.State == want {
			return nil
		}
		if want == svc.Running && st.State == svc.Stopped {
			return errors.New("the service stopped right after starting; see the log")
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the service is %s after %s, not %s", stateName(st.State), limit, stateName(want))
		}
		time.Sleep(300 * time.Millisecond)
	}
}

func stateName(s svc.State) string {
	switch s {
	case svc.Stopped:
		return "stopped"
	case svc.StartPending:
		return "starting"
	case svc.StopPending:
		return "stopping"
	case svc.Running:
		return "running"
	case svc.ContinuePending:
		return "resuming"
	case svc.PausePending:
		return "pausing"
	case svc.Paused:
		return "paused"
	}
	return fmt.Sprintf("state %d", s)
}

// IsElevated reports whether this process is running as administrator, which
// installing, removing, starting and stopping the service need.
func IsElevated() bool { return windows.GetCurrentProcessToken().IsElevated() }
