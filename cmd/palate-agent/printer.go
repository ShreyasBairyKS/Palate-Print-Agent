package main

// palate-agent printer: change this till's printers without editing
// agent.json by hand, and without Palate's server. Every change is checked
// by the same rules as the config file, test-printed before it is saved,
// saved in one step (the old file kept as agent.json.previous), written to
// logs\config-changes.log, and applied by restarting the service.
//
// What a restaurant may change here is what only this till knows: where a
// printer is (address or Windows printer name), its paper width, its name,
// and its backup on this till. Which station prints on which printer, and
// which dishes a station cooks, belong to Palate; the server address and
// credential belong to pairing. None of those can be changed here, and a
// printer's id can't be renamed, because the id is what Palate's jobs name.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/agent"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/config"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/job"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/printer"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/render"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/service"
)

const printerUsage = `usage: palate-agent printer <list|set|add|remove> [id] [flags]

  printer list
  printer set <id> [-address IP[:port] | -usb "Windows printer name"] [-width 58|80] [-name NAME] [-backup ID|none]
  printer add <id> -name NAME (-address IP[:port] | -usb "Windows printer name") [-width 58|80] [-backup ID]
  printer remove <id>

Every change is test-printed before it is saved (-no-test skips that), then
the service is restarted so it takes effect (-no-restart skips that). Run it
from a terminal opened with "Run as administrator".

Which station prints on which printer, and what each station cooks, are set
in Palate, not here. A printer's id can't be changed: Palate's jobs name it.`

func cmdPrinter(args []string) error {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, printerUsage)
		os.Exit(2)
	}
	switch args[0] {
	case "list":
		return printerList(args[1:])
	case "set":
		return printerChange("set", args[1:])
	case "add":
		return printerChange("add", args[1:])
	case "remove":
		return printerChange("remove", args[1:])
	default:
		fmt.Fprintln(os.Stderr, printerUsage)
		os.Exit(2)
	}
	return nil
}

func printerList(args []string) error {
	fs := flag.NewFlagSet("printer list", flag.ExitOnError)
	path := fs.String("config", defaultConfigPath(), "path to agent.json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	fmt.Printf("printers on this till (%s):\n", *path)
	for _, p := range cfg.Printers {
		fmt.Println("  " + p.Describe())
	}
	fmt.Println("Palate sees each printer's id, name, kind and width, never its address.")
	return nil
}

// validID is what a new printer's id may look like: short, plain, and never
// mistakable for an address.
var validID = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)

func printerChange(verb string, args []string) error {
	// The id comes first (printer set p-tandoor -address ...); the flag
	// package stops at the first non-flag, so take it off before parsing.
	var id string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		id, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("printer "+verb, flag.ExitOnError)
	path := fs.String("config", defaultConfigPath(), "path to agent.json")
	name := fs.String("name", "", "printer name, as printed on BACKUP FOR banners")
	address := fs.String("address", "", "network printer: private IP address, optionally with :port (default 9100)")
	usb := fs.String("usb", "", "USB printer: its name in Windows (see palate-agent printers)")
	width := fs.Int("width", 0, "paper width in mm: 58 or 80")
	backup := fs.String("backup", "", "id of another printer on this till to use when this one is down, or none")
	noTest := fs.Bool("no-test", false, "save without a test print (say, the printer is off right now)")
	noRestart := fs.Bool("no-restart", false, "save without restarting the service")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if id == "" {
		return fmt.Errorf("printer %s: which printer? give its id first, e.g. palate-agent printer %s p-tandoor ...", verb, verb)
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("printer %s: unexpected %q (flags go after the id)", verb, fs.Arg(0))
	}
	if *address != "" && *usb != "" {
		return errors.New("a printer is either network (-address) or USB (-usb), not both")
	}

	raw, err := config.Read(*path)
	if err != nil {
		return err
	}
	var before *config.Printer
	var testNeeded bool
	switch verb {
	case "set":
		p := raw.Find(id)
		if p == nil {
			return unknownPrinter(raw, id)
		}
		b := *p
		before = &b
		if *name == "" && *address == "" && *usb == "" && *width == 0 && *backup == "" {
			return errors.New("printer set: nothing to change; give -address, -usb, -width, -name or -backup")
		}
		applyFlags(p, *name, *address, *usb, *width, *backup)
		testNeeded = p.Kind != before.Kind || p.Address != before.Address || p.PrinterName != before.PrinterName || p.Width != before.Width
	case "add":
		if !validID.MatchString(id) {
			return fmt.Errorf("id %q: start with a letter, then letters, digits, - or _ (up to 64)", id)
		}
		if raw.Find(id) != nil {
			return fmt.Errorf("there is already a printer %q on this till; use printer set to change it", id)
		}
		if *name == "" || (*address == "" && *usb == "") {
			return errors.New("printer add: -name and one of -address or -usb are required")
		}
		p := config.Printer{ID: id, Width: 80}
		applyFlags(&p, *name, *address, *usb, *width, *backup)
		raw.Printers = append(raw.Printers, p)
		testNeeded = true
	case "remove":
		p := raw.Find(id)
		if p == nil {
			return unknownPrinter(raw, id)
		}
		b := *p
		before = &b
		if err := raw.Remove(id); err != nil {
			return fmt.Errorf("can't remove %s: %w", id, err)
		}
	}

	checked, err := raw.Check()
	if err != nil {
		return fmt.Errorf("not saved: %w", err)
	}

	if testNeeded && !*noTest {
		p := checked.Find(id)
		if err := testPrint(*p, checked.Timings); err != nil {
			return fmt.Errorf("not saved: %w\nCheck the printer is on, has paper and is at that address, then try again; or add -no-test to save anyway", err)
		}
	}

	if err := config.Save(*path, raw); err != nil {
		return err
	}
	change := describeChange(verb, before, checked.Find(id))
	fmt.Println("saved: " + change)
	recordChange(*path, change)

	switch verb {
	case "add":
		fmt.Println("Palate is told about the new printer when the service restarts, but sends it nothing until a station or the bill is linked to it in Palate.")
	case "remove":
		fmt.Println("Unlink it in Palate too: jobs Palate still sends to it are recorded as failed (not configured).")
	case "set":
		if before.Width != checked.Find(id).Width {
			fmt.Println("Palate is told the new paper width when the service restarts, so bills are drawn for it.")
		}
	}

	if *noRestart {
		fmt.Println("not restarted: the change takes effect the next time the agent starts")
		return nil
	}
	fmt.Println(restartService(*path))
	return nil
}

// applyFlags sets the fields the flags name. -address makes a printer a
// network one; -usb makes it a USB one.
func applyFlags(p *config.Printer, name, address, usb string, width int, backup string) {
	if name != "" {
		p.Name = name
	}
	if address != "" {
		p.Kind, p.Address, p.PrinterName = "network", address, ""
	}
	if usb != "" {
		p.Kind, p.PrinterName, p.Address = "usb", usb, ""
	}
	if width != 0 {
		p.Width = width
	}
	switch strings.ToLower(backup) {
	case "":
	case "none":
		p.Backup = ""
	default:
		p.Backup = backup
	}
}

func unknownPrinter(c *config.Config, id string) error {
	var ids []string
	for _, p := range c.Printers {
		ids = append(ids, p.ID)
	}
	return fmt.Errorf("no printer %q on this till (printers: %s)", id, strings.Join(ids, ", "))
}

func describeChange(verb string, before, after *config.Printer) string {
	switch verb {
	case "add":
		return "added " + after.Describe()
	case "remove":
		return "removed " + before.Describe()
	}
	return before.Describe() + "  ->  " + after.Describe()
}

// testPrint prints the test slip on one printer.
func testPrint(p config.Printer, t config.Timings) error {
	data, err := render.Render(testSlip(p), job.Width(p.Width))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := agent.WriterFor(p, t).Write(ctx, data)
	if err != nil {
		return fmt.Errorf("test print on %s failed (%s): %w", p.ID, printer.KindOf(err), err)
	}
	if res.Confirmed {
		fmt.Printf("test print on %s: the printer confirmed it\n", p.ID)
	} else {
		fmt.Printf("test print on %s: written; this printer can't confirm, so check the paper\n", p.ID)
	}
	return nil
}

// recordChange appends one line to logs\config-changes.log beside the
// config: when, who, and what changed. Support can see later why a
// printer's address moved. A failure to write it doesn't undo the change.
func recordChange(cfgPath, change string) {
	dir := filepath.Join(filepath.Dir(cfgPath), "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, "config-changes.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	user := os.Getenv("USERNAME")
	if d := os.Getenv("USERDOMAIN"); d != "" && user != "" {
		user = d + `\` + user
	}
	fmt.Fprintf(f, "%s  %s  %s\n", time.Now().Format(time.RFC3339), user, change)
}

// restartService restarts the installed service if it runs this config, so
// a change takes effect now, and says what happened.
func restartService(cfgPath string) string {
	info, err := service.Status()
	if errors.Is(err, service.ErrNotInstalled) {
		return "the service isn't installed; the change takes effect the next time the agent starts"
	}
	if err != nil {
		return "couldn't check the service (" + err.Error() + "); restart it with palate-agent stop, then palate-agent start"
	}
	abs, _ := filepath.Abs(cfgPath)
	if !strings.Contains(strings.ToLower(info.Command), strings.ToLower(abs)) {
		return "the service runs a different config (" + info.Command + "), so it wasn't restarted"
	}
	if info.State != "running" {
		return "the service is " + info.State + "; the change takes effect when it starts"
	}
	if !service.IsElevated() {
		return "restart the service as administrator for the change to take effect: palate-agent stop, then palate-agent start"
	}
	if err := service.Stop(); err != nil {
		return "couldn't stop the service (" + err.Error() + "); restart it with palate-agent stop, then palate-agent start"
	}
	if err := service.Start(); err != nil {
		return "stopped the service but couldn't start it again (" + err.Error() + "): run palate-agent start"
	}
	return "restarted the service: the change is in effect, and Palate has been sent the printer list"
}
