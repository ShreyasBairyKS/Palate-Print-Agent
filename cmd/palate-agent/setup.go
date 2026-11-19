package main

// Guided setup: what happens when someone double-clicks palate-agent.exe
// (or runs "palate-agent setup"). It asks for administrator rights, then
// walks through the server address, the printers (each test-printed), the
// service, the pairing code and starting, in that order, with the same rules
// as the individual commands. Once the agent is installed, it shows its
// status instead.

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/agent"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/config"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/htmlrender"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/pair"
	usbprinter "github.com/ShreyasBairyKS/Palate-Print-Agent/printer/usb"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/service"
)

// setupEnv is everything setup touches outside itself, so a test can run the
// whole conversation against fakes.
type setupEnv struct {
	in        *bufio.Reader
	out       io.Writer
	cfgPath   string
	status    func() (service.Info, error)
	install   func(cfgPath string) error
	pair      func(cfg *config.Config, code string) (string, error)
	start     func() error
	testPrint func(p config.Printer, t config.Timings) error
	usbList   func() ([]string, error)
	edgeFind  func() (string, error)
}

func cmdSetup(args []string, log *slog.Logger, doubleClicked bool) error {
	fs := flag.NewFlagSet("setup", flag.ExitOnError)
	path := fs.String("config", defaultConfigPath(), "path to agent.json")
	pause := fs.Bool("pause", false, "console setup: wait for Enter before closing")
	window := fs.Bool("gui", doubleClicked && guiAvailable(), "show the setup window instead of asking in the console")
	noElevate := fs.Bool("no-elevate", false, "don't ask for administrator rights (to look at the window; installing then fails)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if doubleClicked && !*window {
		*pause = true
	}
	in := bufio.NewReader(os.Stdin)
	finish := func(err error) error {
		if *pause {
			if err != nil {
				fmt.Println("\nSetup stopped: " + err.Error())
			}
			fmt.Print("\nPress Enter to close this window.")
			in.ReadString('\n')
			if err != nil {
				os.Exit(1)
			}
		}
		return err
	}

	if !service.IsElevated() && !*noElevate {
		fmt.Println("Palate print agent setup needs administrator rights.")
		fmt.Println("Windows will ask you to allow it; setup continues in a new window.")
		again := []string{"setup", "-config", *path}
		if *window {
			again = append(again, "-gui")
		} else {
			again = append(again, "-pause")
		}
		if err := relaunchElevated(again); err != nil {
			return finish(fmt.Errorf("couldn't get administrator rights (%v). Right-click palate-agent.exe and choose \"Run as administrator\"", err))
		}
		return nil
	}

	env := &setupEnv{
		in: in, out: os.Stdout, cfgPath: *path,
		status:    service.Status,
		install:   func(p string) error { return installService(p, "service", false) },
		pair:      func(cfg *config.Config, code string) (string, error) { return pairTill(cfg, code, log) },
		start:     service.Start,
		testPrint: testPrint,
		usbList:   usbprinter.List,
		edgeFind:  (&htmlrender.Edge{}).Find,
	}
	if *window {
		if err := runGUI(env, startedByDoubleClick()); err != nil {
			// No window possible: fall back to asking in the console.
			fmt.Println("The setup window couldn't open (" + err.Error() + "); continuing here.")
			return finish(runSetup(env))
		}
		return nil
	}
	return finish(runSetup(env))
}

func runSetup(e *setupEnv) (err error) {
	defer func() {
		if r := recover(); r != nil {
			if r != errInputEnded {
				panic(r)
			}
			err = errors.New("setup ended before it finished; run it again to carry on")
		}
	}()
	return e.steps()
}

func (e *setupEnv) steps() error {
	e.say("Palate print agent setup (version %s)", agent.Version)
	e.say("%s", strings.Repeat("=", 44))

	if info, err := e.status(); err == nil {
		e.say("\nThe print agent is already installed on this PC: %s.", info.State)
		e.say("  account: %s", info.Account)
		e.say("  config:  %s", e.cfgPath)
		if cfg, err := config.Load(e.cfgPath); err == nil {
			e.say("\nPrinters:")
			for _, p := range cfg.Printers {
				e.say("  %s", p.Describe())
			}
		}
		e.say("\nTo change a printer:  palate-agent printer set <id> -address <IP>   (as administrator)")
		e.say("To see the log:       palate-agent status")
		e.say("To remove it:         palate-agent uninstall")
		return nil
	} else if !errors.Is(err, service.ErrNotInstalled) {
		return fmt.Errorf("checking for an existing installation: %w", err)
	}

	if _, err := e.edgeFind(); err == nil {
		e.say("\nMicrosoft Edge: found (it draws customer bills).")
	} else {
		e.say("\nMicrosoft Edge wasn't found. Kitchen tickets will print, but customer bills can't")
		e.say("until Edge is installed or edge_path is set in the config.")
	}

	// 1. The config: use one that exists, or build it.
	var cfg *config.Config
	if _, err := os.Stat(e.cfgPath); err == nil {
		c, err := config.Load(e.cfgPath)
		if err != nil {
			return fmt.Errorf("the existing config has a problem: %w", err)
		}
		cfg = c
		e.say("\nUsing the existing config at %s:", e.cfgPath)
		for _, p := range cfg.Printers {
			e.say("  %s", p.Describe())
		}
	} else {
		c, err := e.buildConfig()
		if err != nil {
			return err
		}
		cfg = c
	}

	// 2. The service.
	e.say("\nStep 3 of 5: installing the Windows service...")
	if err := e.install(e.cfgPath); err != nil {
		return err
	}

	// 3. Pairing.
	if _, err := (pair.FileStore{Path: cfg.CredentialFile}).Load(); err == nil {
		e.say("\nStep 4 of 5: this PC is already paired.")
	} else {
		e.say("\nStep 4 of 5: pairing with Palate.")
		e.say("In the Palate portal, choose \"Add a till\" and type the code it shows (it expires in 10 minutes).")
		for {
			code := e.ask("Pairing code (or press Enter to pair later): ")
			if code == "" {
				e.say("\nNot paired yet. When you have a code, run as administrator:")
				e.say("  palate-agent pair -code <code>")
				e.say("  palate-agent start")
				return nil
			}
			dev, err := e.pair(cfg, strings.ToUpper(code))
			if err != nil {
				e.say("  That didn't work: %v", err)
				continue
			}
			e.say("  Paired as %s; Palate now knows this PC's printers.", dev)
			break
		}
	}

	// 4. Start.
	e.say("\nStep 5 of 5: starting the print agent...")
	if err := e.start(); err != nil {
		return fmt.Errorf("installed and paired, but starting failed: %w (try: palate-agent start)", err)
	}
	e.say("\nDone. The print agent is running and starts with Windows.")
	e.say("  Tickets and bills print as soon as Palate sends them.")
	e.say("  Check it any time:   palate-agent status")
	e.say("  Change a printer:    palate-agent printer set <id> -address <IP>   (as administrator)")
	e.say("  Its log:             %s", logPath(e.cfgPath))
	return nil
}

// buildConfig asks for the server and the printers, test-printing each, and
// saves the config.
func (e *setupEnv) buildConfig() (*config.Config, error) {
	e.say("\nStep 1 of 5: Palate's server.")
	var server string
	for {
		server = e.ask("Server address (it starts with https://): ")
		if err := config.CheckServer(server); err != nil {
			e.say("  %v", err)
			continue
		}
		break
	}
	raw := &config.Config{Server: server}

	e.say("\nStep 2 of 5: the printers.")
	all, _ := e.usbList()
	usb := realPrinters(all)
	if len(usb) > 0 {
		e.say("USB printers Windows can see:")
		for i, n := range usb {
			e.say("  %d. %s", i+1, n)
		}
	}
	e.say("A network printer needs its IP address (print its self-test page to find it).")

	for {
		p, ok := e.askPrinter(raw, usb)
		if !ok {
			continue
		}
		raw.Printers = append(raw.Printers, p)
		checked, err := raw.Check()
		if err != nil {
			e.say("  %v", err)
			raw.Printers = raw.Printers[:len(raw.Printers)-1]
			continue
		}
		if !e.testUntilOK(*checked.Find(p.ID), checked.Timings) {
			raw.Printers = raw.Printers[:len(raw.Printers)-1]
			e.say("  %s wasn't added. Enter its details again.", p.Name)
			continue
		}
		e.say("  Added %s.", checked.Find(p.ID).Describe())
		if !e.yes("Add another printer? [y/N] ", false) {
			break
		}
	}

	if len(raw.Printers) > 1 && e.yes("Give printers a backup printer for when they're down? [y/N] ", false) {
		for i := range raw.Printers {
			p := &raw.Printers[i]
			for {
				b := e.ask(fmt.Sprintf("  Backup for %s (an id, or Enter for none): ", p.Name))
				if b == "" {
					break
				}
				if b == p.ID || raw.Find(b) == nil {
					e.say("  Give the id of another printer above.")
					continue
				}
				p.Backup = b
				break
			}
		}
	}

	if err := os.MkdirAll(filepath.Dir(e.cfgPath), 0o755); err != nil {
		return nil, writeHint(err)
	}
	if err := config.Save(e.cfgPath, raw); err != nil {
		return nil, err
	}
	recordChange(e.cfgPath, "setup created the config with "+strconv.Itoa(len(raw.Printers))+" printer(s)")
	e.say("\nSaved the config to %s.", e.cfgPath)
	return config.Load(e.cfgPath)
}

var nonID = regexp.MustCompile(`[^a-z0-9]+`)

// askPrinter asks for one printer's name, id, where it is and its width.
func (e *setupEnv) askPrinter(raw *config.Config, usb []string) (config.Printer, bool) {
	e.say("")
	name := e.ask("Printer name, as the kitchen calls it (e.g. Tandoor, Pass, Bill): ")
	if name == "" {
		e.say("  A name is needed.")
		return config.Printer{}, false
	}
	suggested := suggestID(name, len(raw.Printers)+1)
	id := e.ask(fmt.Sprintf("Its id, which Palate uses and never changes [%s]: ", suggested))
	if id == "" {
		id = suggested
	}
	if !validID.MatchString(id) {
		e.say("  An id starts with a letter, then letters, digits, - or _.")
		return config.Printer{}, false
	}
	if raw.Find(id) != nil {
		e.say("  There's already a printer %s.", id)
		return config.Printer{}, false
	}
	p := config.Printer{ID: id, Name: name}
	prompt := "Its IP address: "
	if len(usb) > 0 {
		prompt = "Its IP address, or the number of a USB printer above: "
	}
	where := e.ask(prompt)
	if n, err := strconv.Atoi(where); err == nil && n >= 1 && n <= len(usb) {
		p.Kind, p.PrinterName = "usb", usb[n-1]
	} else {
		p.Kind, p.Address = "network", where
	}
	width := e.ask("Paper width in mm, 58 or 80 [80]: ")
	switch width {
	case "", "80":
		p.Width = 80
	case "58":
		p.Width = 58
	default:
		e.say("  The width is 58 or 80.")
		return config.Printer{}, false
	}
	return p, true
}

// testUntilOK test-prints until it works, the person skips the test, or
// they choose to re-enter the printer.
func (e *setupEnv) testUntilOK(p config.Printer, t config.Timings) bool {
	for {
		e.say("  Printing a test slip on %s...", p.Name)
		err := e.testPrint(p, t)
		if err == nil {
			return true
		}
		e.say("  The test print failed: %v", err)
		switch strings.ToLower(e.ask("  [r]etry, [s]kip the test and add it anyway, or [c]hange the details? ")) {
		case "s", "skip":
			return true
		case "c", "change":
			return false
		}
	}
}

func (e *setupEnv) say(format string, a ...any) { fmt.Fprintf(e.out, format+"\n", a...) }

// ask prints a prompt and returns the trimmed answer. At the end of input it
// returns "" (so prompts with a default take it).
func (e *setupEnv) ask(prompt string) string {
	fmt.Fprint(e.out, prompt)
	line, err := e.in.ReadString('\n')
	if err != nil && line == "" {
		fmt.Fprintln(e.out)
		panic(errInputEnded)
	}
	return strings.TrimSpace(line)
}

var errInputEnded = errors.New("input ended")

func (e *setupEnv) yes(prompt string, def bool) bool {
	switch strings.ToLower(e.ask(prompt)) {
	case "y", "yes":
		return true
	case "n", "no":
		return false
	}
	return def
}
