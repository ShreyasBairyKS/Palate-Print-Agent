//go:build !windows

package main

import (
	"errors"
	"fmt"
)

func runGUI(*setupEnv, bool, appOptions) error { return errors.New("the window needs Windows") }

func guiAvailable() bool { return false }

// notify tells the person something; without windows, on the console.
func notify(text string, bad bool) { fmt.Println(text) }

func copyToClipboard(string) error { return errors.New("the clipboard needs Windows") }

func askYesNo(string, bool) bool { return false }
