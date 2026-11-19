//go:build !windows

package main

import "errors"

func runGUI(*setupEnv, bool) error { return errors.New("the setup window needs Windows") }

func guiAvailable() bool { return false }
