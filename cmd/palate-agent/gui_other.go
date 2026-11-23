//go:build !windows

package main

import (
	"errors"
	"fmt"
)

func runGUI(*setupEnv, bool, appOptions) error {
	return errors.New("there is no window on " + osName + " yet; use the terminal commands")
}

func guiAvailable() bool { return false }

// notify tells the person something; without windows, on the console.
func notify(text string, bad bool) { fmt.Println(text) }

func copyToClipboard(string) error { return errors.New("no clipboard here") }

func askYesNo(string, bool) bool { return false }
