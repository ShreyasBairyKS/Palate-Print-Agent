package main

// The steps of setting up a till, shared by the setup window and the console
// setup, so both apply the same rules and run the same steps.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/config"
)

// suggestID turns a printer's name into an id: "Front Bill" -> "p-front-bill".
// n numbers it when the name gives nothing usable.
func suggestID(name string, n int) string {
	id := "p-" + strings.Trim(nonID.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if !validID.MatchString(id) || id == "p-" {
		id = fmt.Sprintf("p-%d", n)
	}
	return id
}

// realPrinters drops Windows' built-in pretend printers (PDF, XPS, OneNote,
// fax), which can never be a kitchen printer.
func realPrinters(names []string) []string {
	var out []string
	for _, n := range names {
		l := strings.ToLower(n)
		if strings.Contains(l, "pdf") || strings.Contains(l, "xps") || strings.Contains(l, "onenote") || strings.Contains(l, "fax") {
			continue
		}
		out = append(out, n)
	}
	return out
}

// printerInput is what a person types for one printer.
type printerInput struct {
	Name, ID string
	USB      bool   // false: network
	Where    string // IP address (network) or Windows printer name (USB)
	Width    int    // 58 or 80
	Backup   string // another printer's id, or ""
}

// makePrinter checks one printer against everything else in raw and returns
// it with its address normalised. replacing is the id of the printer being
// edited ("" when adding), so it doesn't clash with itself.
func makePrinter(raw *config.Config, in printerInput, replacing string) (config.Printer, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.ID = strings.TrimSpace(in.ID)
	in.Where = strings.TrimSpace(in.Where)
	switch {
	case in.Name == "":
		return config.Printer{}, errors.New("give the printer a name, as the kitchen calls it")
	case !validID.MatchString(in.ID):
		return config.Printer{}, errors.New("the id starts with a letter, then letters, digits, - or _")
	case in.ID != replacing && raw.Find(in.ID) != nil:
		return config.Printer{}, fmt.Errorf("there's already a printer with the id %s", in.ID)
	case in.Where == "" && in.USB:
		return config.Printer{}, errors.New("choose the USB printer")
	case in.Where == "":
		return config.Printer{}, errors.New("type the printer's IP address")
	case in.Backup == in.ID && in.Backup != "":
		return config.Printer{}, errors.New("a printer can't be its own backup")
	}
	p := config.Printer{ID: in.ID, Name: in.Name, Width: in.Width, Backup: in.Backup}
	if in.USB {
		p.Kind, p.PrinterName = "usb", in.Where
	} else {
		p.Kind, p.Address = "network", in.Where
	}
	// Check it in place, as the config would hold it.
	trial := *raw
	trial.Printers = nil
	for _, q := range raw.Printers {
		if q.ID != replacing {
			trial.Printers = append(trial.Printers, q)
		}
	}
	trial.Printers = append(trial.Printers, p)
	if trial.Server == "" {
		trial.Server = "https://example.invalid" // the server is checked on its own
	}
	checked, err := trial.Check()
	if err != nil {
		return config.Printer{}, err
	}
	return *checked.Find(p.ID), nil
}

// saveNewConfig writes the config setup built, creating its folder.
func saveNewConfig(cfgPath string, raw *config.Config) error {
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		return writeHint(err)
	}
	if err := config.Save(cfgPath, raw); err != nil {
		return err
	}
	recordChange(cfgPath, "setup saved the config with "+strconv.Itoa(len(raw.Printers))+" printer(s)")
	return nil
}

// installSteps saves the config, installs the service, pairs (when a code
// is given) and starts the agent, reporting each step through say. It
// returns paired = false when there was no code, so the agent is installed
// but not yet started.
func installSteps(e *setupEnv, raw *config.Config, code string, say func(string)) (paired bool, err error) {
	if _, err := raw.Check(); err != nil {
		return false, err
	}
	say("Saving the settings...")
	if err := saveNewConfig(e.cfgPath, raw); err != nil {
		return false, err
	}
	cfg, err := config.Load(e.cfgPath)
	if err != nil {
		return false, err
	}
	say("Installing the Windows service...")
	if err := e.install(e.cfgPath); err != nil {
		return false, err
	}
	code = strings.ToUpper(strings.TrimSpace(code))
	if code == "" {
		say("Installed. Not paired yet: pair with a code from the Palate portal, then start it.")
		return false, nil
	}
	say("Pairing with Palate...")
	dev, err := e.pair(cfg, code)
	if err != nil {
		return false, fmt.Errorf("pairing: %w", err)
	}
	say("Paired as " + dev + ". Starting the print agent...")
	if err := e.start(); err != nil {
		return true, fmt.Errorf("installed and paired, but starting failed: %w", err)
	}
	say("Running. Tickets and bills print as soon as Palate sends them.")
	return true, nil
}
