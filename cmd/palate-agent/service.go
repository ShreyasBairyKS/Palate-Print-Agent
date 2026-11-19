package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/agent"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/config"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/internal/fileacl"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/internal/logfile"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/pair"
	"github.com/ShreyasBairyKS/Palate-Print-Agent/service"
)

var errNeedAdmin = errors.New("this needs administrator rights: open a terminal with \"Run as administrator\" and run it again")

// logPath is where the service writes its log: logs\agent.log beside the
// config.
func logPath(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "logs", "agent.log")
}

// installedExe is where install puts the program: a service account can't
// read files in a user's folders (Desktop, Downloads, OneDrive).
func installedExe() string {
	base := os.Getenv("ProgramFiles")
	if base == "" {
		base = `C:\Program Files`
	}
	return filepath.Join(base, "PalatePrintAgent", "palate-agent.exe")
}

func cmdInstall(args []string) error {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	path := fs.String("config", defaultConfigPath(), "path to agent.json")
	account := fs.String("account", "service", `"service" (its own low-privilege account, recommended) or "localsystem"`)
	start := fs.Bool("start", true, "start the service once installed (if the till is paired)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !service.IsElevated() {
		return errNeedAdmin
	}
	return installService(*path, *account, *start)
}

// installService installs (and optionally starts) the service for the
// config at path. The caller has checked for administrator rights.
func installService(path, account string, start bool) error {
	var acct string
	switch strings.ToLower(account) {
	case "service":
		acct = service.VirtualAccount
	case "localsystem":
		acct = service.LocalSystem
	default:
		return fmt.Errorf("install: -account must be service or localsystem, not %q", account)
	}
	cfgPath, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if _, err := os.Stat(cfgPath); errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("install: there's no config at %s yet. Double-click palate-agent.exe for guided setup, or create it first with: palate-agent init <id> -server https://... -name NAME -address IP", cfgPath)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	for _, p := range []string{cfgPath, cfg.CredentialFile, cfg.LedgerFile} {
		if inUserFolder(p) {
			return fmt.Errorf("install: %s is inside a user's folder, which the service can't read; put agent.json in %s", p, filepath.Dir(defaultConfigPath()))
		}
	}

	// The program goes to Program Files.
	exe := installedExe()
	if err := copySelf(exe); err != nil {
		return fmt.Errorf("install: copying the program to %s: %w", exe, err)
	}

	// The data folder: only SYSTEM, Administrators and the service may open
	// it. The credential and ledger keep their own (stricter) access lists,
	// updated so the service can read ones made before this install.
	dataDir := filepath.Dir(cfgPath)
	if err := fileacl.RestrictDir(dataDir); err != nil {
		return fmt.Errorf("install: %w", err)
	}
	for _, f := range []string{cfg.CredentialFile, cfg.LedgerFile} {
		if _, err := os.Stat(f); err == nil {
			if err := fileacl.Restrict(f); err != nil {
				return fmt.Errorf("install: %w", err)
			}
		}
	}

	if err := service.Install(exe, []string{"run", "-config", cfgPath}, acct); err != nil {
		return fmt.Errorf("install: %w", err)
	}
	fmt.Printf("installed the %s service\n", service.DisplayName)
	fmt.Printf("  program:  %s\n", exe)
	fmt.Printf("  config:   %s\n", cfgPath)
	fmt.Printf("  log:      %s\n", logPath(cfgPath))
	fmt.Printf("  account:  %s\n", acct)
	fmt.Println("  starts with Windows; restarts after a failure (5 s, 30 s, 60 s)")
	if hasUSB(cfg) && acct == service.VirtualAccount {
		fmt.Println("  note: USB printers must be installed for all users. If the log says the service")
		fmt.Println("        can't see one, reinstall with -account localsystem.")
	}

	if !start {
		fmt.Println("start it with: palate-agent start")
		return nil
	}
	if _, err := (pair.FileStore{Path: cfg.CredentialFile}).Load(); err != nil {
		fmt.Println("not started: this till isn't paired yet. Run palate-agent pair -code <code>, then palate-agent start")
		return nil
	}
	if err := service.Start(); err != nil {
		return fmt.Errorf("installed, but starting failed: %w", err)
	}
	fmt.Println("started")
	return nil
}

func cmdUninstall() error {
	if !service.IsElevated() {
		return errNeedAdmin
	}
	if err := service.Uninstall(); err != nil {
		return err
	}
	exe := installedExe()
	if self, err := os.Executable(); err == nil && !samePath(self, exe) {
		os.Remove(exe)
		os.Remove(filepath.Dir(exe))
	}
	fmt.Printf("removed the %s service\n", service.DisplayName)
	fmt.Printf("the data folder (config, credential, ledger, logs) is kept: %s\n", filepath.Dir(defaultConfigPath()))
	return nil
}

func cmdStart() error {
	if !service.IsElevated() {
		return errNeedAdmin
	}
	if err := service.Start(); err != nil {
		return err
	}
	fmt.Println("started")
	return nil
}

func cmdStop() error {
	if !service.IsElevated() {
		return errNeedAdmin
	}
	if err := service.Stop(); err != nil {
		return err
	}
	fmt.Println("stopped")
	return nil
}

func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	path := fs.String("config", defaultConfigPath(), "path to agent.json")
	lines := fs.Int("log", 10, "how many recent log lines to show")
	if err := fs.Parse(args); err != nil {
		return err
	}
	info, err := service.Status()
	if errors.Is(err, service.ErrNotInstalled) {
		fmt.Println("the service is not installed (install it with: palate-agent install)")
		return nil
	}
	if err != nil {
		return err
	}
	fmt.Printf("%s: %s\n", service.DisplayName, info.State)
	if info.PID != 0 {
		fmt.Printf("  process:    %d\n", info.PID)
	}
	fmt.Printf("  account:    %s\n", info.Account)
	fmt.Printf("  starts with Windows: %v\n", info.AutoStart)
	fmt.Printf("  command:    %s\n", info.Command)
	if info.LastExitErr != "" {
		fmt.Printf("  %s\n", info.LastExitErr)
	}
	if *lines > 0 {
		lp := logPath(*path)
		tail, err := tailLines(lp, *lines)
		switch {
		case err == nil && len(tail) > 0:
			fmt.Printf("\nlast %d log lines (%s):\n", len(tail), lp)
			for _, l := range tail {
				fmt.Println("  " + l)
			}
		case errors.Is(err, os.ErrPermission):
			fmt.Println("\nthe log is readable by administrators only; run this as administrator to see it")
		}
	}
	return nil
}

// runAgent is the agent itself, whether in a terminal or as the service.
func runAgent(ctx context.Context, cfg *config.Config, log *slog.Logger) error {
	store := pair.FileStore{Path: cfg.CredentialFile}
	if _, err := store.Load(); err != nil {
		return err
	}
	log.Info("palate-agent starting", "version", agent.Version, "server", cfg.Server, "printers", len(cfg.Printers))
	agent.CheckUSB(cfg, log)
	a, err := agent.New(cfg, store, log)
	if err != nil {
		return err
	}
	defer a.Close()
	return a.Run(ctx)
}

// runService is cmdRun when Windows started the process: logs go to the
// rotating log file, and Windows decides when to stop.
func runService(cfgPath string) error {
	w := &logfile.Writer{Path: logPath(cfgPath)}
	defer w.Close()
	log := slog.New(slog.NewTextHandler(w, nil))
	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Error("cannot start: bad config", "path", cfgPath, "err", err)
		return err
	}
	log.Info("service starting", "version", agent.Version, "config", cfgPath)
	return service.Run(func(ctx context.Context) error { return runAgent(ctx, cfg, log) }, log)
}

func hasUSB(cfg *config.Config) bool {
	for _, p := range cfg.Printers {
		if p.Kind == "usb" {
			return true
		}
	}
	return false
}

// inUserFolder reports whether path is under the folder holding users'
// profiles (C:\Users).
func inUserFolder(path string) bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	users := strings.ToLower(filepath.Dir(home)) + string(filepath.Separator)
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	return strings.HasPrefix(strings.ToLower(abs), users)
}

func samePath(a, b string) bool {
	aa, err1 := filepath.Abs(a)
	bb, err2 := filepath.Abs(b)
	return err1 == nil && err2 == nil && strings.EqualFold(aa, bb)
}

// copySelf copies the running program to dst (via a temporary file, so a
// failed copy never leaves half a program).
func copySelf(dst string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if samePath(self, dst) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(self)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".palate-agent-*.exe")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}

// tailLines returns the last n lines of a file.
func tailLines(path string, n int) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		lines = append(lines, sc.Text())
		if len(lines) > n {
			lines = lines[1:]
		}
	}
	return lines, sc.Err()
}
