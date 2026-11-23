//go:build !windows

package main

import (
	"errors"
	"os"
	"runtime"
)

func startedByDoubleClick() bool { return false }

func relaunchElevated([]string) error { return errors.New("needs Windows") }

func runVersion(string) (string, error) { return "", errors.New("needs Windows") }

func removeAtRestart(string) {}

func windowsVersion() string { return runtime.GOOS }

func desktopDir() string { h, _ := os.UserHomeDir(); return h }
