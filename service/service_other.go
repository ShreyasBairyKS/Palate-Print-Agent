//go:build !windows

// Package service runs the agent as a Windows service. Other systems have
// no service support yet.
package service

import (
	"context"
	"errors"
	"log/slog"
)

const (
	Name           = "PalatePrintAgent"
	DisplayName    = "Palate Print Agent"
	VirtualAccount = `NT SERVICE\` + Name
	LocalSystem    = "LocalSystem"
)

var (
	ErrNotInstalled = errors.New("the Palate Print Agent service is not installed")
	errUnsupported  = errors.New("running as a service is only supported on Windows")
)

type Info struct {
	State       string
	PID         uint32
	Account     string
	Command     string
	AutoStart   bool
	LastExitErr string
}

func IsService() (bool, error)                                { return false, nil }
func Run(func(ctx context.Context) error, *slog.Logger) error { return errUnsupported }
func Install(string, []string, string) error                  { return errUnsupported }
func Uninstall() error                                        { return errUnsupported }
func Start() error                                            { return errUnsupported }
func Stop() error                                             { return errUnsupported }
func Status() (Info, error)                                   { return Info{}, errUnsupported }

func IsElevated() bool { return false }
