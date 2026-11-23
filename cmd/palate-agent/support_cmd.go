package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// cmdSupport is "Send to support..." for a terminal: it makes the report,
// saves it on the Desktop (or in the current folder) and prints it.
func cmdSupport(args []string) error {
	fs := flag.NewFlagSet("support", flag.ExitOnError)
	path := fs.String("config", defaultConfigPath(), "path to agent.json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	report := makeSupportReport(*path)
	fmt.Println(report)
	dir := desktopDir()
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		dir, _ = os.Getwd()
	}
	saved, err := saveReport(dir, report, time.Now())
	if err != nil {
		return fmt.Errorf("the report is above, but it couldn't be saved: %w", err)
	}
	fmt.Println("Saved as " + saved + ". Send that file to Palate support.")
	return nil
}

// capitalize makes the first letter a capital.
func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// browserLabel names the browser at path, the one that draws bills.
func browserLabel(path string) string {
	b := strings.ToLower(filepath.Base(path))
	switch {
	case strings.Contains(b, "edge"):
		return "Microsoft Edge"
	case strings.Contains(b, "chromium"):
		return "Chromium"
	case strings.Contains(b, "chrome"):
		return "Google Chrome"
	}
	return capitalize(browserName)
}
