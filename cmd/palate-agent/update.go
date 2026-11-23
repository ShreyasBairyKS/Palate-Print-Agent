package main

// Updating and removing the installed agent without a terminal, and telling
// builds apart by when they were built.
//
// Double-clicking a newer palate-agent.exe on a till where the agent is
// installed offers to update it: the service stops, the new program replaces
// the one in Program Files, and the service starts again. The config, the
// pairing and the ledger stay where they are.

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/agent"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/config"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/pair"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/service"
)

// buildDate is when this program was built, in RFC 3339 (UTC).
// scripts/package.sh sets it with -ldflags "-X main.buildDate=...".
var buildDate string

// versionLine is what "palate-agent version" prints: the version, and when
// it was built if known ("0.4.0-m4 built 2026-11-22T08:35:00Z").
func versionLine() string {
	if buildDate == "" {
		return agent.Version
	}
	return agent.Version + " built " + buildDate
}

// parseVersionLine reads what versionLine printed (an older program prints
// only its version).
func parseVersionLine(s string) (ver, built string) {
	f := strings.Fields(s)
	if len(f) > 0 {
		ver = f[0]
	}
	if len(f) >= 3 && f[1] == "built" {
		built = f[2]
	}
	return ver, built
}

// versionLabel is this program's version for people.
func versionLabel() string { return labelFor(agent.Version, buildDate) }

// labelFor is "version 0.4.0-m4, built 22 Nov 2026, 2:05 pm", or without the
// date when it isn't known.
func labelFor(ver, built string) string {
	if ver == "" {
		ver = "unknown"
	}
	t, err := time.Parse(time.RFC3339, built)
	if err != nil {
		return "version " + ver
	}
	return "version " + ver + ", built " + strings.ReplaceAll(strings.ReplaceAll(t.Local().Format("2 Jan 2006, 3:04 PM"), "AM", "am"), "PM", "pm")
}

// buildCompare is this program against the one installed in Program Files.
type buildCompare struct {
	Different bool   // the installed program isn't this one
	Older     bool   // this file was built before the installed one
	Installed string // the installed program's label
	This      string // this program's label
}

// compareInstalled compares the running program (self) with the installed
// one by their contents. version runs the installed program's "version"
// command, to say which build it is.
func compareInstalled(self, installed string, version func(exe string) (string, error)) (buildCompare, error) {
	c := buildCompare{This: versionLabel()}
	if samePath(self, installed) {
		return c, nil
	}
	a, err := fileHash(self)
	if err != nil {
		return c, err
	}
	b, err := fileHash(installed)
	if errors.Is(err, os.ErrNotExist) {
		c.Different, c.Installed = true, "missing from "+filepath.Dir(installed)
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if bytes.Equal(a, b) {
		return c, nil
	}
	c.Different = true
	c.Installed = "an earlier build"
	if out, err := version(installed); err == nil {
		ver, built := parseVersionLine(out)
		c.Installed = labelFor(ver, built)
		ti, err1 := time.Parse(time.RFC3339, built)
		tt, err2 := time.Parse(time.RFC3339, buildDate)
		c.Older = err1 == nil && err2 == nil && tt.Before(ti)
	}
	return c, nil
}

func fileHash(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}

// updateOffer is the question asked before updating.
func updateOffer(c buildCompare) string {
	s := "A different version of the Palate print agent is installed on this PC.\n\n" +
		"Installed:   " + c.Installed + "\n" +
		"This file:   " + c.This + "\n\n"
	if c.Older {
		s += "Careful: this file is OLDER than the installed one.\n\n"
	}
	return s + "Install this file's version? Printers, settings and pairing are kept. Printing pauses for a few seconds."
}

// checkForUpdate compares this program with the installed one, when the
// agent is installed. It reports nothing to do on any error.
func checkForUpdate() (buildCompare, bool) {
	if _, err := service.Status(); err != nil {
		return buildCompare{}, false
	}
	self, err := os.Executable()
	if err != nil {
		return buildCompare{}, false
	}
	c, err := compareInstalled(self, installedExe(), runVersion)
	return c, err == nil && c.Different
}

// updateProgram replaces the installed program with this one: it stops the
// service, copies this program over the installed one, and starts the
// service again if it was running or the till is paired. The caller has
// administrator rights.
func updateProgram(cfgPath string, say func(string)) error {
	info, err := service.Status()
	if err != nil {
		return err
	}
	if info.State != "stopped" {
		say("Stopping the print agent...")
		if err := service.Stop(); err != nil {
			return err
		}
	}
	say("Installing " + versionLabel() + "...")
	exe := installedExe()
	// The stopped program can hold its file for a moment after it exits.
	for i := 0; ; i++ {
		err = copySelf(exe)
		if err == nil || i == 20 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err != nil {
		if info.State == "running" {
			service.Start() // carry on with the old program
		}
		return fmt.Errorf("copying the program to %s: %w", exe, err)
	}
	recordChange(cfgPath, "updated the program to "+versionLabel())
	paired := false
	if cfg, err := config.Load(cfgPath); err == nil {
		_, perr := (pair.FileStore{Path: cfg.CredentialFile}).Load()
		paired = perr == nil
	}
	if info.State == "running" || paired {
		say("Starting the print agent...")
		if err := service.Start(); err != nil {
			return fmt.Errorf("updated, but starting failed: %w", err)
		}
	}
	say("Updated to " + versionLabel() + ".")
	return nil
}

// removeAgent removes the service and the program from this PC. The data
// folder (config, pairing, ledger, logs) is kept unless deleteData, so a
// reinstall picks up where it left off. The caller has administrator
// rights. It returns what it kept, for the message.
func removeAgent(cfgPath string, deleteData bool) (kept string, err error) {
	if err := service.Uninstall(); err != nil && !errors.Is(err, service.ErrNotInstalled) {
		return "", err
	}
	exe := installedExe()
	if self, err := os.Executable(); err == nil && samePath(self, exe) {
		// Running from Program Files: Windows deletes it at the next restart.
		removeAtRestart(exe)
		removeAtRestart(filepath.Dir(exe))
	} else {
		os.Remove(exe)
		os.Remove(filepath.Dir(exe))
	}
	dataDir := filepath.Dir(cfgPath)
	if !deleteData {
		return dataDir, nil
	}
	// Only ever delete the agent's own folder, never one a custom -config
	// pointed somewhere else.
	if !strings.EqualFold(filepath.Base(dataDir), "PalatePrintAgent") {
		return dataDir, fmt.Errorf("removed the program, but kept %s: it isn't the agent's own folder", dataDir)
	}
	if err := os.RemoveAll(dataDir); err != nil {
		return dataDir, fmt.Errorf("removed the program, but couldn't delete all of %s: %w", dataDir, err)
	}
	return "", nil
}

func cmdUpdate(args []string) error {
	fs := flag.NewFlagSet("update", flag.ExitOnError)
	path := fs.String("config", defaultConfigPath(), "path to agent.json")
	window := fs.Bool("gui", false, "report in message boxes, then open the window")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !service.IsElevated() {
		return errNeedAdmin
	}
	var lines []string
	err := updateProgram(*path, func(s string) {
		lines = append(lines, s)
		if !*window {
			fmt.Println(s)
		}
	})
	if !*window {
		return err
	}
	if err != nil {
		notify("The update didn't finish.\n\n"+explain(err).Box(), true)
	} else {
		notify("Updated to "+versionLabel()+".\n\nTickets and bills print as before.", false)
	}
	openWindow(*path, slog.Default())
	return nil
}
