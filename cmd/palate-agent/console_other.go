//go:build !windows

package main

import "errors"

func startedByDoubleClick() bool { return false }

func relaunchElevated([]string) error { return errors.New("needs Windows") }
