//go:build !windows && !linux && !darwin

package service

// Other systems have no service support: run "palate-agent run" from the
// system's own startup mechanism.

import "errors"

const (
	VirtualAccount = "root"
	LocalSystem    = "root"
)

var errUnsupported = errors.New("installing the service is supported on Windows, Linux (systemd) and macOS; run \"palate-agent run\" from this system's startup mechanism instead")

func PrepareAccount(string) error            { return nil }
func Install(string, []string, string) error { return errUnsupported }
func Uninstall() error                       { return ErrNotInstalled }
func Start() error                           { return errUnsupported }
func Stop() error                            { return errUnsupported }
func Status() (Info, error)                  { return Info{}, ErrNotInstalled }
