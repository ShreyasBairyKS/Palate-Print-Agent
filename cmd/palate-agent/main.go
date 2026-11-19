// Command palate-agent is the Palate print agent.
//
//	palate-agent printers                  # Windows printers this process can see (for usb)
//	palate-agent pair -code K7Q2-M9XD      # once per till, with a code from the portal
//	palate-agent test-print -printer p-tandoor
//	palate-agent run                       # foreground (or as the service, when Windows starts it)
//	palate-agent install                   # as administrator: install and start the Windows service
//	palate-agent status | start | stop | uninstall
//	palate-agent version
//
// Every command that reads the config takes -config (default
// %ProgramData%\PalatePrintAgent\agent.json).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/agent"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/api"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/config"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/job"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/pair"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/printer"
	usbprinter "github.com/ShreyasBairyKS/Palate-Print-Agent/printer/usb"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/render"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/service"
)

func defaultConfigPath() string {
	base := os.Getenv("ProgramData")
	if base == "" {
		base = "."
	}
	return filepath.Join(base, "PalatePrintAgent", "agent.json")
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: palate-agent <command> [flags]

commands:
  printers    list the Windows printers this process can see (for usb)
  pair        redeem a pairing code from the portal and register printers
  test-print  print a test slip on one printer
  run         poll for jobs and print them (foreground)
  install     install the Windows service and start it (as administrator)
  status      show whether the service is running, with recent log lines
  start       start the service (as administrator)
  stop        stop the service, letting tickets in hand finish (as administrator)
  uninstall   stop and remove the service; the data folder is kept (as administrator)
  version     print the agent version`)
	os.Exit(2)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "printers":
		err = cmdPrinters()
	case "pair":
		err = cmdPair(args, log)
	case "test-print":
		err = cmdTestPrint(args)
	case "run":
		err = cmdRun(args, log)
	case "install":
		err = cmdInstall(args)
	case "uninstall":
		err = cmdUninstall()
	case "start":
		err = cmdStart()
	case "stop":
		err = cmdStop()
	case "status":
		err = cmdStatus(args)
	case "version":
		fmt.Println(agent.Version)
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "palate-agent:", err)
		os.Exit(1)
	}
}

func loadConfig(fs *flag.FlagSet, args []string) (*config.Config, error) {
	path := fs.String("config", defaultConfigPath(), "path to agent.json")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	return config.Load(*path)
}

func cmdPrinters() error {
	names, err := usbprinter.List()
	if err != nil {
		return err
	}
	fmt.Println("Windows printers visible to this process (use one as printer_name for a usb printer):")
	for _, n := range names {
		fmt.Println("  " + n)
	}
	fmt.Println("The service runs as its own account (NT SERVICE\\PalatePrintAgent), which sees only printers installed for all users.")
	return nil
}

func cmdPair(args []string, log *slog.Logger) error {
	fs := flag.NewFlagSet("pair", flag.ExitOnError)
	code := fs.String("code", "", "pairing code from the portal")
	cfg, err := loadConfig(fs, args)
	if err != nil {
		return err
	}
	if *code == "" {
		return errors.New("pair: -code is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store := pair.FileStore{Path: cfg.CredentialFile}
	dev, err := pair.Redeem(ctx, agent.NewHTTPClient(20*time.Second), cfg.Server, *code, cfg.AgentName, store)
	if err != nil {
		return err
	}
	a, err := agent.New(cfg, store, log)
	if err != nil {
		return err
	}
	defer a.Close()
	if err := a.Cloud.RegisterPrinters(ctx, toAPI(cfg)); err != nil {
		return fmt.Errorf("paired as %s, but registering printers failed (run will retry): %w", dev, err)
	}
	fmt.Printf("paired as %s; %d printer(s) registered. Run a test print on each before service.\n", dev, len(cfg.Printers))
	return nil
}

func cmdTestPrint(args []string) error {
	fs := flag.NewFlagSet("test-print", flag.ExitOnError)
	id := fs.String("printer", "", "printer id from agent.json")
	cfg, err := loadConfig(fs, args)
	if err != nil {
		return err
	}
	for _, p := range cfg.Printers {
		if p.ID != *id {
			continue
		}
		data, err := render.Render(testSlip(p), job.Width(p.Width))
		if err != nil {
			return err
		}
		res, err := agent.WriterFor(p, cfg.Timings).Write(context.Background(), data)
		switch {
		case err != nil:
			return fmt.Errorf("test print on %s failed (%s): %w", p.ID, printer.KindOf(err), err)
		case res.Confirmed:
			fmt.Printf("printed on %s; the printer confirmed it\n", p.ID)
		default:
			fmt.Printf("written to %s; the printer can't confirm, so check the paper\n", p.ID)
		}
		return nil
	}
	return fmt.Errorf("test-print: no printer with id %q in the config", *id)
}

// testSlip exercises everything a real printer can get wrong in one ticket:
// the large bold station name, bold item lines, accented letters (the code
// page), Kannada (the raster path), an extra line, and the cut at the end.
func testSlip(p config.Printer) job.Slip {
	return job.Slip{
		Station:     p.Name,
		OrderNumber: "TEST",
		PlacedAt:    time.Now(),
		Extras: []job.Extra{
			{Label: "Printer", Value: fmt.Sprintf("%s at %s, %d mm", p.ID, where(p), p.Width)},
		},
		Items: []job.Item{
			{Quantity: 1, Name: "Test print", Notes: "if you can read this, text works"},
			{Quantity: 2, Name: "Café crème brûlée", Notes: "accents: é è ê à ç ñ ü ö €"},
			{Quantity: 1, Name: "ಮಸಾಲೆ ದೋಸೆ", Notes: "Kannada, printed as an image"},
		},
		Notes: "Wrong letters or boxes above? Check the printer's code page. The paper should be cut below.",
	}
}

func where(p config.Printer) string {
	if p.Kind == "usb" {
		return p.PrinterName
	}
	return p.Address
}

func cmdRun(args []string, log *slog.Logger) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	path := fs.String("config", defaultConfigPath(), "path to agent.json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if isSvc, err := service.IsService(); err == nil && isSvc {
		return runService(*path)
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	// Ctrl-C stops cleanly: tickets in hand finish first.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return runAgent(ctx, cfg, log)
}

func toAPI(cfg *config.Config) []api.Printer {
	out := make([]api.Printer, len(cfg.Printers))
	for i, p := range cfg.Printers {
		out[i] = api.Printer{ID: p.ID, Name: p.Name, Kind: p.Kind, Width: p.Width}
	}
	return out
}
