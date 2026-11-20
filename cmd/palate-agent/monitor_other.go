//go:build !windows

package main

import "errors"

func runMonitor(string, bool) error { return errors.New("the status window needs Windows") }
