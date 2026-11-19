package main

// Finding network printers, for the setup window, the console setup and
// "palate-agent printer find".

import (
	"context"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/config"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/job"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/printer"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/printer/discover"
	netprinter "github.com/ShreyasBairyKS/Palate-Print-Agent/printer/net"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/render"
)

// findPrinters searches this PC's networks for receipt printers.
func findPrinters(ctx context.Context) ([]discover.Found, []discover.Network, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return discover.Find(ctx, discover.Options{})
}

// identifySlip is printed on a found printer so staff can see which printer
// has which address.
func identifySlip(addr string) job.Slip {
	return job.Slip{
		Station:     shortAddr(addr),
		OrderNumber: "ADDRESS",
		PlacedAt:    time.Now(),
		Items:       []job.Item{{Quantity: 1, Name: "This printer's address", Notes: shortAddr(addr)}},
		Notes:       "Palate print agent setup found this printer. Add it under the name the kitchen uses for it.",
	}
}

// identifyPrinter prints identifySlip on the printer at addr. It prints at
// 58 mm, which fits both paper widths.
func identifyPrinter(addr string) error {
	if !discover.IsPrinterAddr(addr) {
		return fmt.Errorf("%s isn't a private network printer address", addr)
	}
	data, err := render.Render(identifySlip(addr), job.Width58)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := (&netprinter.Writer{Addr: addr}).Write(ctx, data); err != nil {
		return fmt.Errorf("%s (%s): %w", shortAddr(addr), printer.KindOf(err), err)
	}
	return nil
}

// shortAddr drops the usual port: "192.168.1.50:9100" -> "192.168.1.50".
func shortAddr(addr string) string { return strings.TrimSuffix(addr, ":9100") }

// foundLabel describes one found device in a line, noting a printer this
// till already has at that address.
func foundLabel(f discover.Found, raw *config.Config) string {
	s := shortAddr(f.Addr)
	if f.Addr == discover.Local {
		s += " (this PC)"
	}
	if f.Answers {
		s += "   receipt printer"
	} else {
		s += "   something on the printer port (didn't answer; may be a printer)"
	}
	if f.Windows != "" {
		s += "   ·   Windows: " + f.Windows
	}
	if p := addedAt(raw, f.Addr); p != nil {
		s += "   ·   already added as " + p.Name
	}
	return s
}

// addedAt returns the printer in raw at addr, if any.
func addedAt(raw *config.Config, addr string) *config.Printer {
	if raw == nil {
		return nil
	}
	for i, p := range raw.Printers {
		if p.Kind != "network" {
			continue
		}
		a, err := config.CheckPrinterAddress(p.Address)
		if err == nil && a == addr {
			return &raw.Printers[i]
		}
	}
	return nil
}

// networksLabel says what was searched: "192.168.1.0/24 (Wi-Fi)".
func networksLabel(nets []discover.Network) string {
	var s []string
	for _, n := range nets {
		s = append(s, n.Prefix.String()+" ("+n.Interface+")")
	}
	if len(s) == 0 {
		return "no network (this PC isn't connected to a private network)"
	}
	return strings.Join(s, ", ")
}

func printerFind(args []string) error {
	fs := flag.NewFlagSet("printer find", flag.ExitOnError)
	path := fs.String("config", defaultConfigPath(), "path to agent.json (to mark printers already added)")
	ident := fs.Bool("identify", false, "print each receipt printer's address on it, to tell them apart")
	if err := fs.Parse(args); err != nil {
		return err
	}
	raw, _ := config.Read(*path)
	fmt.Println("Looking for receipt printers on this PC's network...")
	found, nets, err := findPrinters(context.Background())
	if err != nil && len(found) == 0 {
		return err
	}
	fmt.Println("searched: " + networksLabel(nets) + ", port 9100, and Windows' network printer ports")
	if len(found) == 0 {
		fmt.Println("No network printers found. Check the printer is on and cabled to the same network, or type its IP address.")
		return nil
	}
	for _, f := range found {
		fmt.Println("  " + foundLabel(f, raw))
	}
	if !*ident {
		fmt.Println("To tell them apart: palate-agent printer find -identify (each prints its own address).")
		return nil
	}
	for _, f := range found {
		if !f.Answers {
			continue
		}
		if err := identifyPrinter(f.Addr); err != nil {
			fmt.Println("  couldn't print on " + err.Error())
		} else {
			fmt.Println("  printed its address on " + shortAddr(f.Addr))
		}
	}
	return nil
}
