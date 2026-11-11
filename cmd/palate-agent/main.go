// Command palate-agent is the Palate print agent.
//
//	palate-agent pair -code K7Q2-M9XD      # once per till, with a code from the portal
//	palate-agent test-print -printer p-tandoor
//	palate-agent run                       # foreground; the Windows service arrives in M4
//	palate-agent version
//
// Every command takes -config (default %ProgramData%\PalatePrintAgent\agent.json).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
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
	netprinter "github.com/ShreyasBairyKS/Palate-Print-Agent/printer/net"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/render"
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
  pair        redeem a pairing code from the portal and register printers
  test-print  print a test slip on one printer
  run         poll for jobs and print them (foreground)
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
	case "pair":
		err = cmdPair(args, log)
	case "test-print":
		err = cmdTestPrint(args)
	case "run":
		err = cmdRun(args, log)
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
	dev, err := pair.Redeem(ctx, &http.Client{Timeout: 20 * time.Second}, cfg.Server, *code, cfg.AgentName, store)
	if err != nil {
		return err
	}
	a := agent.New(cfg, store, log)
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
		data, err := render.Render(job.Slip{
			Station:     p.Name,
			OrderNumber: "TEST",
			Table:       "",
			PlacedAt:    time.Now(),
			Items: []job.Item{{Quantity: 1, Name: "Test print",
				Notes: fmt.Sprintf("printer %s at %s, %d mm. If you can read this, it works.", p.ID, p.Address, p.Width)}},
		}, job.Width(p.Width))
		if err != nil {
			return err
		}
		w := &netprinter.Writer{Addr: p.Address, ConnectTimeout: cfg.Timings.ConnectTimeout.D(),
			StallTimeout: cfg.Timings.StallTimeout.D(), StatusTimeout: cfg.Timings.StatusTimeout.D()}
		res, err := w.Write(context.Background(), data)
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

func cmdRun(args []string, log *slog.Logger) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	cfg, err := loadConfig(fs, args)
	if err != nil {
		return err
	}
	store := pair.FileStore{Path: cfg.CredentialFile}
	if _, err := store.Load(); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	log.Info("palate-agent starting", "version", agent.Version, "server", cfg.Server, "printers", len(cfg.Printers))
	return agent.New(cfg, store, log).Run(ctx)
}

func toAPI(cfg *config.Config) []api.Printer {
	out := make([]api.Printer, len(cfg.Printers))
	for i, p := range cfg.Printers {
		out[i] = api.Printer{ID: p.ID, Name: p.Name, Kind: p.Kind, Width: p.Width}
	}
	return out
}
